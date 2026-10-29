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

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestOwnerImpersonationBypassAuthorizer(t *testing.T) {
	t.Parallel()

	creator := &corev1alpha1.OwnerUserInfo{Username: "adam", UID: "uid-adam"}

	cases := map[string]struct {
		phase        corev1alpha1.LogicalClusterPhaseType
		createdBy    *corev1alpha1.OwnerUserInfo
		absent       bool
		deepSAR      bool
		emptyCluster bool
		getterErr    error

		userName string
		uid      string
		groups   []string

		wantDecision authorizer.Decision
		wantReason   string
		wantErr      bool
	}{
		"creator through the initializing virtual workspace": {
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "owner impersonation fallback",
		},
		"creator through the terminating virtual workspace": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "owner impersonation fallback",
		},
		"creator while the cluster deletes": {
			phase:        corev1alpha1.LogicalClusterPhaseDeleting,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "owner impersonation fallback",
		},
		// Locks in that only the phases the lifecycle virtual workspaces serve are opened.
		"no virtual workspace serves a scheduling cluster": {
			phase:        corev1alpha1.LogicalClusterPhaseScheduling,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `phase "Scheduling" is not served by a lifecycle virtual workspace`,
		},
		"ready clusters need no bypass": {
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   `phase "Ready" is not served by a lifecycle virtual workspace`,
		},
		// Locks in that the group alone is not enough: the identity has to be the recorded creator.
		"foreign uid with the group is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			uid:          "uid-eve",
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "identity is not the recorded creator",
		},
		"foreign name with the group is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			userName:     "eve",
			uid:          creator.UID,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "identity is not the recorded creator",
		},
		"cluster without a recorded creator is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "identity is not the recorded creator",
		},
		"identity without the group is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "no owner impersonation group",
		},
		"deep SAR is skipped": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			deepSAR:      true,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"empty cluster is skipped": {
			emptyCluster: true,
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			createdBy:    creator,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty cluster name",
		},
		"missing logical cluster": {
			absent:       true,
			groups:       []string{bootstrap.SystemKcpOwnerImpersonationGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster not found",
		},
		"getter error": {
			phase:     corev1alpha1.LogicalClusterPhaseTerminating,
			getterErr: fmt.Errorf("informer not synced"),
			groups:    []string{bootstrap.SystemKcpOwnerImpersonationGroup},

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
				logicalCluster := newLogicalCluster(cluster, cas.phase)
				logicalCluster.Spec.CreatedBy = cas.createdBy
				getLogicalCluster = stubLogicalClusterGetter(logicalCluster)
			}
			if cas.getterErr != nil {
				getLogicalCluster = func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return nil, cas.getterErr
				}
			}

			a := NewOwnerImpersonationBypassAuthorizer(getLogicalCluster)

			name := cas.userName
			if name == "" {
				name = creator.Username
			}
			uid := cas.uid
			if uid == "" {
				uid = creator.UID
			}

			attr := authorizer.AttributesRecord{
				User:     &user.DefaultInfo{Name: name, UID: uid, Groups: cas.groups},
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
