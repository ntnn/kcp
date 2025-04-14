package framework

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/sdk/apis/core"
)

func TestServer(t *testing.T) {
	StartTestServer(t)
}

func TestServerCreateConfigMap(t *testing.T) {
	_, kubeClient, _ := StartTestServer(t)

	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: metav1.NamespaceDefault,
		},
		Data: map[string]string{
			"foo": "bar",
		},
	}

	_, err := kubeClient.Cluster(core.RootCluster.Path()).
		CoreV1().
		ConfigMaps(metav1.NamespaceDefault).
		Create(t.Context(), configmap, metav1.CreateOptions{})
	require.Nil(t, err)
}
