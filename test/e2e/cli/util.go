package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testCli struct {
	server         kcptestingserver.RunningServer
	kubeconfigPath string
}

func newTestCli(t kcptesting.TestingT) *testCli {
	t.Helper()

	tc := &testCli{}
	tc.server = kcptesting.SharedKcpServer(t)
	tc.kubeconfigPath = writeKubeconfig(t, tc.server)

	return tc
}

func (tc *testCli) runPlugin(t kcptesting.TestingT, plugin string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	return framework.RunKcpCliPlugin(t, tc.kubeconfigPath, plugin, args)
}

func writeKubeconfig(t kcptesting.TestingT, server kcptestingserver.RunningServer) string {
	t.Helper()

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	tmpdir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpdir, "kubeconfig.yaml")
	err = clientcmd.WriteToFile(rawConfig, kubeconfigPath)
	require.NoError(t, err)

	return kubeconfigPath
}

func (tc *testCli) workspaceShouldExist(t kcptesting.TestingT, wsName string) error {
	t.Helper()

	// TODO replace with t.Context() in go1.24
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clientset, err := kcpclientset.NewForConfig(tc.server.BaseConfig(t))
	if err != nil {
		return err
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := clientset.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %q not found", wsName)
	return nil
}
