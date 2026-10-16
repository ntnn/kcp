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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// ActiveRotationAnnotationKey coordinates exclusive ownership and retries of
// an APIExportIdentityRotation. Its value is
// "<logical cluster name>|<rotation name>|<new key fingerprint>|<old key fingerprint>"
// for stable-identity rotations, which never change bindings or storage and
// do not wait for shard reports. Historical storage-identity rotations use
// identity hashes in the last two parts; per-shard migrators use those hashes
// to locate the rotation and report progress without relying on separately
// replicated spec and status updates. The annotation is cleared on completion.
const ActiveRotationAnnotationKey = "migration.kcp.io/active-rotation"

// APIExportIdentityRotation is a one-shot request to rotate an APIExport's
// identity key onto a fresh identity secret. It lives next to the APIExport in
// the provider workspace and is served through the platform-owned
// migration APIExport: possessing the type requires binding that export,
// which is how the platform delegates the rotation capability.
//
// Rotation is a tracked, resumable procedure: the new key fingerprint is
// accepted before switching the identity secret, and the old fingerprint
// remains accepted until it is retired per spec.aliasRetirement. The export's
// stable identityHash and its consumers' storage prefixes do not change.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Export",type="string",JSONPath=".spec.export.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Migrated",type="string",JSONPath=".status.migratedBindings"
// +kubebuilder:printcolumn:name="Total",type="string",JSONPath=".status.totalBindings"
type APIExportIdentityRotation struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec APIExportIdentityRotationSpec `json:"spec"`

	// +optional
	Status APIExportIdentityRotationStatus `json:"status,omitempty"`
}

// APIExportIdentityRotationSpec is the request: which export, which new
// identity key, and when the old key fingerprint is retired.
type APIExportIdentityRotationSpec struct {
	// export references the APIExport whose identity key is rotated. The
	// referenced export may live in another workspace (and on another
	// shard): rotation is a platform capability, delegated by binding the
	// migration.kcp.io APIExport, and does not require exposing that
	// capability in the workspaces owning the rotated exports. Immutable.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="export is immutable"
	Export ExportReference `json:"export"`

	// newIdentity references the pre-created secret holding the new
	// identity key. Immutable.
	//
	// +required
	NewIdentity apisv1alpha2.Identity `json:"newIdentity"`

	// aliasRetirement decides when the old identity key fingerprint stops
	// being accepted in the export's status.identityKeyHashes. For historical
	// storage-identity rotations, it controls retirement of the old identity alias:
	//
	//   - Manual (default): the fingerprint remains until the provider retires it by
	//     updating this field to Immediate (or After with an elapsed
	//     duration).
	//   - After: the fingerprint is retired the given duration after the rotation
	//     entered AliasActive.
	//   - Immediate: no overlap window; the old fingerprint is retired once
	//     the secret is switched. This is the leaked-secret remediation mode.
	//
	// The field is mutable while the rotation is in AliasActive, but only
	// toward earlier retirement. Retirement is irreversible.
	//
	// +optional
	// +kubebuilder:default={policy: Manual}
	AliasRetirement AliasRetirement `json:"aliasRetirement,omitempty"`
}

