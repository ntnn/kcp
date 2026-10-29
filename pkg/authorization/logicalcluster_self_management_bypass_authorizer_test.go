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
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestLogicalClusterSelfManagementBypassAuthorizer(t *testing.T) {
	t.Parallel()

	const policyReason = "bootstrap policy: allowed"

	cases := map[string]struct {
		phase        corev1alpha1.LogicalClusterPhaseType
		absent       bool
		deepSAR      bool
		emptyCluster bool
		getterErr    error

		verb        string
		apiGroup    string
		resource    string
		name        string
		subresource string
		path        string

		policyResult authorizer.Decision
		policyErr    error

		wantDecision authorizer.Decision
		wantReason   string
		wantErr      bool
	}{
		"inactive cluster may be read and reactivated": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   `logical cluster "Inactive" self-management: ` + policyReason,
		},
		"inactive cluster discovery is served": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			path:         "/openapi/v3",
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   `logical cluster "Inactive" self-management: ` + policyReason,
		},
		"deleting cluster finalizes through its object": {
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   `logical cluster "Deleting" self-management: ` + policyReason,
		},
		// Locks in that a deleting cluster serves no discovery, only its object.
		"deleting cluster discovery is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			path:         "/openapi/v3",
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the LogicalCluster object or discovery",
		},
		// Locks in that only the object itself is admitted, not the rest of its API group.
		"logical cluster status is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			subresource:  "status",
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the LogicalCluster object or discovery",
		},
		"content of an inactive cluster is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			verb:         "get",
			resource:     "configmaps",
			name:         "cm",
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the LogicalCluster object or discovery",
		},
		"ready cluster needs no bypass": {
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "logical cluster is not inactive or deleting",
		},
		// Locks in that the bypass is bounded by the bootstrap policy.
		"request outside the bootstrap policy falls through": {
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionNoOpinion,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not covered by the bootstrap policy",
		},
		"bootstrap policy error": {
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyErr:    fmt.Errorf("informer not synced"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "error checking bootstrap policy",
			wantErr:      true,
		},
		"deep SAR is skipped": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			deepSAR:      true,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"empty cluster is skipped": {
			emptyCluster: true,
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty cluster name",
		},
		"missing logical cluster": {
			absent:       true,
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster not found",
		},
		"getter error": {
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			getterErr:    fmt.Errorf("informer not synced"),
			verb:         "update",
			apiGroup:     corev1alpha1.SchemeGroupVersion.Group,
			resource:     "logicalclusters",
			name:         corev1alpha1.LogicalClusterName,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "error getting LogicalCluster",
			wantErr:      true,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			cluster := logicalcluster.Name("root:ws")
			if cas.emptyCluster {
				cluster = logicalcluster.Name("")
			}

			ctx := t.Context()
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: cluster})
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			var getLogicalCluster LogicalClusterGetter
			if cas.absent {
				getLogicalCluster = stubLogicalClusterGetter()
			} else {
				getLogicalCluster = stubLogicalClusterGetter(newLogicalCluster(cluster, cas.phase))
			}
			if cas.getterErr != nil {
				getLogicalCluster = func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return nil, cas.getterErr
				}
			}

			bootstrapPolicy := authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
				if cas.policyErr != nil {
					return authorizer.DecisionNoOpinion, "policy error", cas.policyErr
				}
				return cas.policyResult, policyReason, nil
			})

			a := NewLogicalClusterSelfManagementBypassAuthorizer(getLogicalCluster, bootstrapPolicy)

			attr := authorizer.AttributesRecord{
				User:        &user.DefaultInfo{Name: "adam", UID: "uid-adam"},
				Verb:        cas.verb,
				APIGroup:    cas.apiGroup,
				Resource:    cas.resource,
				Name:        cas.name,
				Subresource: cas.subresource,
				Path:        cas.path,
			}

			decision, reason, err := a.Authorize(ctx, attr)
			if cas.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
