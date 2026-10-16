/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package upgrade contains tests that verify a kcp server upgrade: a previous
// kcp version is started and seeded with data, then shut down and restarted
// from the same root directory using the current version. The test asserts
// that pre-upgrade data survives and that the upgraded server is fully
// functional.
//
// The binaries under test are provided via environment variables (see
// upgradeFromBinaryEnv and upgradeToBinaryEnv); without them the test skips.
// Use `make test-upgrade` to run the full flow.
package upgrade

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
)

const (
	// upgradeFromBinaryEnv points to the kcp binary of the version to upgrade
	// from, usually the latest release. See hack/download-kcp-release.sh.
	upgradeFromBinaryEnv = "KCP_UPGRADE_FROM_BINARY"
	// upgradeToBinaryEnv points to the kcp binary of the version to upgrade
	// to, usually bin/kcp built from the current tree.
	upgradeToBinaryEnv = "KCP_UPGRADE_TO_BINARY"
	// upgradeE2EPackagesEnv optionally lists go packages (space-separated)
	// of e2e tests to run against the upgraded server, e.g.
	// "./test/e2e/apibinding/...". The packages must be compatible with the
	// shared external server mode (--kcp-kubeconfig).
	upgradeE2EPackagesEnv = "KCP_UPGRADE_E2E_PACKAGES"

	seedTimeout = 3 * time.Minute
)

func TestUpgrade(t *testing.T) {
	t.Parallel()

	fromBinary := os.Getenv(upgradeFromBinaryEnv)
	if fromBinary == "" {
		t.Skipf("%s is not set, skipping upgrade test. Run `make test-upgrade` to run the full flow.", upgradeFromBinaryEnv)
	}
	toBinary := os.Getenv(upgradeToBinaryEnv)
	if toBinary == "" {
		t.Skipf("%s is not set, skipping upgrade test. Run `make test-upgrade` to run the full flow.", upgradeToBinaryEnv)
	}

	artifactDir, dataDir, err := kcptestingserver.ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	group := "upgrade.test.kcp.io"
	rootPath := core.RootCluster.Path()
	orgPath := rootPath.Join("upgrade-org")
	providerPath := orgPath.Join("provider")
	consumerPath := orgPath.Join("consumer")
	sheriffsGVR := schema.GroupVersionResource{Group: group, Version: "v1", Resource: "sheriffs"}

	var preUpgradeSheriffUID types.UID
	var preUpgradeShardCount int

	t.Logf("Phase 1: starting previous version %q and seeding data", fromBinary)
	oldServer := startKcp(ctx, t, fromBinary, "old", dataDir, artifactDir, "")
	{
		kcpClient, err := kcpclientset.NewForConfig(oldServer.baseConfig(ctx, t))
		require.NoError(t, err)
		kubeClient, err := kcpkubernetesclientset.NewForConfig(oldServer.baseConfig(ctx, t))
		require.NoError(t, err)
		dynamicClient, err := kcpdynamic.NewForConfig(oldServer.baseConfig(ctx, t))
		require.NoError(t, err)

		createWorkspace(ctx, t, kcpClient, rootPath, "upgrade-org")
		createWorkspace(ctx, t, kcpClient, orgPath, "provider")
		createWorkspace(ctx, t, kcpClient, orgPath, "consumer")

		apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClient, group, "pre-upgrade export")
		apifixtures.BindToExport(ctx, t, providerPath, group, consumerPath, kcpClient)
		createSheriff(ctx, t, dynamicClient, consumerPath, sheriffsGVR, "pre-upgrade")

		sheriff, err := dynamicClient.Cluster(consumerPath).Resource(sheriffsGVR).Namespace("default").Get(ctx, "pre-upgrade", metav1.GetOptions{})
		require.NoError(t, err, "failed to get seeded sheriff")
		preUpgradeSheriffUID = sheriff.GetUID()

		_, err = kubeClient.Cluster(consumerPath).CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "upgrade-marker"},
			Data:       map[string]string{"seeded-by": "pre-upgrade"},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create marker ConfigMap")

		shards, err := kcpClient.Cluster(rootPath).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "failed to list shards before upgrade")
		require.NotEmpty(t, shards.Items, "expected at least one shard before upgrade")
		preUpgradeShardCount = len(shards.Items)
	}
	t.Log("Phase 1: stopping previous version")
	oldServer.stop(t)

	t.Logf("Phase 2: starting current version %q on the same root directory", toBinary)
	newServer := startKcp(ctx, t, toBinary, "new", dataDir, artifactDir, oldServer.host)
	t.Cleanup(func() { newServer.stop(t) })

	t.Log("Phase 3: verifying pre-upgrade data and post-upgrade functionality")
	kcpClient, err := kcpclientset.NewForConfig(newServer.baseConfig(ctx, t))
	require.NoError(t, err)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(newServer.baseConfig(ctx, t))
	require.NoError(t, err)
	dynamicClient, err := kcpdynamic.NewForConfig(newServer.baseConfig(ctx, t))
	require.NoError(t, err)

	t.Log("Verifying seeded workspaces are ready")
	for _, ws := range []struct {
		parent logicalcluster.Path
		name   string
	}{
		{rootPath, "upgrade-org"},
		{orgPath, "provider"},
		{orgPath, "consumer"},
	} {
		waitWorkspaceReady(ctx, t, kcpClient, ws.parent, ws.name)
	}

	t.Log("Verifying the APIBinding is still bound")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, group, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if !conditions.IsTrue(binding, apisv1alpha2.InitialBindingCompleted) {
			return false, fmt.Sprintf("InitialBindingCompleted: %v", conditions.Get(binding, apisv1alpha2.InitialBindingCompleted))
		}
		return true, ""
	}, seedTimeout, 500*time.Millisecond, "APIBinding %s|%s did not become bound after upgrade", consumerPath, group)

	t.Log("Verifying the seeded sheriff survived the upgrade")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sheriff, err := dynamicClient.Cluster(consumerPath).Resource(sheriffsGVR).Namespace("default").Get(ctx, "pre-upgrade", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if sheriff.GetUID() != preUpgradeSheriffUID {
			return false, fmt.Sprintf("UID changed across the upgrade: had %q, got %q", preUpgradeSheriffUID, sheriff.GetUID())
		}
		return true, ""
	}, seedTimeout, 500*time.Millisecond, "seeded sheriff %s|default/pre-upgrade not served after upgrade", consumerPath)

	t.Log("Verifying the marker ConfigMap survived the upgrade")
	cm, err := kubeClient.Cluster(consumerPath).CoreV1().ConfigMaps("default").Get(ctx, "upgrade-marker", metav1.GetOptions{})
	require.NoError(t, err, "failed to get marker ConfigMap after upgrade")
	require.Equal(t, "pre-upgrade", cm.Data["seeded-by"], "marker ConfigMap data changed across the upgrade")

	t.Log("Verifying shards are registered and ready")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		shards, err := kcpClient.Cluster(rootPath).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(shards.Items) != preUpgradeShardCount {
			return false, fmt.Sprintf("expected %d shards, got %d", preUpgradeShardCount, len(shards.Items))
		}
		for _, shard := range shards.Items {
			if shard.Spec.BaseURL == "" {
				return false, fmt.Sprintf("shard %s has no baseURL", shard.Name)
			}
		}
		return true, ""
	}, seedTimeout, 500*time.Millisecond, "shards did not settle after upgrade")

	t.Log("Verifying new bound resources can be created after the upgrade")
	createSheriff(ctx, t, dynamicClient, consumerPath, sheriffsGVR, "post-upgrade")

	t.Log("Verifying new workspaces can be created after the upgrade")
	createWorkspace(ctx, t, kcpClient, orgPath, "post-upgrade")

	if pkgs := os.Getenv(upgradeE2EPackagesEnv); pkgs != "" {
		t.Logf("Phase 4: running e2e packages against the upgraded server: %s", pkgs)
		runE2E(ctx, t, strings.Fields(pkgs), newServer.kubeconfigPath)
	}
}

