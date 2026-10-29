/*
Copyright 2026 The kcp Authors.

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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// NewWorkspaceBootstrapperBypassAuthorizer admits kcp's workspace bootstrapper while it builds a logical cluster.
func NewWorkspaceBootstrapperBypassAuthorizer(getLogicalCluster LogicalClusterGetter) authorizer.Authorizer {
	return &workspaceBootstrapperBypassAuthorizer{
		getLogicalCluster: getLogicalCluster,
	}
}

type workspaceBootstrapperBypassAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
}

func (a *workspaceBootstrapperBypassAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR reviews another identity, the bootstrapper's group must not admit the reviewed request
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	if !rbacregistryvalidation.EffectiveGroups(ctx, attr.GetUser()).Has(bootstrap.SystemKcpWorkspaceBootstrapper) {
		return authorizer.DecisionNoOpinion, "not the workspace bootstrapper", nil
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	logicalCluster, err := a.getLogicalCluster(cluster.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionNoOpinion, "LogicalCluster not found", nil
		}
		return authorizer.DecisionNoOpinion, "error getting LogicalCluster", err
	}

	if logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing {
		return authorizer.DecisionNoOpinion, "only initializing logical clusters are bootstrapped", nil
	}

	return authorizer.DecisionAllow, "workspace bootstrapping", nil
}
