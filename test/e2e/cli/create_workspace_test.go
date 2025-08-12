package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCreateWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	tc := newTestCli(t)
	wsName := "test-create-workspace"

	_, _, err := tc.runPlugin(t, "create-workspace", wsName)
	require.NoError(t, err)
	tc.workspaceShouldExist(t, wsName)

	_, stderr, err := tc.runPlugin(t, "create-workspace", wsName)
	require.Error(t, err)
	require.Contains(t, stderr.String(), "already exists")

	_, _, err = tc.runPlugin(t, "create-workspace", wsName, "--ignore-existing")
	require.NoError(t, err)
}

func TestCreateWorkspaceEnter(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	tc := newTestCli(t)
	wsName := "test-create-workspace-enter"

	_, _, err := tc.runPlugin(t, "create-workspace", wsName, "--enter")
	require.NoError(t, err)
	tc.workspaceShouldExist(t, wsName)

	stdout, _, err := tc.runPlugin(t, "ws", ".", "--short")
	require.NoError(t, err)
	require.Equal(t, "root:"+wsName+"\n", stdout.String())
}