// kcpProcess is a minimal external kcp server runner. Unlike the SDK test
// fixture it does not monitor the server's health endpoints for the lifetime
// of the test, which allows stopping the server mid-test without failing it.
type kcpProcess struct {
	phase          string
	kubeconfigPath string
	host           string
	cmd            *exec.Cmd
	done           chan struct{}
	stopOnce       sync.Once
}

// startKcp starts the given kcp binary against dataDir and waits for it to
// become ready.
//
// All phases share dataDir/admin.kubeconfig: kcp persists only the hash of
// the shard-admin token in the root directory and recovers the token itself
// from the existing kubeconfig on restart, so the file must survive across
// phases. kcp rewrites it with the current server address early during
// startup; previousHost guards against reading the previous phase's stale
// copy before that happens.
func startKcp(ctx context.Context, t *testing.T, binary, phase, dataDir, artifactDir, previousHost string) *kcpProcess {
	t.Helper()

	securePort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	etcdClientPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	etcdPeerPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)

	kubeconfigPath := filepath.Join(dataDir, "admin.kubeconfig")
	args := []string{
		"start",
		"--root-directory", dataDir,
		"--secure-port", securePort,
		"--embedded-etcd-client-port", etcdClientPort,
		"--embedded-etcd-peer-port", etcdPeerPort,
		"--kubeconfig-path", kubeconfigPath,
		"--bind-address", "127.0.0.1",
		"--v=2",
	}

	t.Logf("running: %s %s", binary, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, binary, args...)
	// Run the server in its own process group so stopping it terminates any
	// children as well.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logFile, err := os.Create(filepath.Join(artifactDir, "kcp-"+phase+".log"))
	require.NoError(t, err, "failed to create log file")
	t.Cleanup(func() { logFile.Close() })
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	require.NoError(t, cmd.Start(), "failed to start kcp (%s)", phase)

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	s := &kcpProcess{
		phase:          phase,
		kubeconfigPath: kubeconfigPath,
		cmd:            cmd,
		done:           done,
	}

	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Wait until this server instance has (re)written the kubeconfig, i.e.
	// until it names a host different from the previous phase's server.
	var restConfig *rest.Config
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		clientConfig, err := kcptestingserver.LoadKubeConfig(kubeconfigPath, "shard-base")
		if err != nil {
			return false, err.Error()
		}
		cfg, err := clientConfig.ClientConfig()
		if err != nil {
			return false, err.Error()
		}
		if cfg.Host == previousHost {
			return false, fmt.Sprintf("kubeconfig still points at the previous server %s", previousHost)
		}
		restConfig = cfg
		return true, ""
	}, 5*time.Minute, 250*time.Millisecond, "kcp (%s) never wrote its kubeconfig, see %s", phase, logFile.Name())

	require.NoError(t, kcptestingserver.WaitForReady(readyCtx, restConfig), "kcp (%s) never became ready, see %s", phase, logFile.Name())
	t.Logf("kcp (%s) is ready at %s", phase, restConfig.Host)
	s.host = restConfig.Host

	return s
}

