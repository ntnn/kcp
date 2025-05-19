package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/integration/framework"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func createAndDelteWs(
	t *testing.T,
	name string,
	kcpClient kcpclientset.ClusterInterface,
) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Logf("Create a workspace with a shard")
	workspace, err := kcpClient.TenancyV1alpha1().Workspaces().Cluster(core.RootCluster.Path()).Create(
		ctx,
		&tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create workspace")

	t.Logf("Wait until the %q workspace is ready", workspace.Name)
	require.Eventually(
		t,
		func() bool {
			workspace, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(
				ctx,
				workspace.Name,
				metav1.GetOptions{},
			)
			require.NoError(t, err, "failed to get workspace")
			t.Logf("workspace status phase: %v", workspace.Status.Phase)
			return workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
		},
		1*time.Minute,
		100*time.Millisecond,
	)

	err = kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Delete(
		ctx,
		workspace.Name,
		metav1.DeleteOptions{},
	)
	require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

	t.Logf("Ensure workspace is removed")
	require.Eventually(t, func() bool {
		_, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(
			ctx,
			workspace.Name,
			metav1.GetOptions{},
		)
		return apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func TestWorkspaceDeletionLeak(t *testing.T) {
	kcpClient, _, _ := framework.StartTestServer(t)

	// create and delete a workspace to warmup any informers and caches
	createAndDelteWs(t, "warmup-workspace", kcpClient)

	curGoroutines := goleak.IgnoreCurrent()

	// create and delete workspace, wait a couple seconds for goroutines
	// to finish and then check for any goroutine leaks
	createAndDelteWs(t, "workspace-leaktest", kcpClient)

	framework.GoleakWithDefaults(t, curGoroutines)
}
