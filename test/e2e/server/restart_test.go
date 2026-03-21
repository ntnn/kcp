/*
Copyright 2025 The kcp Authors.

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

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRestart(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// must use a private kcp server to restart it
	server := kcptesting.PrivateKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: metav1.NamespaceDefault,
		},
		Data: map[string]string{
			"foo": "bar",
		},
	}

	t.Log("Create ConfigMap")
	_, err = kubeClusterClient.Cluster(orgPath).CoreV1().ConfigMaps("default").Create(ctx, configMap, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap %s", configMapName)

	t.Log("Stopping server")
	server.Stop()

	t.Log("Waiting for server to stop")
	// TODO

	t.Log("Starting server again")
	err := server.Run(t)
	require.NoError(t, err, "error starting server again")

	cfg := server.BaseConfig(t)
	kubeClusterClient, err = kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	cm, err = kubeClusterClient.Cluster(orgPath).CoreV1().ConfigMaps("default").Get(ctx, configMap.Name, metav1.GetOptions{})
	require.NoError(t, err, "error creating configmap %s", configMapName)
	require.NotNil(t, cm, "expected a configmap")
}