// baseConfig returns a rest.Config for the "base" context of the server's
// kubeconfig, with client-side throttling disabled.
func (k *kcpProcess) baseConfig(ctx context.Context, t *testing.T) *rest.Config {
	t.Helper()

	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	clientConfig, err := kcptestingserver.WaitLoadKubeConfig(loadCtx, k.kubeconfigPath, "base")
	require.NoError(t, err, "failed to load kubeconfig for kcp (%s)", k.phase)
	restConfig, err := clientConfig.ClientConfig()
	require.NoError(t, err)
	restConfig.QPS = -1
	return restConfig
}

// stop terminates the server with SIGTERM and waits for it to exit, so that
// the etcd data directory is released cleanly before a next phase reuses it.
func (k *kcpProcess) stop(t *testing.T) {
	t.Helper()

	k.stopOnce.Do(func() {
		if k.cmd.Process == nil {
			return
		}
		// Signal the whole process group, mirroring the SDK test fixture.
		if err := syscall.Kill(-k.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Logf("failed to send SIGTERM to kcp (%s): %v", k.phase, err)
		}
		select {
		case <-k.done:
			t.Logf("kcp (%s) shut down", k.phase)
		case <-time.After(2 * time.Minute):
			_ = syscall.Kill(-k.cmd.Process.Pid, syscall.SIGKILL)
			<-k.done
			t.Fatalf("kcp (%s) did not shut down within timeout, killed", k.phase)
		}
	})
}

func createWorkspace(ctx context.Context, t *testing.T, client kcpclientset.ClusterInterface, parent logicalcluster.Path, name string) {
	t.Helper()

	t.Logf("Creating workspace %s|%s", parent, name)
	_, err := client.Cluster(parent).TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace %s|%s", parent, name)
	waitWorkspaceReady(ctx, t, client, parent, name)
}

// createSheriff creates a Sheriff instance in the default namespace of the
// given logical cluster. Bound CRDs are served asynchronously, so creation is
// retried until the resource is available.
func createSheriff(ctx context.Context, t *testing.T, client kcpdynamic.ClusterInterface, path logicalcluster.Path, gvr schema.GroupVersionResource, name string) {
	t.Helper()

	t.Logf("Creating %s %s|default/%s", gvr, path, name)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := client.Cluster(path).Resource(gvr).Namespace("default").Create(ctx, &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": gvr.Group + "/" + gvr.Version,
				"kind":       "Sheriff",
				"metadata":   map[string]any{"name": name},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, seedTimeout, 500*time.Millisecond, "failed to create sheriff %s|default/%s", path, name)
}

func waitWorkspaceReady(ctx context.Context, t *testing.T, client kcpclientset.ClusterInterface, parent logicalcluster.Path, name string) {
	t.Helper()

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		ws, err := client.Cluster(parent).TenancyV1alpha1().Workspaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady, fmt.Sprintf("phase: %s", ws.Status.Phase)
	}, seedTimeout, 500*time.Millisecond, "workspace %s|%s never became ready", parent, name)
}

// runE2E runs the given e2e test packages against the upgraded server via the
// shared external server mode of the e2e framework.
func runE2E(ctx context.Context, t *testing.T, packages []string, kubeconfigPath string) {
	t.Helper()

	repoDir, err := kcptestinghelpers.RepositoryDir()
	require.NoError(t, err, "failed to determine repository dir")

	args := append([]string{"test"}, packages...)
	args = append(args, "-args", "--kcp-kubeconfig", kubeconfigPath)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	t.Logf("running: go %s", strings.Join(args, " "))
	require.NoError(t, cmd.Run(), "e2e tests failed against the upgraded server")
}
