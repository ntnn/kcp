/*
Copyright 2025 The KCP Authors.

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

package garbagecollector

import (
	"encoding/json"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

type getFinalizers interface {
	GetFinalizers() []string
}

func hasFinalizer(obj getFinalizers, finalizer string) bool {
	finalizers := obj.GetFinalizers()
	return slices.Contains(finalizers, finalizer)
}

func patchRemoveFinalizer(obj getFinalizers, finalizer string) ([]byte, error) {
	finalizers := obj.GetFinalizers()
	newFinalizers := slices.Delete(finalizers, slices.Index(finalizers, finalizer), slices.Index(finalizers, finalizer)+1)
	dummy := metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: newFinalizers,
		},
	}
	return json.Marshal(&dummy)
}

type getOwnerReferences interface {
	GetOwnerReferences() []metav1.OwnerReference
}

// patchRemoveOwnerReferencesByUIDs builds a merge patch that removes all
// ownerReferences matching any of the given UIDs.
func patchRemoveOwnerReferencesByUIDs(obj getOwnerReferences, ownerUIDs []types.UID) ([]byte, error) {
	uidSet := make(map[types.UID]struct{}, len(ownerUIDs))
	for _, uid := range ownerUIDs {
		uidSet[uid] = struct{}{}
	}

	ownerReferences := obj.GetOwnerReferences()
	newOwnerReferences := slices.DeleteFunc(ownerReferences, func(ref metav1.OwnerReference) bool {
		_, remove := uidSet[ref.UID]
		return remove
	})
	dummy := metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: newOwnerReferences,
		},
	}
	return json.Marshal(&dummy)
}

// patchUnblockOwnerRefs builds a merge patch that sets
// BlockOwnerDeletion=false on all ownerReferences. Returns nil if no
// ownerReference has BlockOwnerDeletion=true.
func patchUnblockOwnerRefs(obj getOwnerReferences) ([]byte, error) {
	ownerReferences := obj.GetOwnerReferences()
	modified := false
	newRefs := make([]metav1.OwnerReference, len(ownerReferences))
	copy(newRefs, ownerReferences)
	for i := range newRefs {
		if newRefs[i].BlockOwnerDeletion != nil && *newRefs[i].BlockOwnerDeletion {
			newRefs[i].BlockOwnerDeletion = ptr.To(false)
			modified = true
		}
	}
	if !modified {
		return nil, nil
	}
	dummy := metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: newRefs,
		},
	}
	return json.Marshal(&dummy)
}
