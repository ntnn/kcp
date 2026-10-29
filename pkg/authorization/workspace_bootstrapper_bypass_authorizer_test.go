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

func TestWorkspaceBootstrapperBypassAuthorizer(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		phase        corev1alpha1.LogicalClusterPhaseType
		absent       bool
		deepSAR      bool
		emptyCluster bool
		getterErr    error
		groups       []string

		wantDecision authorizer.Decision
		wantReason   string
		wantErr      bool
	}{
		"bootstrapper initializes the cluster": {
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "workspace bootstrapping",
		},
		// Locks in that the bootstrapper does not stay admitted once the cluster is usable.
		"bootstrapper in a ready cluster is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseReady,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "only initializing logical clusters are bootstrapped",
		},
		"bootstrapper in a terminating cluster is not admitted": {
			phase:        corev1alpha1.LogicalClusterPhaseTerminating,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "only initializing logical clusters are bootstrapped",
		},
		"other identities are not the bootstrapper": {
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{bootstrap.SystemKcpAdminGroup},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the workspace bootstrapper",
		},
		"deep SAR is skipped": {
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			deepSAR:      true,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"empty cluster is skipped": {
			emptyCluster: true,
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty cluster name",
		},
		"missing logical cluster": {
			absent:       true,
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster not found",
		},
		"getter error": {
			phase:        corev1alpha1.LogicalClusterPhaseInitializing,
			getterErr:    fmt.Errorf("informer not synced"),
			groups:       []string{bootstrap.SystemKcpWorkspaceBootstrapper},
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

			a := NewWorkspaceBootstrapperBypassAuthorizer(getLogicalCluster)

			attr := authorizer.AttributesRecord{
				User:     &user.DefaultInfo{Name: "system:kcp:bootstrapper", Groups: cas.groups},
				Verb:     "create",
				APIGroup: "rbac.authorization.k8s.io",
				Resource: "clusterrolebindings",
				Name:     "bootstrap",
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
