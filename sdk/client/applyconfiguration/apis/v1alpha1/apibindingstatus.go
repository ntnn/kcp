/*
Copyright The KCP Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

import (
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// APIBindingStatusApplyConfiguration represents a declarative configuration of the APIBindingStatus type for use
// with apply.
type APIBindingStatusApplyConfiguration struct {
	APIExportClusterName    *string                              `json:"apiExportClusterName,omitempty"`
	BoundResources          []BoundAPIResourceApplyConfiguration `json:"boundResources,omitempty"`
	Phase                   *apisv1alpha1.APIBindingPhaseType    `json:"phase,omitempty"`
	Conditions              *conditionsv1alpha1.Conditions       `json:"conditions,omitempty"`
	AppliedPermissionClaims []PermissionClaimApplyConfiguration  `json:"appliedPermissionClaims,omitempty"`
	ExportPermissionClaims  []PermissionClaimApplyConfiguration  `json:"exportPermissionClaims,omitempty"`
}

// APIBindingStatusApplyConfiguration constructs a declarative configuration of the APIBindingStatus type for use with
// apply.
func APIBindingStatus() *APIBindingStatusApplyConfiguration {
	return &APIBindingStatusApplyConfiguration{}
}

// WithAPIExportClusterName sets the APIExportClusterName field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the APIExportClusterName field is set to the value of the last call.
func (b *APIBindingStatusApplyConfiguration) WithAPIExportClusterName(value string) *APIBindingStatusApplyConfiguration {
	b.APIExportClusterName = &value
	return b
}

// WithBoundResources adds the given value to the BoundResources field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the BoundResources field.
func (b *APIBindingStatusApplyConfiguration) WithBoundResources(values ...*BoundAPIResourceApplyConfiguration) *APIBindingStatusApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithBoundResources")
		}
		b.BoundResources = append(b.BoundResources, *values[i])
	}
	return b
}

// WithPhase sets the Phase field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Phase field is set to the value of the last call.
func (b *APIBindingStatusApplyConfiguration) WithPhase(value apisv1alpha1.APIBindingPhaseType) *APIBindingStatusApplyConfiguration {
	b.Phase = &value
	return b
}

// WithConditions sets the Conditions field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Conditions field is set to the value of the last call.
func (b *APIBindingStatusApplyConfiguration) WithConditions(value conditionsv1alpha1.Conditions) *APIBindingStatusApplyConfiguration {
	b.Conditions = &value
	return b
}

// WithAppliedPermissionClaims adds the given value to the AppliedPermissionClaims field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the AppliedPermissionClaims field.
func (b *APIBindingStatusApplyConfiguration) WithAppliedPermissionClaims(values ...*PermissionClaimApplyConfiguration) *APIBindingStatusApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithAppliedPermissionClaims")
		}
		b.AppliedPermissionClaims = append(b.AppliedPermissionClaims, *values[i])
	}
	return b
}

// WithExportPermissionClaims adds the given value to the ExportPermissionClaims field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the ExportPermissionClaims field.
func (b *APIBindingStatusApplyConfiguration) WithExportPermissionClaims(values ...*PermissionClaimApplyConfiguration) *APIBindingStatusApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithExportPermissionClaims")
		}
		b.ExportPermissionClaims = append(b.ExportPermissionClaims, *values[i])
	}
	return b
}
