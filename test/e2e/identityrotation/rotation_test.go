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

package identityrotation

import (
	"context"
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/apis/v1alpha2/permissionclaims"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/identity"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

var cowboysGVR = wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys")

// rotationClients bundles the cluster-scoped clients the rotation tests use.
type rotationClients struct {
	kcp     kcpclientset.ClusterInterface
	kube    kcpkubernetesclientset.ClusterInterface
	dynamic kcpdynamic.ClusterInterface
}

func newRotationClients(t *testing.T, server kcptestingserver.RunningServer) rotationClients {
	t.Helper()
	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)
	return rotationClients{kcp: kcpClusterClient, kube: kubeClusterClient, dynamic: dynamicClusterClient}
}

// installCowboysExport installs the cowboys APIResourceSchema and APIExport in
// the provider workspace and returns the export's initial identity hash.
func installCowboysExport(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path) string {
	t.Helper()

	t.Logf("Install the cowboys APIResourceSchema and APIExport in %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.kcp.Cluster(providerPath).Discovery()))
	require.NoError(t, helpers.CreateResourceFromFS(ctx, c.dynamic.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))

	return createCowboysExport(ctx, t, c, providerPath, "today-cowboys", "today.cowboys.wildwest.dev")
}

// installCowboysSchema installs a copy of the embedded cowboys
// APIResourceSchema under the given name, so several exports can each own a
// schema (and thus a bound CRD) of their own.
func installCowboysSchema(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, schemaName string) {
	t.Helper()

	raw, err := testFiles.ReadFile("apiresourceschema_cowboys.yaml")
	require.NoError(t, err)
	schema := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal(raw, &schema.Object))
	schema.SetName(schemaName)
	_, err = c.dynamic.Cluster(providerPath).Resource(apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas")).Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err)
}

// createCowboysExport creates an APIExport serving cowboys from the given
// schema and returns its initial identity hash.
func createCowboysExport(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, exportName, schemaName string) string {
	t.Helper()

	_, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name:   "cowboys",
				Group:  "wildwest.dev",
				Schema: schemaName,
				Storage: apisv1alpha2.ResourceSchemaStorage{
					CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
				},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	var hash string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		hash = export.Status.IdentityHash
		return hash != "", "waiting for identity hash"
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	return hash
}

// bindCowboysAndCreate binds the cowboys export in the consumer workspace and
// creates a named cowboy, returning its UID.
func bindCowboysAndCreate(ctx context.Context, t *testing.T, c rotationClients, providerPath, consumerPath logicalcluster.Path, cowboy string) types.UID {
	t.Helper()
	return bindExportAndCreateCowboy(ctx, t, c, providerPath, consumerPath, "today-cowboys", cowboy)
}

// bindExportAndCreateCowboy binds the named export in the consumer workspace
// (as APIBinding "cowboys") and creates a named cowboy, returning its UID.
func bindExportAndCreateCowboy(ctx context.Context, t *testing.T, c rotationClients, providerPath, consumerPath logicalcluster.Path, exportName, cowboy string) types.UID {
	t.Helper()

	t.Logf("Bind export %q in %q and create cowboy %q", exportName, consumerPath, cowboy)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: exportName},
				},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	var uid types.UID
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": wildwestv1alpha1.SchemeGroupVersion.String(),
				"kind":       "Cowboy",
				"metadata":   map[string]interface{}{"name": cowboy},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			obj, err = c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, cowboy, metav1.GetOptions{})
		}
		if err != nil {
			return false, err.Error()
		}
		uid = obj.GetUID()
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	return uid
}

// bindMigrationAPI binds migration.kcp.io from root into the given workspace
// to obtain the rotation capability there.
func bindMigrationAPI(ctx context.Context, t *testing.T, c rotationClients, path logicalcluster.Path) {
	t.Helper()

	t.Logf("Bind the migration export in %q to obtain the rotation capability", path)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(path).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "migration.kcp.io"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: core.RootCluster.Path().String(), Name: "migration.kcp.io"},
				},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("binding migration.kcp.io: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	kcptesting.WaitForAPIReady(t, c.kcp.Cluster(path).Discovery(), migrationv1alpha1.SchemeGroupVersion)
}

