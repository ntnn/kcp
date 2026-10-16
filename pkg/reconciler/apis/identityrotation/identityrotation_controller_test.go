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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestReconcileMigratingAggregation(t *testing.T) {
	t.Parallel()
	const newHash = "new"
	entry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, IdentityHash: newHash, TotalBindings: total, MigratedBindings: migrated}
	}
	staleEntry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, IdentityHash: "old", TotalBindings: total, MigratedBindings: migrated}
	}
	scenarios := []struct {
		name             string
		shards           []string
		entries          []migrationv1alpha1.ShardMigrationProgress
		expectedPhase    migrationv1alpha1.APIExportIdentityRotationPhase
		expectedTotal    int32
		expectedMigrated int32
		expectedEntries  int
	}{
		{
			name:             "drained when every shard reported and is fully migrated",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  2,
		},
		{
			name:             "not drained while a shard has not reported",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  1,
		},
		{
			name:             "not drained while a shard is mid-drain",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 3, 1)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    5,
			expectedMigrated: 3,
			expectedEntries:  2,
		},
		{
			name:             "entries of removed shards are ignored and do not block the drain",
			shards:           []string{"root"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 1, 1), entry("gone", 4, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    1,
			expectedMigrated: 1,
			expectedEntries:  2, // entries are migrator-owned; the controller does not prune them
		},
		{
			name:             "zero bindings everywhere drains immediately",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 0, 0), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    0,
			expectedMigrated: 0,
			expectedEntries:  2,
		},
		{
			// a shard that counted against the pre-rotation identity (stale
			// replicated export status) reports everything as migrated; that
			// report must not satisfy the gate.
			name:             "reports against a stale identity are ignored",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), staleEntry("alpha", 3, 3)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  2,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			var updated *migrationv1alpha1.APIExportIdentityRotation
			c := &Controller{
				getAPIExport: func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Status: apisv1alpha2.APIExportStatus{IdentityHash: newHash, IdentityKeyHashes: []string{newHash}},
					}, nil
				},
				listShardNames: func() ([]string, error) {
					return scenario.shards, nil
				},
				updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
					updated = rotation
					return rotation, nil
				},
			}
			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				ObjectMeta: metav1.ObjectMeta{Name: "rot"},
				Status: migrationv1alpha1.APIExportIdentityRotationStatus{
					Phase:           migrationv1alpha1.APIExportIdentityRotationMigrating,
					NewIdentityHash: newHash,
					Shards:          scenario.entries,
				},
			}
			if err := c.reconcileMigrating(context.Background(), "root", rotation); err != nil {
				t.Fatal(err)
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			if updated.Status.Phase != scenario.expectedPhase {
				t.Errorf("expected phase %q, got %q", scenario.expectedPhase, updated.Status.Phase)
			}
			if updated.Status.TotalBindings != scenario.expectedTotal || updated.Status.MigratedBindings != scenario.expectedMigrated {
				t.Errorf("expected %d/%d bindings, got %d/%d", scenario.expectedMigrated, scenario.expectedTotal, updated.Status.MigratedBindings, updated.Status.TotalBindings)
			}
			if len(updated.Status.Shards) != scenario.expectedEntries {
				t.Errorf("expected %d shard entries, got %d", scenario.expectedEntries, len(updated.Status.Shards))
			}
			drained := conditions.IsTrue(updated, migrationv1alpha1.IdentityRotationDrained)
			if wantDrained := scenario.expectedPhase == migrationv1alpha1.APIExportIdentityRotationAliasActive; drained != wantDrained {
				t.Errorf("expected Drained=%v, got %v", wantDrained, drained)
			}
		})
	}
}

