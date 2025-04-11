package workspace

import (
	"testing"
	"time"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/test/integration/framework"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestWorkspaceDeletionLeak(t *testing.T) {
	kcpClient, _, cancel := framework.StartTestServer(t)

	curGoroutines := goleak.IgnoreCurrent()

	t.Logf("Create a workspace with a shard")
	workspace, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Create(t.Context(), &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-deletion-leak"},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: tenancyv1alpha1.WorkspaceTypeReference{
				Name: "universal",
				Path: "root",
			},
			Location: &tenancyv1alpha1.WorkspaceLocation{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": corev1alpha1.RootShard,
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace")

	t.Logf("Wait until the %q workspace is ready", workspace.Name)
	require.Eventually(t, func() bool {
		workspace, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(t.Context(), workspace.Name, metav1.GetOptions{})
		require.NoError(t, err, "failed to get workspace")
		if actual, expected := workspace.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
			return false
		}
		return workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
	}, 1*time.Minute, 100*time.Millisecond)

	err = kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Delete(t.Context(), workspace.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

	t.Logf("Ensure workspace is removed")
	require.Eventually(t, func() bool {
		_, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(t.Context(), workspace.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	cancel()

	framework.GoleakWithDefaults(t, curGoroutines)
}