// createRotationSecret pre-creates the new identity secret in the export's
// workspace: that is where kcp resolves export identities.
func createRotationSecret(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path) {
	t.Helper()
	createIdentitySecret(ctx, t, c, providerPath, "today-cowboys-rotated")
}

// createIdentitySecret pre-creates a new identity secret with the given name
// in the export's workspace. Every secret gets a distinct key fingerprint.
func createIdentitySecret(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, secretName string) {
	t.Helper()

	_, err := c.kube.Cluster(providerPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kcp-system"},
	}, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	_, err = c.kube.Cluster(providerPath).CoreV1().Secrets("kcp-system").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName},
		StringData: map[string]string{"key": "a-fresh-identity-key-for-" + providerPath.String() + "/" + secretName + "-0123456789abcdef"},
	}, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

// requestRotation creates the rotation object in rotationPath, referencing
// the export via exportRef and the new identity secret by name. It tolerates
// the bound CRD establishment race after binding migration.kcp.io.
func requestRotation(ctx context.Context, t *testing.T, c rotationClients, rotationPath logicalcluster.Path, exportRef migrationv1alpha1.ExportReference, name, secretName string, retirement migrationv1alpha1.AliasRetirement) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(rotationPath).MigrationV1alpha1().APIExportIdentityRotations().Create(ctx, &migrationv1alpha1.APIExportIdentityRotation{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
				Export: exportRef,
				NewIdentity: apisv1alpha2.Identity{
					SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: secretName},
				},
				AliasRetirement: retirement,
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating rotation: %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "creating the rotation request")
}

// waitForRotationPhase waits until the rotation reaches the given phase and
// returns the rotation's new key fingerprint, not the stable resource identity.
func waitForRotationPhase(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, name string, phase migrationv1alpha1.APIExportIdentityRotationPhase) string {
	t.Helper()
	return waitForRotationPhaseWithin(ctx, t, c, providerPath, name, phase, wait.ForeverTestTimeout)
}

// waitForRotationPhaseWithin is waitForRotationPhase with an explicit
// timeout, for scenarios rotating many exports.
func waitForRotationPhaseWithin(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, name string, phase migrationv1alpha1.APIExportIdentityRotationPhase, timeout time.Duration) string {
	t.Helper()
	var newHash string
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rotation, err := c.kcp.Cluster(providerPath).MigrationV1alpha1().APIExportIdentityRotations().Get(ctx, name, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, phase, rotation.Status.Phase,
			"rotation phase %s, migrated %d/%d", rotation.Status.Phase, rotation.Status.MigratedBindings, rotation.Status.TotalBindings)
		if phase == migrationv1alpha1.APIExportIdentityRotationAliasActive || phase == migrationv1alpha1.APIExportIdentityRotationCompleted {
			valid := conditions.Get(rotation, migrationv1alpha1.IdentityRotationValid)
			require.NotNil(collect, valid)
			require.Equal(collect, "StableKeyRotation", valid.Reason)
			require.Zero(collect, rotation.Status.TotalBindings, "key rotation must not require a storage drain")
			require.Zero(collect, rotation.Status.MigratedBindings)
		}
		newHash = rotation.Status.NewIdentityHash
	}, timeout, 250*time.Millisecond)
	return newHash
}

func currentKeyHash(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, exportName string) string {
	t.Helper()
	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, export.Spec.Identity)
	require.NotNil(t, export.Spec.Identity.SecretRef)
	ref := export.Spec.Identity.SecretRef
	secret, err := c.kube.Cluster(providerPath).CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	require.NoError(t, err)
	hash, err := identity.KeyHash(secret)
	require.NoError(t, err)
	return hash
}

func requireExportKeys(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, exportName, stableID, currentHash string, accepted []string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, stableID, export.Status.IdentityHash)
		require.ElementsMatch(collect, accepted, export.Status.IdentityKeyHashes)
		require.Empty(collect, export.Status.IdentityAliasHashes, "key rotation must not publish storage identity aliases")
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
	require.Equal(t, currentHash, currentKeyHash(ctx, t, c, providerPath, exportName))
}

