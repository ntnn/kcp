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

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// LogicalRoot is the root of a workspace tree outside of the root workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Root Path",type=string,JSONPath=`.spec.rootPath`,description="Alias for the first path segment of the tree"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.clusterName`,description="Name of the root logical cluster"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready, Deleting)"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.URL`,description="URL to access the root logical cluster"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type LogicalRoot struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	// +kubebuilder:default={}
	Spec LogicalRootSpec `json:"spec,omitempty"`

	// +optional
	Status LogicalRootStatus `json:"status,omitempty"`
}

// LogicalRootSpec holds the desired state of the LogicalRoot.
//
// +kubebuilder:validation:XValidation:rule="has(oldSelf.rootPath) == has(self.rootPath)",message="rootPath is immutable"
// +kubebuilder:validation:XValidation:rule="has(oldSelf.type) == has(self.type)",message="type is immutable"
type LogicalRootSpec struct {
	// rootPath is an alias for the first path segment of the tree.
	// When empty the tree is addressed by status.clusterName.
	//
	// Logical cluster names take precedence over root paths.
	// Setting it requires the LogicalRootAllowCustomNames feature gate.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="rootPath is immutable"
	// +kubebuilder:validation:XValidation:rule="!(self in ['root', 'system', 'user'])",message="rootPath is reserved"
	RootPath string `json:"rootPath,omitempty"`

	// type is the WorkspaceType of the root logical cluster.
	// It must not allow a parent.
	// When empty the type root in the root workspace is used.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type *tenancyv1alpha1.WorkspaceTypeReference `json:"type,omitempty"`

	// location constrains the shards the root logical cluster can be scheduled to.
	// When empty an arbitrary shard is chosen.
	//
	// +optional
	Location *tenancyv1alpha1.WorkspaceLocation `json:"location,omitempty"`
}

// LogicalRootStatus communicates the observed state of the LogicalRoot.
type LogicalRootStatus struct {
	// clusterName is the generated name of the root logical cluster.
	//
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// phase of the root logical cluster (Scheduling, Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase corev1alpha1.LogicalClusterPhaseType `json:"phase,omitempty"`

	// URL is the address under which the root logical cluster can be reached.
	//
	// +optional
	URL string `json:"URL,omitempty"`

	// conditions is the current processing state of the LogicalRoot.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// initializers must be cleared by a controller
	// before the root logical cluster is ready.
	//
	// +optional
	Initializers []corev1alpha1.LogicalClusterInitializer `json:"initializers,omitempty"`

	// terminators must be cleared by a controller
	// before the root logical cluster is deleted.
	//
	// +optional
	Terminators []corev1alpha1.LogicalClusterTerminator `json:"terminators,omitempty"`
}

func (in *LogicalRoot) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *LogicalRoot) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

// LogicalRootList is a list of LogicalRoots.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LogicalRootList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []LogicalRoot `json:"items"`
}
