package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCreateWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	// TODO: replace with t.Context in go1.24
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	kubeconfigPath := writeKubeconfig(t, server)

	wsName := "test-create-workspace"

	_, _, err := framework.RunKcpCliPlugin(t, "create-workspace", kubeconfigPath, []string{wsName})
	require.NoError(t, err)

	clientset, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := clientset.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %q not found", wsName)

	_, stderr, err := framework.RunKcpCliPlugin(t, "create-workspace", kubeconfigPath, []string{wsName})
	require.Error(t, err)
	require.Contains(t, stderr.String(), "already exists")

	_, _, err = framework.RunKcpCliPlugin(t, "create-workspace", kubeconfigPath, []string{wsName, "--ignore-existing"})
	require.NoError(t, err)
}

func TestCreateWorkspaceEnter(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	// TODO: replace with t.Context in go1.24
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	kubeconfigPath := writeKubeconfig(t, server)

	wsName := "test-create-workspace-enter"

	_, _, err := framework.RunKcpCliPlugin(t, "create-workspace", kubeconfigPath, []string{wsName, "--enter"})
	require.NoError(t, err)

	clientset, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := clientset.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %q not found", wsName)

	stdout, _, err := framework.RunKcpCliPlugin(t, "ws", kubeconfigPath, []string{".", "--short"})
	require.NoError(t, err)
	require.Equal(t, "root:"+wsName+"\n", stdout.String())
}