// requireConsumerUnchanged checks stable binding identities, object UIDs, and no fence.
func requireConsumerUnchanged(ctx context.Context, t *testing.T, c rotationClients, consumerPath logicalcluster.Path, stableID, cowboy string, uid types.UID) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		binding, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEmpty(collect, binding.Status.BoundResources)
		for _, br := range binding.Status.BoundResources {
			require.Equal(collect, stableID, br.Schema.IdentityHash)
			require.Equal(collect, []string{stableID}, br.IdentityHashes)
		}
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "binding in %s must retain its stable identity", consumerPath)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, cowboy, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, uid, obj.GetUID(), "key rotation must preserve object UIDs")
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "cowboy %s in %s must survive key rotation", cowboy, consumerPath)
	requireUnfenced(ctx, t, c, consumerPath)
}

// TestAPIExportIdentityRotation exercises the full identity rotation flow of
// enhancements KEP 0005 with multiple consumers of the rotated export: bound
// resource identities, bindings, and object UIDs stay unchanged while the key
// fingerprint changes and the old key is retired. Two consumers use the root
// shard and a third uses another shard when available. The rotation is requested
// from a separate ops
// workspace via spec.export.path - the delegation model: only the platform's
// ops workspace binds migration.kcp.io, the provider workspace never sees the
// rotation capability.
func TestAPIExportIdentityRotation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())
	// pin the third consumer to a non-root shard when one exists, so sharded
	// environments deterministically exercise stable identities across shards.
	var nonRootShard string
	shards, err := c.kcp.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, shard := range shards.Items {
		if shard.Name != corev1alpha1.RootShard {
			nonRootShard = shard.Name
			break
		}
	}
	consumer3Opts := []kcptesting.UnprivilegedWorkspaceOption{kcptesting.WithName("consumer-3")}
	if nonRootShard != "" {
		t.Logf("Pinning consumer-3 to shard %q for cross-shard key rotation", nonRootShard)
		consumer3Opts = append(consumer3Opts, kcptesting.WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"name": nonRootShard},
		}}))
	}

	consumerPaths := make([]logicalcluster.Path, 0, 3)
	for _, opts := range [][]kcptesting.UnprivilegedWorkspaceOption{
		{kcptesting.WithName("consumer-1"), kcptesting.WithRootShard()},
		{kcptesting.WithName("consumer-2"), kcptesting.WithRootShard()},
		consumer3Opts,
	} {
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, opts...)
		consumerPaths = append(consumerPaths, path)
	}

	oldHash := installCowboysExport(ctx, t, c, providerPath)
	oldKeyHash := currentKeyHash(ctx, t, c, providerPath, "today-cowboys")

	cowboyUIDs := make([]types.UID, len(consumerPaths))
	for i, consumerPath := range consumerPaths {
		cowboyUIDs[i] = bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, fmt.Sprintf("woody-%d", i+1))
	}

	bindMigrationAPI(ctx, t, c, opsPath)
	createRotationSecret(ctx, t, c, providerPath)

	t.Logf("Request the rotation from the ops workspace %q, referencing the export by path (immediate alias retirement)", opsPath)
	requestRotation(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
		"rotate-today-cowboys", "today-cowboys-rotated", migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate})

	t.Logf("Wait for the rotation to complete")
	newHash := waitForRotationPhase(ctx, t, c, opsPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationCompleted)
	require.NotEmpty(t, newHash)
	require.NotEqual(t, oldKeyHash, newHash)

	t.Logf("The export retains its stable identity and accepts only the new key")
	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, oldHash, export.Status.IdentityHash)
	requireExportKeys(ctx, t, c, providerPath, "today-cowboys", oldHash, newHash, []string{newHash})

	t.Logf("Every consumer retains its identity and every cowboy survived without a drain")
	for i, consumerPath := range consumerPaths {
		requireConsumerUnchanged(ctx, t, c, consumerPath, oldHash, fmt.Sprintf("woody-%d", i+1), cowboyUIDs[i])
	}

	t.Logf("A second rotation within the cooldown is rejected by admission")
	_, err = c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportIdentityRotations().Create(ctx, &migrationv1alpha1.APIExportIdentityRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "rotate-again"},
		Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
			Export: migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
			NewIdentity: apisv1alpha2.Identity{
				SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: "today-cowboys-rotated"},
			},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err, "expected the rotation cooldown to reject an immediate second rotation")
}

