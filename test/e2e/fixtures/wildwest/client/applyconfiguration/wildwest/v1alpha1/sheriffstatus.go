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

// SheriffStatusApplyConfiguration represents a declarative configuration of the SheriffStatus type for use
// with apply.
type SheriffStatusApplyConfiguration struct {
	Result *string `json:"result,omitempty"`
}

// SheriffStatusApplyConfiguration constructs a declarative configuration of the SheriffStatus type for use with
// apply.
func SheriffStatus() *SheriffStatusApplyConfiguration {
	return &SheriffStatusApplyConfiguration{}
}

// WithResult sets the Result field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Result field is set to the value of the last call.
func (b *SheriffStatusApplyConfiguration) WithResult(value string) *SheriffStatusApplyConfiguration {
	b.Result = &value
	return b
}
