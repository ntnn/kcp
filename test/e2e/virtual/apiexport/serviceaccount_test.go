/*
Copyright 2022 The kcp Authors.

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

package apiexport

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMintServiceAccountTokenThroughVW(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	// dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	// require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	const providerSAClaimLabel = "custom.provider/label"

	t.Log("Setup ServiceAccount in consumer with cluster-admin")
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "serviceaccount",
			Namespace: "default",
			Labels: map[string]string{
				providerSAClaimLabel: "true",
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().ServiceAccounts("default").Create(t.Context(), sa, metav1.CreateOptions{})
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: sa.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "cluster-admin",
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create APIExport in provider with claims for ServiceAccount")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sa-token",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Resource: "serviceaccounts",
					},
					Verbs: []string{"get", "list"},
					Subresources: []apisv1alpha2.SubresourceClaim{
						{
							Name:  "token",
							Verbs: []string{"create"},
						},
					},
					DefaultSelector: &apisv1alpha2.PermissionClaimSelector{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								providerSAClaimLabel: "true",
							},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer")
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Resource: "serviceaccounts",
							},
							Verbs: []string{"get", "list"},
							Subresources: []apisv1alpha2.SubresourceClaim{
								{
									Name:  "token",
									Verbs: []string{"create"},
								},
							},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{providerSAClaimLabel: "true"},
							},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for VW URL in APIExportES")
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	vwClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Log("Verify the ServiceAccount is visible through the VW")
	vwServiceAccounts, err := vwClient.CoreV1().ServiceAccounts().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, vwServiceAccounts.Items, 1, "expect listing exactly one ServiceAccount through the VW")
	require.Equal(t, vwServiceAccounts.Items[0].Name, sa.Name, "expect the ServieAccount to have the name %q", sa.Name)

	t.Log("Mint a token for the ServiceAccount")
	tokenRequest := &authenticationv1.TokenRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sa.Name,
			Namespace: sa.Namespace,
		},
		Spec: authenticationv1.TokenRequestSpec{},
	}
	_, err = vwClient.CoreV1().ServiceAccounts().Cluster(consumerClusterName.Path()).Namespace(sa.Namespace).CreateToken(t.Context(), sa.Name, tokenRequest, metav1.CreateOptions{})
	require.NoError(t, err)
}