// TestReconcilePendingRetryAfterFlip covers the retry of a Pending rotation
// after a previous attempt already flipped the export but failed before
// recording Migrating on the rotation. The export's current secret then already
// contains the new key; without the active-rotation annotation
// recovery this re-validation used to terminally fail the rotation with the
// fresh-secret error.
func TestReconcilePendingRetryAfterFlip(t *testing.T) {
	t.Parallel()
	const (
		oldHash = "oldhash"
		newHash = "newhash"
	)

	scenarios := []struct {
		name            string
		annotation      string
		exportIdentity  string
		expectedPhase   migrationv1alpha1.APIExportIdentityRotationPhase
		expectedOldHash string
		legacyAlias     bool
		currentSecret   string
	}{
		{
			name:            "retry after flip resumes into Migrating with the annotation's old hash",
			annotation:      "root|rot|" + newHash + "|" + oldHash,
			exportIdentity:  newHash,
			expectedPhase:   migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedOldHash: oldHash,
		},
		{
			name:            "retry after ownership resumes before key publication",
			annotation:      "root|rot|" + newHash + "|" + oldHash,
			exportIdentity:  oldHash,
			currentSecret:   "old-secret",
			expectedPhase:   migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedOldHash: oldHash,
		},
		{
			name:           "a genuinely reused secret without an active rotation still fails",
			annotation:     "",
			exportIdentity: newHash,
			expectedPhase:  migrationv1alpha1.APIExportIdentityRotationFailed,
		},
		{
			name:           "an active rotation of a different rotation object fails this one",
			annotation:     "root|other-rot|" + newHash + "|" + oldHash,
			exportIdentity: newHash,
			expectedPhase:  migrationv1alpha1.APIExportIdentityRotationFailed,
		},
		{
			name:           "legacy pending migration fails safely without retiring its alias",
			annotation:     "root|rot|" + newHash + "|" + oldHash,
			exportIdentity: newHash,
			legacyAlias:    true,
			expectedPhase:  migrationv1alpha1.APIExportIdentityRotationFailed,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			export := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "today-cowboys",
					Annotations: map[string]string{"kcp.io/cluster": "root"},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "default", Name: "new-secret"}},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash:      "stable",
					IdentityKeyHashes: []string{scenario.exportIdentity},
				},
			}
			if scenario.annotation != "" {
				export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey] = scenario.annotation
			}
			if scenario.legacyAlias {
				export.Status.IdentityAliasHashes = []string{oldHash}
			}
			if scenario.currentSecret != "" {
				export.Spec.Identity.SecretRef.Name = scenario.currentSecret
			}

			var updated *migrationv1alpha1.APIExportIdentityRotation
			c := &Controller{
				getAPIExport: func(_ logicalcluster.Path, _ string) (*apisv1alpha2.APIExport, error) {
					return export, nil
				},
				getSecretHash: func(_ context.Context, _ logicalcluster.Path, _, name string) (string, error) {
					if name == "old-secret" {
						return oldHash, nil
					}
					return newHash, nil
				},
				updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					return e, nil
				},
				updateAPIExportStatus: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					require.Equal(t, "stable", e.Status.IdentityHash)
					return e, nil
				},
				updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, r *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
					updated = r
					return r, nil
				},
			}

			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				ObjectMeta: metav1.ObjectMeta{Name: "rot"},
				Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
					Export:      migrationv1alpha1.ExportReference{Name: "today-cowboys"},
					NewIdentity: apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "default", Name: "new-secret"}},
				},
				Status: migrationv1alpha1.APIExportIdentityRotationStatus{
					Phase: migrationv1alpha1.APIExportIdentityRotationPending,
				},
			}

			if err := c.reconcilePending(context.Background(), logicalcluster.Name("root"), rotation); err != nil {
				t.Fatalf("reconcilePending returned error: %v", err)
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			if updated.Status.Phase != scenario.expectedPhase {
				t.Errorf("expected phase %q, got %q (conditions: %+v)", scenario.expectedPhase, updated.Status.Phase, updated.Status.Conditions)
			}
			if scenario.legacyAlias {
				require.Equal(t, []string{oldHash}, export.Status.IdentityAliasHashes)
				require.Equal(t, "stable", export.Status.IdentityHash)
			}
			if scenario.expectedPhase == migrationv1alpha1.APIExportIdentityRotationMigrating {
				if updated.Status.OldIdentityHash != scenario.expectedOldHash {
					t.Errorf("expected old identity hash %q, got %q", scenario.expectedOldHash, updated.Status.OldIdentityHash)
				}
				if updated.Status.NewIdentityHash != newHash {
					t.Errorf("expected new identity hash %q, got %q", newHash, updated.Status.NewIdentityHash)
				}
			}
		})
	}
}

