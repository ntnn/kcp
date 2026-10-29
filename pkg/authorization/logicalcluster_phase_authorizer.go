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

// NewLogicalClusterPhaseAuthorizer returns an authorizer that denies
// access to the content of logical clusters if they are not in the
// ready phase.
func NewLogicalClusterPhaseAuthorizer(getLogicalCluster LogicalClusterGetter) func(delegate authorizer.Authorizer) authorizer.Authorizer {
	return func(delegate authorizer.Authorizer) authorizer.Authorizer {
		return &logicalClusterPhaseAuthorizer{
			getLogicalCluster: getLogicalCluster,
			delegate:          delegate,
		}
	}
}

type logicalClusterPhaseAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
	delegate          authorizer.Authorizer
}

func (a *logicalClusterPhaseAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		return DelegateAuthorization("deep SAR request", a.delegate).Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	// empty or system workspaces have no meaning in the context of authorizing workspace content.
	// To access system workspaces, the user must be privileged such that authorization is skipped completely.
	if cluster == nil || cluster.Name.Empty() || strings.HasPrefix(cluster.Name.String(), "system:") {
		return DelegateAuthorization("empty or system workspace", a.delegate).Authorize(ctx, attr)
	}

	logicalCluster, err := a.getLogicalCluster(cluster.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			// the existence pre-gate denies this; here the phase is simply unknown
			return DelegateAuthorization("LogicalCluster not found", a.delegate).Authorize(ctx, attr)
		}
		return authorizer.DecisionNoOpinion, "error getting LogicalCluster", err
	}

	// Only a Ready logical cluster serves content through the regular chain.
	// Everything else that operates on a cluster in another phase does so through a bypass authorizer.
	if logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		return authorizer.DecisionDeny, fmt.Sprintf("not permitted due to phase %q", logicalCluster.Status.Phase), nil
	}

	return DelegateAuthorization("phase \"Ready\" permits access", a.delegate).Authorize(ctx, attr)
}