// TestAPIExportIdentityRotationAliasWindow exercises cross-consumers of the
// stable identity: a second export's permission claim and labels remain valid
// both during the old key's overlap window and after its retirement.
func TestAPIExportIdentityRotationAliasWindow(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"), kcptesting.WithRootShard())
	providerClusterName := logicalcluster.Name(providerWS.Spec.Cluster)

	oldHash := installCowboysExport(ctx, t, c, providerPath)
	oldKeyHash := currentKeyHash(ctx, t, c, providerPath, "today-cowboys")
	cowboyUID := bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, "woody")

	// An unrelated export exercises shared wildcard storage without changing
	// its identity or serving state when the provider rotates a key.
	bystanderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("bystander"), kcptesting.WithRootShard())
	installCowboysSchema(ctx, t, c, bystanderPath, "bystander.cowboys.wildwest.dev")
	createCowboysExport(ctx, t, c, bystanderPath, "bystander-cowboys", "bystander.cowboys.wildwest.dev")
	bindExportAndCreateCowboy(ctx, t, c, bystanderPath, bystanderPath, "bystander-cowboys", "bystander")

	claim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"},
		Verbs:         []string{"*"},
		IdentityHash:  oldHash,
	}

	t.Logf("Install a claimer APIExport in %q claiming cowboys with the pre-rotation identity %s", providerPath, oldHash)
	_, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-claimer"},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{claim},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind the claimer export in %q and accept the claim", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "claimer"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "today-claimer"},
				},
				PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: claim,
						Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				}},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	requireCowboyClaimLabel := func(hash, msg string) {
		t.Helper()
		expectedClaim := claim
		expectedClaim.IdentityHash = hash
		key, value, err := permissionclaims.ToLabelKeyAndValue(providerClusterName, "today-claimer", expectedClaim)
		require.NoError(t, err)
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, "woody", metav1.GetOptions{})
			require.NoError(collect, err)
			require.Equal(collect, value, obj.GetLabels()[key], "cowboy labels: %v", obj.GetLabels())
		}, wait.ForeverTestTimeout, 250*time.Millisecond, msg)
	}

	t.Logf("Before the rotation the cowboy carries the claim label derived from the old identity")
	requireCowboyClaimLabel(oldHash, "claim label for the pre-rotation identity")

	bindMigrationAPI(ctx, t, c, providerPath)
	createRotationSecret(ctx, t, c, providerPath)

	t.Logf("Request the rotation with Manual retirement to hold the alias window open")
	requestRotation(ctx, t, c, providerPath,
		migrationv1alpha1.ExportReference{Name: "today-cowboys"}, // empty path: same-workspace default
		"rotate-today-cowboys", "today-cowboys-rotated", migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementManual})

	t.Logf("Wait for the accepted old/new key overlap window")
	newHash := waitForRotationPhase(ctx, t, c, providerPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationAliasActive)
	require.NotEmpty(t, newHash)
	require.NotEqual(t, oldKeyHash, newHash)

	t.Logf("Both key fingerprints are accepted while the stable identity is unchanged")
	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, oldHash, export.Status.IdentityHash)
	requireExportKeys(ctx, t, c, providerPath, "today-cowboys", oldHash, newHash, []string{oldKeyHash, newHash})

	t.Logf("During the overlap window the claim label retains the stable identity")
	requireCowboyClaimLabel(oldHash, "claim label must retain the stable identity during key rotation")

	t.Logf("The cowboy is unchanged")
	obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, "woody", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, cowboyUID, obj.GetUID())

	t.Logf("Retire the alias by moving the policy to Immediate")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		rotation, err := c.kcp.Cluster(providerPath).MigrationV1alpha1().APIExportIdentityRotations().Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		rotation.Spec.AliasRetirement = migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate}
		_, err = c.kcp.Cluster(providerPath).MigrationV1alpha1().APIExportIdentityRotations().Update(ctx, rotation, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("moving retirement to Immediate: %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)

	waitForRotationPhase(ctx, t, c, providerPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationCompleted)

	t.Logf("The old key is retired and the stable identity claim still resolves")
	export, err = c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, oldHash, export.Status.IdentityHash)
	requireExportKeys(ctx, t, c, providerPath, "today-cowboys", oldHash, newHash, []string{newHash})
	requireCowboyClaimLabel(oldHash, "retiring an old key must not invalidate a stable identity claim")
	requireConsumerUnchanged(ctx, t, c, consumerPath, oldHash, "woody", cowboyUID)
}

