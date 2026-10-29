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

func TestLogicalClusterPhaseAuthorizer(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cluster      string
		phase        corev1alpha1.LogicalClusterPhaseType
		absent       bool
		deepSAR      bool
		getterErr    error
		wantDecision authorizer.Decision
		wantReason   string
		wantErr      bool
	}{
		"ready phase permits access": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   `delegating due to phase "Ready" permits access`,
		},
		"initializing phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Initializing"`,
		},
		"scheduling phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseScheduling,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Scheduling"`,
		},
		"unavailable phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseUnavailable,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Unavailable"`,
		},
		"inactive phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseInactive,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Inactive"`,
		},
		"terminating phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Terminating"`,
		},
		"deleting phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "Deleting"`,
		},
		"unknown phase is denied": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseType("FuturePhase"),
			wantDecision: authorizer.DecisionDeny,
			wantReason:   `not permitted due to phase "FuturePhase"`,
		},
		"system workspace is skipped": {
			cluster:      "system:api",
			phase:        corev1alpha1.LogicalClusterPhaseScheduling,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to empty or system workspace",
		},
		"empty cluster is skipped": {
			cluster:      "",
			phase:        corev1alpha1.LogicalClusterPhaseScheduling,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to empty or system workspace",
		},
		"missing logical cluster delegates to the existence pre-gate": {
			cluster:      "root:gone",
			absent:       true,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to LogicalCluster not found",
		},
		"deep SAR is skipped": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseScheduling,
			deepSAR:      true,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to deep SAR request",
		},
		"getter error": {
			cluster:      "root:ws",
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			getterErr:    fmt.Errorf("informer not synced"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "error getting LogicalCluster",
			wantErr:      true,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: logicalcluster.Name(cas.cluster)})
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			var getLogicalCluster LogicalClusterGetter
			if cas.absent {
				getLogicalCluster = stubLogicalClusterGetter()
			} else {
				getLogicalCluster = stubLogicalClusterGetter(newLogicalCluster(logicalcluster.Name(cas.cluster), cas.phase))
			}
			if cas.getterErr != nil {
				getLogicalCluster = func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return nil, cas.getterErr
				}
			}

			a := NewLogicalClusterPhaseAuthorizer(getLogicalCluster)(
				authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
					return authorizer.DecisionAllow, "delegate", nil
				}),
			)

			attr := authorizer.AttributesRecord{
				User:     &user.DefaultInfo{Name: "adam", UID: "uid-adam"},
				Verb:     "get",
				Resource: "configmaps",
				Name:     "cm",
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
