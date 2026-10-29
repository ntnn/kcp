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

package authorization

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	rbacv1listers "github.com/kcp-dev/client-go/listers/rbac/v1"
	rbacwrapper "github.com/kcp-dev/virtual-workspace-framework/pkg/wrappers/rbac"

	"github.com/kcp-dev/kcp/pkg/authorization/shardpaths"
)

const (
	WorkspaceAccessNotPermittedReason = "workspace access not permitted"
)

func NewWorkspaceContentAuthorizer(localInformers, globalInformers kcpkubernetesinformers.SharedInformerFactory) func(delegate authorizer.Authorizer) authorizer.Authorizer {
	return func(delegate authorizer.Authorizer) authorizer.Authorizer {
		return &workspaceContentAuthorizer{
			localClusterRoleLister:        localInformers.Rbac().V1().ClusterRoles().Lister(),
			localClusterRoleBindingLister: localInformers.Rbac().V1().ClusterRoleBindings().Lister(),

			globalClusterRoleLister:        globalInformers.Rbac().V1().ClusterRoles().Lister(),
			globalClusterRoleBindingLister: globalInformers.Rbac().V1().ClusterRoleBindings().Lister(),

			delegate: delegate,
		}
	}
}

type workspaceContentAuthorizer struct {
	localClusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	localClusterRoleLister        rbacv1listers.ClusterRoleClusterLister

	globalClusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	globalClusterRoleLister        rbacv1listers.ClusterRoleClusterLister

	delegate authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	cluster := genericapirequest.ClusterFrom(ctx)

	// Shard-level non-resource URLs (e.g. /metrics) carry no workspace semantics.
	// WithShardLevelPaths has either rejected the request already or scoped it to
	// :root. Skip the verb=access gate so a minimally-privileged identity bound
	// only to a NonResourceURLs rule (e.g. system:kcp:metrics-reader) can scrape
	// without also being granted broader workspace access.
	if !attr.IsResourceRequest() && shardpaths.Paths.Has(attr.GetPath()) {
		return DelegateAuthorization("shard-level path", a.delegate).Authorize(ctx, attr)
	}

	// empty or system workspaces have no meaning in the context of authorizing workspace content.
	// To access system workspaces, the user must be privileged such that authorization is skipped completely.
	if cluster == nil || cluster.Name.Empty() || strings.HasPrefix(cluster.Name.String(), "system:") {
		return authorizer.DecisionNoOpinion, "empty or system workspace", nil
	}

	isServiceAccount := rbacregistryvalidation.IsServiceAccount(attr.GetUser())
	isInScope := rbacregistryvalidation.IsInScope(attr.GetUser(), cluster.Name)

	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		return DelegateAuthorization("deep SAR request", a.delegate).Authorize(ctx, attr)
	}

	switch {
	case isServiceAccount && isInScope:
		// A service account declared in the requested workspace is always authorized inside that workspace.
		return DelegateAuthorization("local service account access", a.delegate).Authorize(ctx, attr)

	default:
		authz := rbac.New(
			&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister()},
			&rbac.RoleBindingLister{Lister: rbacwrapper.NewMergedRoleBindingLister()},
			&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
				a.localClusterRoleLister.Cluster(cluster.Name),
				a.globalClusterRoleLister.Cluster(cluster.Name),
				a.localClusterRoleLister.Cluster(controlplaneapiserver.LocalAdminCluster),
			)},
			&rbac.ClusterRoleBindingLister{Lister: rbacwrapper.NewMergedClusterRoleBindingLister(
				a.localClusterRoleBindingLister.Cluster(cluster.Name),
				a.globalClusterRoleBindingLister.Cluster(cluster.Name),
				a.localClusterRoleBindingLister.Cluster(controlplaneapiserver.LocalAdminCluster),
			)},
		)

		workspaceAttr := authorizer.AttributesRecord{
			User:            attr.GetUser(),
			Verb:            "access",
			Path:            "/",
			ResourceRequest: false,
		}

		dec, _, err := authz.Authorize(ctx, workspaceAttr)
		if err != nil {
			return authorizer.DecisionNoOpinion, fmt.Sprintf("errors from workspace content authorizer: %v", err), err
		}
		if dec != authorizer.DecisionAllow {
			return dec, "no verb=access permission on /", nil
		}
		return DelegateAuthorization("user logical cluster access", a.delegate).Authorize(ctx, attr)
	}
}
