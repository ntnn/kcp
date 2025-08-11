package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/client-go/tools/clientcmd"

	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
)

func writeKubeconfig(t *testing.T, server kcptestingserver.RunningServer) string {
	t.Helper()

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	tmpdir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpdir, "kubeconfig.yaml")
	err = clientcmd.WriteToFile(rawConfig, kubeconfigPath)
	require.NoError(t, err)

	return kubeconfigPath
}
