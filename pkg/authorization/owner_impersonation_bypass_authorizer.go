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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// ownerImpersonationPhases are the phases whose virtual workspaces proxy content as the logical cluster's creator.
var ownerImpersonationPhases = sets.New(
	corev1alpha1.LogicalClusterPhaseInitializing,
	corev1alpha1.LogicalClusterPhaseTerminating,
	corev1alpha1.LogicalClusterPhaseDeleting,
)

// NewOwnerImpersonationBypassAuthorizer admits requests a lifecycle virtual workspace proxied as
// the creator recorded in LogicalCluster.Spec.CreatedBy.
//
// Both halves must fit: the synthetic group can only be added by the virtual workspace proxies,
// and the identity must be the recorded creator. Neither alone is enough.
func NewOwnerImpersonationBypassAuthorizer(getLogicalCluster LogicalClusterGetter) authorizer.Authorizer {
	return &ownerImpersonationBypassAuthorizer{
		getLogicalCluster: getLogicalCluster,
	}
}

type ownerImpersonationBypassAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
}

func (a *ownerImpersonationBypassAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR reviews another identity, the proxy's group must not admit the reviewed request
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	if !rbacregistryvalidation.EffectiveGroups(ctx, attr.GetUser()).Has(bootstrap.SystemKcpOwnerImpersonationGroup) {
		return authorizer.DecisionNoOpinion, "no owner impersonation group", nil
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

	if !ownerImpersonationPhases.Has(logicalCluster.Status.Phase) {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("phase %q is not served by a lifecycle virtual workspace", logicalCluster.Status.Phase), nil
	}

	createdBy := logicalCluster.Spec.CreatedBy
	if createdBy == nil ||
		attr.GetUser() == nil ||
		createdBy.Username != attr.GetUser().GetName() ||
		createdBy.UID != attr.GetUser().GetUID() {
		return authorizer.DecisionNoOpinion, "identity is not the recorded creator", nil
	}

	return authorizer.DecisionAllow, "owner impersonation fallback", nil
}
