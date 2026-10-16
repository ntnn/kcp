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

package identityrotation

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestConcurrentRotationOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "export", ResourceVersion: "1",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Name: "old"}},
		},
		Status: apisv1alpha2.APIExportStatus{
			IdentityHash: "stable", IdentityKeyHashes: []string{"old"},
		},
	}
	stale := export.DeepCopy()
	newRotation := func(name string) *migrationv1alpha1.APIExportIdentityRotation {
		return &migrationv1alpha1.APIExportIdentityRotation{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
				Export: migrationv1alpha1.ExportReference{Name: "export"},
				NewIdentity: apisv1alpha2.Identity{
					SecretRef: &corev1.SecretReference{Name: name},
				},
			},
		}
	}
	winner, loser := newRotation("winner"), newRotation("loser")
	var c *Controller
	version := 1
	staleRead, race := false, true
	c = &Controller{
		getAPIExport: func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) {
			if staleRead {
				staleRead = false
				return stale.DeepCopy(), nil
			}
			return export.DeepCopy(), nil
		},
		getSecretHash: func(_ context.Context, _ logicalcluster.Path, _, name string) (string, error) {
			return name, nil
		},
		updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			if e.ResourceVersion != export.ResourceVersion {
				return nil, apierrors.NewConflict(apisv1alpha2.Resource("apiexports"), e.Name, fmt.Errorf("stale resource version"))
			}
			version++
			export = e.DeepCopy()
			export.ResourceVersion = strconv.Itoa(version)
			if race {
				race = false
				staleRead = true
				require.True(t, apierrors.IsConflict(c.reconcilePending(ctx, "root", loser)), "the stale contender must lose ownership before publishing")
				require.NoError(t, c.reconcilePending(ctx, "root", loser))
				require.Equal(t, migrationv1alpha1.APIExportIdentityRotationFailed, loser.Status.Phase)
			}
			return export.DeepCopy(), nil
		},
		updateAPIExportStatus: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			require.Equal(t, "root|winner|winner|old", export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey], "claim ownership before publishing fingerprints")
			require.NotContains(t, e.Status.IdentityKeyHashes, "loser")
			require.Equal(t, export.ResourceVersion, e.ResourceVersion)
			version++
			export.Status = e.DeepCopy().Status
			export.ResourceVersion = strconv.Itoa(version)
			return export.DeepCopy(), nil
		},
		updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, r *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
			return r, nil
		},
	}
	require.NoError(t, c.reconcilePending(ctx, "root", winner))
	require.Equal(t, migrationv1alpha1.APIExportIdentityRotationMigrating, winner.Status.Phase)
	require.Equal(t, "winner", export.Spec.Identity.SecretRef.Name)
	require.Equal(t, "stable", export.Status.IdentityHash)
	require.ElementsMatch(t, []string{"old", "winner"}, export.Status.IdentityKeyHashes)
}

func TestAuthorizedRotationCanReuseReservedInitialKey(t *testing.T) {
	t.Parallel()
	c := &Controller{
		lookupIdentity: func(string) (string, error) {
			return "", &indexers.IdentityReservedError{Identity: "original-key"}
		},
	}
	require.NoError(t, c.verifyKeyIdentities("original-key", "original-key"))
	require.Error(t, c.verifyKeyIdentities("other-identity", "original-key"))
}