func TestStableKeyRotation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		stableID       string
		invalidCurrent bool
	}{
		{stableID: "stable"},
		{stableID: "stable", invalidCurrent: true},
		{stableID: "new"},
	} {
		t.Run(fmt.Sprintf("identity %s invalid current key %v", tt.stableID, tt.invalidCurrent), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			export := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{Name: "export", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
				Spec: apisv1alpha2.APIExportSpec{
					Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Name: "old"}},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: tt.stableID, IdentityKeyHashes: []string{"old"},
				},
			}
			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				ObjectMeta: metav1.ObjectMeta{Name: "rotation"},
				Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
					Export:      migrationv1alpha1.ExportReference{Name: "export"},
					NewIdentity: apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Name: "new"}},
					AliasRetirement: migrationv1alpha1.AliasRetirement{
						Policy: migrationv1alpha1.AliasRetirementImmediate,
					},
				},
			}
			var writes int
			c := &Controller{
				getAPIExport: func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) { return export, nil },
				getSecretHash: func(_ context.Context, _ logicalcluster.Path, _, name string) (string, error) {
					if name == "old" && tt.invalidCurrent {
						return "foreign", nil
					}
					return name, nil
				},
				updateAPIExportStatus: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					writes++
					require.Equal(t, tt.stableID, e.Status.IdentityHash)
					require.NotEmpty(t, export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey], "ownership must precede key publication")
					export = e.DeepCopy()
					return export, nil
				},
				updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					writes++
					if e.Spec.Identity.SecretRef.Name == "new" {
						require.Contains(t, export.Status.IdentityKeyHashes, "new", "accept new key before switching secret")
					} else {
						require.Equal(t, []string{"old"}, export.Status.IdentityKeyHashes, "ownership acquisition must not publish a key")
					}
					export = e.DeepCopy()
					return export, nil
				},
				updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, r *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
					return r, nil
				},
				listShardNames: func() ([]string, error) {
					t.Fatal("stable rotation must not require storage drain reports")
					return nil, nil
				},
				lookupIdentity: func(hash string) (string, error) {
					if hash == tt.stableID {
						return "", &indexers.IdentityReservedError{Identity: tt.stableID}
					}
					return "", nil
				},
			}
			require.NoError(t, c.reconcilePending(ctx, "root", rotation))
			if tt.invalidCurrent {
				require.Equal(t, migrationv1alpha1.APIExportIdentityRotationFailed, rotation.Status.Phase)
				require.Zero(t, writes)
				return
			}
			require.Equal(t, tt.stableID, export.Status.IdentityHash)
			require.ElementsMatch(t, []string{"old", "new"}, export.Status.IdentityKeyHashes)
			require.Empty(t, export.Status.IdentityAliasHashes)
			require.Equal(t, "new", export.Spec.Identity.SecretRef.Name)
			require.NoError(t, c.reconcileMigrating(ctx, "root", rotation))
			require.Equal(t, migrationv1alpha1.APIExportIdentityRotationAliasActive, rotation.Status.Phase)
			rotation.Spec.AliasRetirement.Policy = migrationv1alpha1.AliasRetirementManual
			delay, err := c.reconcileAliasActive(ctx, "root", rotation)
			require.NoError(t, err)
			require.Zero(t, delay)
			require.Contains(t, export.Status.IdentityKeyHashes, "old")
			rotation.Spec.AliasRetirement.Policy = migrationv1alpha1.AliasRetirementAfter
			rotation.Spec.AliasRetirement.After = &metav1.Duration{Duration: time.Hour}
			delay, err = c.reconcileAliasActive(ctx, "root", rotation)
			require.NoError(t, err)
			require.Positive(t, delay)
			require.Contains(t, export.Status.IdentityKeyHashes, "old")
			rotation.Spec.AliasRetirement.Policy = migrationv1alpha1.AliasRetirementImmediate
			export.Spec.Identity.SecretRef.Name = "old"
			_, err = c.reconcileAliasActive(ctx, "root", rotation)
			require.Error(t, err)
			require.Contains(t, export.Status.IdentityKeyHashes, "old")
			export.Spec.Identity.SecretRef.Name = "new"
			c.lookupIdentity = func(string) (string, error) { return "conflicting", nil }
			_, err = c.reconcileAliasActive(ctx, "root", rotation)
			require.Error(t, err)
			require.Contains(t, export.Status.IdentityKeyHashes, "old")
			c.lookupIdentity = nil
			_, err = c.reconcileAliasActive(ctx, "root", rotation)
			require.NoError(t, err)
			require.Equal(t, migrationv1alpha1.APIExportIdentityRotationCompleted, rotation.Status.Phase)
			require.Equal(t, tt.stableID, export.Status.IdentityHash)
			require.Equal(t, []string{"new"}, export.Status.IdentityKeyHashes)
		})
	}
}

func TestKeyPublicationFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, id string
		lookup   func(string) (string, error)
	}{
		{name: "uninitialized identity"},
		{name: "conflicting identity", id: "stable", lookup: func(string) (string, error) { return "other", nil }},
		{name: "unsynchronized identity lookup", id: "stable", lookup: func(string) (string, error) { return "", errors.New("not synced") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			export := &apisv1alpha2.APIExport{
				Spec: apisv1alpha2.APIExportSpec{
					Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Name: "old"}},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: tt.id, IdentityKeyHashes: []string{"old"},
				},
			}
			c := &Controller{
				getAPIExport: func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) { return export, nil },
				getSecretHash: func(_ context.Context, _ logicalcluster.Path, _, name string) (string, error) {
					return name, nil
				},
				lookupIdentity: tt.lookup,
			}
			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
					NewIdentity: apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Name: "new"}},
				},
			}
			require.Error(t, c.reconcilePending(context.Background(), "root", rotation))
			require.Equal(t, tt.id, export.Status.IdentityHash)
			require.Equal(t, []string{"old"}, export.Status.IdentityKeyHashes)
			require.Equal(t, "old", export.Spec.Identity.SecretRef.Name)
		})
	}
}