// TestAPIExportIdentityRotationConcurrent rotates several exports at the same
// time, each bound by many consumers spread across all shards. It exercises
// independent key changes without moving storage or changing consumer identities.
func TestAPIExportIdentityRotationConcurrent(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const (
		exportCount   = 3
		consumerCount = 30
	)
	exportName := func(k int) string { return fmt.Sprintf("today-cowboys-%d", k+1) }
	schemaName := func(k int) string { return fmt.Sprintf("today-%d.cowboys.wildwest.dev", k+1) }
	secretName := func(k int) string { return fmt.Sprintf("today-cowboys-%d-rotated", k+1) }
	rotationName := func(k int) string { return fmt.Sprintf("rotate-today-cowboys-%d", k+1) }

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())

	shards, err := c.kcp.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	shardNames := make([]string, 0, len(shards.Items))
	for _, shard := range shards.Items {
		shardNames = append(shardNames, shard.Name)
	}

	// consumer i binds export i%exportCount and lives on shard
	// (i/exportCount)%shards, so every export's consumers are spread over
	// every shard (a plain i%shards would correlate with the export choice
	// and put each export's consumers on a single shard).
	shardFor := func(i int) string { return shardNames[(i/exportCount)%len(shardNames)] }
	t.Logf("Create %d consumer workspaces across shards %v", consumerCount, shardNames)
	consumerPaths := make([]logicalcluster.Path, 0, consumerCount)
	for i := range consumerCount {
		opts := []kcptesting.UnprivilegedWorkspaceOption{kcptesting.WithName("consumer-%d", i+1)}
		if len(shardNames) > 1 {
			opts = append(opts, kcptesting.WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"name": shardFor(i)},
			}}))
		}
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, opts...)
		consumerPaths = append(consumerPaths, path)
	}

	// Each export owns its own schema and stable storage identity.
	oldHashes := make([]string, exportCount)
	oldKeyHashes := make([]string, exportCount)
	for k := range exportCount {
		installCowboysSchema(ctx, t, c, providerPath, schemaName(k))
		oldHashes[k] = createCowboysExport(ctx, t, c, providerPath, exportName(k), schemaName(k))
		oldKeyHashes[k] = currentKeyHash(ctx, t, c, providerPath, exportName(k))
	}

	cowboyUIDs := make([]types.UID, consumerCount)
	for i, consumerPath := range consumerPaths {
		cowboyUIDs[i] = bindExportAndCreateCowboy(ctx, t, c, providerPath, consumerPath, exportName(i%exportCount), fmt.Sprintf("woody-%d", i+1))
	}

	bindMigrationAPI(ctx, t, c, opsPath)
	for k := range exportCount {
		createIdentitySecret(ctx, t, c, providerPath, secretName(k))
	}

	t.Logf("Request %d rotations back to back", exportCount)
	for k := range exportCount {
		requestRotation(ctx, t, c, opsPath,
			migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: exportName(k)},
			rotationName(k), secretName(k), migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate})
	}

	newHashes := make([]string, exportCount)
	for k := range exportCount {
		t.Logf("Wait for rotation %q to complete", rotationName(k))
		newHashes[k] = waitForRotationPhaseWithin(ctx, t, c, opsPath, rotationName(k), migrationv1alpha1.APIExportIdentityRotationCompleted, 5*time.Minute)
		require.NotEqual(t, oldKeyHashes[k], newHashes[k])
		requireExportKeys(ctx, t, c, providerPath, exportName(k), oldHashes[k], newHashes[k], []string{newHashes[k]})
	}

	require.Len(t, sets.New(newHashes...), exportCount, "each rotation must accept its own new key")

	t.Logf("Every consumer retains its export's stable identity and every cowboy survived")
	for i, consumerPath := range consumerPaths {
		requireConsumerUnchanged(ctx, t, c, consumerPath, oldHashes[i%exportCount], fmt.Sprintf("woody-%d", i+1), cowboyUIDs[i])
	}
}
