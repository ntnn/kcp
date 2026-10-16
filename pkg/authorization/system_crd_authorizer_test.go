/*
Copyright 2026 The KCP Authors.

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

package authorization

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestSystemCRDAuthorizerIdentityStatus(t *testing.T) {
	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: logicalcluster.Name("workspace")})
	for _, resource := range []struct {
		group, version, resource string
	}{
		{"apis.kcp.io", "v1alpha1", "apiexports"},
		{"apis.kcp.io", "v1alpha2", "apiexports"},
		{"apis.kcp.io", "v1alpha1", "apibindings"},
		{"apis.kcp.io", "v1alpha2", "apibindings"},
		{"cache.kcp.io", "v1alpha1", "cachedresources"},
	} {
		for _, verb := range []string{"get", "update", "patch"} {
			t.Run(resource.resource+"/"+resource.version+"/"+verb, func(t *testing.T) {
				called := false
				delegate := authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
					called = true
					return authorizer.DecisionAllow, "workspace admin", nil
				})
				guard := NewSystemCRDAuthorizer(delegate)
				attr := authorizer.AttributesRecord{
					User: &user.DefaultInfo{
						Name:   "tenant-admin",
						Groups: []string{user.AllAuthenticated, "system:kcp:admin"},
					},
					Verb: verb, APIGroup: resource.group, APIVersion: resource.version,
					Resource: resource.resource, Subresource: "status", ResourceRequest: true,
				}
				decision, reason, err := guard.Authorize(ctx, attr)
				require.NoError(t, err)
				require.Equal(t, authorizer.DecisionDeny, decision)
				require.Contains(t, reason, "status updates not permitted")
				require.False(t, called, "tenant RBAC must not override protected status")

				// Match the default server chain: privileged groups are checked
				// before the RBAC chain containing the system CRD guard.
				chain := union.New(authorizerfactory.NewPrivilegedGroups(user.SystemPrivilegedGroup), guard)
				attr.User = &user.DefaultInfo{Name: user.APIServerUser, Groups: []string{user.AllAuthenticated, user.SystemPrivilegedGroup}}
				decision, _, err = chain.Authorize(ctx, attr)
				require.NoError(t, err)
				require.Equal(t, authorizer.DecisionAllow, decision)
				require.False(t, called)

				attr.User = &user.DefaultInfo{Name: "system:serviceaccount:default:controller", Groups: []string{user.AllAuthenticated, "system:serviceaccounts"}}
				decision, _, err = chain.Authorize(ctx, attr)
				require.NoError(t, err)
				require.Equal(t, authorizer.DecisionDeny, decision)
				require.False(t, called)

				attr.Subresource = ""
				decision, _, err = guard.Authorize(ctx, attr)
				require.NoError(t, err)
				require.Equal(t, authorizer.DecisionAllow, decision)
				require.True(t, called, "main-resource access should remain governed by RBAC")
			})
		}
	}
}

func TestSystemCRDAuthorizerDelegation(t *testing.T) {
	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: logicalcluster.Name("workspace")})
	for _, resource := range []struct {
		group, resource string
	}{
		{"cache.kcp.io", "cachedresourceendpointslices"},
		{"apis.kcp.io", "apiexportendpointslices"},
		{"example.org", "cachedresources"},
		{"example.org", "apiexports"},
	} {
		t.Run(resource.group+"/"+resource.resource, func(t *testing.T) {
			called := false
			guard := NewSystemCRDAuthorizer(authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
				called = true
				return authorizer.DecisionAllow, "allowed", nil
			}))
			attr := authorizer.AttributesRecord{APIGroup: resource.group, Resource: resource.resource, Subresource: "status", Verb: "update", ResourceRequest: true}
			decision, _, err := guard.Authorize(ctx, attr)
			require.NoError(t, err)
			require.Equal(t, authorizer.DecisionAllow, decision)
			require.True(t, called)
		})
	}
}