// ExportReference identifies the APIExport being rotated.
type ExportReference struct {
	// path is the workspace path of the APIExport (e.g. root:org:provider,
	// or a logical cluster name). Empty means the rotation's own workspace.
	//
	// +optional
	Path string `json:"path,omitempty"`

	// name is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AliasRetirement configures when the pre-rotation identity key fingerprint is
// retired.
type AliasRetirement struct {
	// policy selects the retirement mode.
	//
	// +required
	// +kubebuilder:validation:Enum=Manual;After;Immediate
	Policy AliasRetirementPolicy `json:"policy"`

	// after is the deprecation window for the After policy, measured from
	// the rotation entering AliasActive.
	//
	// +optional
	After *metav1.Duration `json:"after,omitempty"`
}

// AliasRetirementPolicy is a retirement mode for the old identity key fingerprint.
type AliasRetirementPolicy string

const (
	// AliasRetirementManual keeps the old fingerprint until the provider explicitly
	// retires it.
	AliasRetirementManual AliasRetirementPolicy = "Manual"
	// AliasRetirementAfter retires the old fingerprint a fixed duration after
	// the secret is switched.
	AliasRetirementAfter AliasRetirementPolicy = "After"
	// AliasRetirementImmediate retires the old fingerprint once the secret
	// is switched.
	AliasRetirementImmediate AliasRetirementPolicy = "Immediate"
)

// APIExportIdentityRotationPhase describes the lifecycle position of a
// rotation.
type APIExportIdentityRotationPhase string

const (
	// APIExportIdentityRotationPending: the rotation is validated and the
	// new key fingerprint computed.
	APIExportIdentityRotationPending APIExportIdentityRotationPhase = "Pending"
	// APIExportIdentityRotationMigrating: the new fingerprint is accepted
	// and the identity secret is switched. Stable-identity rotations proceed
	// without shard reports; historical storage-identity rotations aggregate
	// per-shard drain progress before proceeding.
	APIExportIdentityRotationMigrating APIExportIdentityRotationPhase = "Migrating"
	// APIExportIdentityRotationAliasActive: the new secret is in use; the old
	// key fingerprint remains accepted until retirement. Historical
	// storage-identity rotations retain the old hash as an identity alias.
	APIExportIdentityRotationAliasActive APIExportIdentityRotationPhase = "AliasActive"
	// APIExportIdentityRotationCompleted: the old key fingerprint is retired
	// and its secret is deletable. The export's stable identity is unchanged.
	APIExportIdentityRotationCompleted APIExportIdentityRotationPhase = "Completed"
	// APIExportIdentityRotationFailed: the rotation cannot proceed; see
	// conditions.
	APIExportIdentityRotationFailed APIExportIdentityRotationPhase = "Failed"
)

// APIExportIdentityRotationStatus reports rotation progress.
type APIExportIdentityRotationStatus struct {
	// phase is the current lifecycle phase of the rotation.
	//
	// +optional
	Phase APIExportIdentityRotationPhase `json:"phase,omitempty"`

	// oldIdentityHash is the SHA-256 fingerprint of the identity key the
	// export used when a stable-identity rotation started, not its stable
	// identityHash. Historical storage-identity rotations record the old
	// identity hash instead.
	//
	// +optional
	OldIdentityHash string `json:"oldIdentityHash,omitempty"`

	// newIdentityHash is the SHA-256 fingerprint of the new identity key.
	// Stable-identity rotations accept this value in the export's
	// status.identityKeyHashes without changing its stable identityHash.
	// Historical storage-identity rotations record the target identity hash instead.
	//
	// +optional
	NewIdentityHash string `json:"newIdentityHash,omitempty"`

	// migratedBindings is the number of APIBindings whose bound instances
	// are fully drained onto the new identity, summed over all shards that
	// have reported in status.shards.
	//
	// +optional
	MigratedBindings int32 `json:"migratedBindings,omitempty"`

	// totalBindings is the number of APIBindings bound to the rotating
	// export, summed over all shards that have reported in status.shards.
	//
	// +optional
	TotalBindings int32 `json:"totalBindings,omitempty"`

	// shards reports drain progress per shard. Each shard's identity
	// migrator maintains its own entry (including shards with zero
	// bindings, so completeness is decidable). The drain is only considered
	// complete once every shard has reported and every entry is fully
	// migrated.
	//
	// +optional
	// +listType=map
	// +listMapKey=shard
	Shards []ShardMigrationProgress `json:"shards,omitempty"`

	// aliasActiveTimestamp records when the rotation entered AliasActive,
	// the reference point for the After retirement policy.
	//
	// +optional
	AliasActiveTimestamp *metav1.Time `json:"aliasActiveTimestamp,omitempty"`

	// conditions is a list of conditions that apply to the rotation.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ShardMigrationProgress is one shard's self-reported drain progress for a
// rotation. It is written by the identity migrator running on that shard via
// server-side apply, with a per-shard field manager, so shards never conflict
// with each other or with the rotation controller.
type ShardMigrationProgress struct {
	// shard is the name of the reporting shard.
	//
	// +required
	Shard string `json:"shard"`

	// identityHash is the target identity the counts were evaluated
	// against. Reports for any other hash than the rotation's new identity
	// are ignored by the rotation controller.
	//
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// totalBindings is the number of APIBindings of the rotating export
	// hosted on this shard. Zero is meaningful ("this shard has nothing to
	// drain") and always serialized.
	//
	// +required
	TotalBindings int32 `json:"totalBindings"`

	// migratedBindings is the number of those bindings fully drained onto
	// the new identity.
	//
	// +required
	MigratedBindings int32 `json:"migratedBindings"`

	// lastUpdateTime is when this shard last refreshed its entry.
	//
	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
}

func (in *APIExportIdentityRotation) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExportIdentityRotation) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of APIExportIdentityRotation.
const (
	// IdentityRotationValid indicates the rotation request passed
	// validation: the export exists, the new secret exists and hashes to a
	// new identity, and no conflicting rotation or binding migration is in
	// progress.
	IdentityRotationValid conditionsv1alpha1.ConditionType = "Valid"

	// IdentityRotationDrained indicates all bindings of the export are
	// drained onto the new identity.
	IdentityRotationDrained conditionsv1alpha1.ConditionType = "Drained"

	// IdentityRotationAliasRetired indicates the old identity alias has
	// been retired.
	IdentityRotationAliasRetired conditionsv1alpha1.ConditionType = "AliasRetired"
)

// APIExportIdentityRotationList is a list of APIExportIdentityRotation
// resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportIdentityRotationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExportIdentityRotation `json:"items"`
}
