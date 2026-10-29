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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// selfManagementPhases are the phases in which the only operation left on a logical cluster is on
// the LogicalCluster object itself.
var selfManagementPhases = map[corev1alpha1.LogicalClusterPhaseType]bool{
	// an inactive cluster is reactivated by clearing the inactive annotation on it, and its
	// discovery keeps being served
	corev1alpha1.LogicalClusterPhaseInactive: true,
	// a deleting cluster only finalizes: whoever added a finalizer has to be able to remove it,
	// or the cluster can never go away
	corev1alpha1.LogicalClusterPhaseDeleting: true,
}

// NewLogicalClusterSelfManagementBypassAuthorizer admits requests against the LogicalCluster
// object of a cluster that is inactive or deleting, and discovery of an inactive one.
//
// The bootstrap policy bounds what is admitted, so this does not hand out access the operator has
// not granted through kcp's own policy.
func NewLogicalClusterSelfManagementBypassAuthorizer(getLogicalCluster LogicalClusterGetter, bootstrapPolicy authorizer.Authorizer) authorizer.Authorizer {
	return &logicalClusterSelfManagementBypassAuthorizer{
		getLogicalCluster: getLogicalCluster,
		bootstrapPolicy:   bootstrapPolicy,
	}
}

type logicalClusterSelfManagementBypassAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
	bootstrapPolicy   authorizer.Authorizer
}

func (a *logicalClusterSelfManagementBypassAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR reviews another identity, the bypass must not admit the reviewed request
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
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

	if !selfManagementPhases[logicalCluster.Status.Phase] {
		return authorizer.DecisionNoOpinion, "logical cluster is not inactive or deleting", nil
	}

	clusterObject := isLogicalClusterSelf(attr)
	discovery := logicalCluster.Status.Phase == corev1alpha1.LogicalClusterPhaseInactive && isOpenAPIRequest(attr)
	if !clusterObject && !discovery {
		return authorizer.DecisionNoOpinion, "not the LogicalCluster object or discovery", nil
	}

	allowed, reason, err := bootstrapPolicyAllows(ctx, a.bootstrapPolicy, attr)
	if err != nil {
		return authorizer.DecisionNoOpinion, "error checking bootstrap policy", err
	}
	if !allowed {
		return authorizer.DecisionNoOpinion, "not covered by the bootstrap policy", nil
	}

	return authorizer.DecisionAllow, fmt.Sprintf("logical cluster %q self-management: %s", logicalCluster.Status.Phase, reason), nil
}

// isLogicalClusterSelf returns true for requests against the LogicalCluster object of the
// requested cluster.
func isLogicalClusterSelf(attr authorizer.Attributes) bool {
	return attr.GetAPIGroup() == corev1alpha1.SchemeGroupVersion.Group &&
		attr.GetResource() == "logicalclusters" &&
		attr.GetName() == corev1alpha1.LogicalClusterName &&
		attr.GetSubresource() == ""
}

// isOpenAPIRequest returns true for the discovery endpoints, which are served per cluster and
// expose no workspace content.
func isOpenAPIRequest(attr authorizer.Attributes) bool {
	return !attr.IsResourceRequest() && strings.HasPrefix(attr.GetPath(), "/openapi")
}
