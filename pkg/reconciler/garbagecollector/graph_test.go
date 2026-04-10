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
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
)

var (
	testNodeA = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       "test-deployment",
			UID:        "uid-deployment",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	testNodeB = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       "test-pod",
			UID:        "uid-pod",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	testNodeC = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       "test-pod2",
			UID:        "uid-pod2",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
)

func TestGraph_Nodes(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	t.Log("Adding Deployment node to graph")
	graph.Add(testNodeA, nil, nil)

	t.Log("Add Pod nodes with Deployment as owner")
	graph.Add(testNodeB, nil, []ObjectReference{testNodeA})
	graph.Add(testNodeC, nil, []ObjectReference{testNodeA})

	t.Log("Verify that Deployment owns the two Pods")
	owned := graph.Owned(testNodeA)
	assert.Equal(t, 2, len(owned), "expected Deployment to own 2 Pods")
	assert.Contains(t, owned, testNodeB, "expected Deployment to own testNodeB")
	assert.Contains(t, owned, testNodeC, "expected Deployment to own testNodeC")

	t.Log("Verify reverse: Pods have Deployment as owner")
	ownersB := graph.Owners(testNodeB)
	assert.Equal(t, 1, len(ownersB), "expected Pod B to have 1 owner")
	assert.Contains(t, ownersB, testNodeA, "expected Pod B's owner to be Deployment")

	ownersC := graph.Owners(testNodeC)
	assert.Equal(t, 1, len(ownersC), "expected Pod C to have 1 owner")
	assert.Contains(t, ownersC, testNodeA, "expected Pod C's owner to be Deployment")

	t.Log("Remove one Pod and verify ownership")
	removed, owned := graph.Remove(testNodeB)
	assert.True(t, removed, "expected Pod removal to succeed")
	assert.Equal(t, 0, len(owned), "expected Pod to own 0 objects upon removal")

	owned = graph.Owned(testNodeA)
	assert.Equal(t, 1, len(owned), "expected Deployment to own 1 Pod after removal")
	assert.Contains(t, owned, testNodeC, "expected Deployment to still own testNodeC")

	t.Log("Verify removed Pod has no owners")
	ownersB = graph.Owners(testNodeB)
	assert.Nil(t, ownersB, "expected removed Pod B to have no owners")

	t.Log("Try removing Deployment with owned Pod")
	success, owned := graph.Remove(testNodeA)
	assert.False(t, success, "expected Deployment removal to fail due to owned Pods")
	assert.Equal(t, 1, len(owned), "expected Deployment to own 1 Pod during removal attempt")

	t.Log("Remove remaining Pod and verify Deployment ownership")
	graph.Remove(testNodeC)

	owned = graph.Owned(testNodeA)
	assert.Equal(t, 0, len(owned), "expected Deployment to own 0 Pods after all removals")

	t.Log("Now remove Deployment successfully")
	success, owned = graph.Remove(testNodeA)
	assert.True(t, success, "expected Deployment removal to succeed with no owned Pods")
	assert.Equal(t, 0, len(owned), "expected no owned Pods during Deployment removal")
}

func TestGraph_OwnedNilSafe(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	// Owned on a reference not in the graph should return nil, not panic.
	owned := graph.Owned(testNodeA)
	assert.Nil(t, owned, "expected nil for non-existent object")
}

func TestGraph_OwnersNilSafe(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	// Owners on a reference not in the graph should return nil, not panic.
	owners := graph.Owners(testNodeA)
	assert.Nil(t, owners, "expected nil for non-existent dependent")
}

func TestGraph_MultipleOwners(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	ownerA := testNodeA
	ownerB := ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "ReplicaSet",
			Name:       "test-rs",
			UID:        "uid-rs",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	dependent := testNodeB

	graph.Add(ownerA, nil, nil)
	graph.Add(ownerB, nil, nil)
	graph.Add(dependent, nil, []ObjectReference{ownerA, ownerB})

	t.Log("Verify dependent has two owners")
	owners := graph.Owners(dependent)
	assert.Equal(t, 2, len(owners))
	assert.Contains(t, owners, ownerA)
	assert.Contains(t, owners, ownerB)

	t.Log("Verify both owners list the dependent")
	assert.Contains(t, graph.Owned(ownerA), dependent)
	assert.Contains(t, graph.Owned(ownerB), dependent)

	t.Log("Update dependent to only have ownerB, then remove ownerA")
	graph.Add(dependent, []ObjectReference{ownerA, ownerB}, []ObjectReference{ownerB})

	assert.Equal(t, 0, len(graph.Owned(ownerA)), "ownerA should have no dependents after update")
	success, _ := graph.Remove(ownerA)
	assert.True(t, success)

	t.Log("Verify dependent's owner list is updated")
	owners = graph.Owners(dependent)
	assert.Equal(t, 1, len(owners), "expected dependent to have 1 owner after removal")
	assert.Contains(t, owners, ownerB)
}

func TestGraph_OwnerUpdate(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	ownerA := testNodeA
	ownerB := ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "ReplicaSet",
			Name:       "test-rs",
			UID:        "uid-rs",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	dependent := testNodeB

	graph.Add(ownerA, nil, nil)
	graph.Add(ownerB, nil, nil)

	t.Log("Add dependent with ownerA")
	graph.Add(dependent, nil, []ObjectReference{ownerA})
	assert.Equal(t, 1, len(graph.Owners(dependent)))
	assert.Contains(t, graph.Owners(dependent), ownerA)

	t.Log("Update dependent: move from ownerA to ownerB")
	graph.Add(dependent, []ObjectReference{ownerA}, []ObjectReference{ownerB})

	owners := graph.Owners(dependent)
	assert.Equal(t, 1, len(owners))
	assert.Contains(t, owners, ownerB)

	assert.Equal(t, 0, len(graph.Owned(ownerA)), "ownerA should no longer own dependent")
	assert.Equal(t, 1, len(graph.Owned(ownerB)), "ownerB should now own dependent")
}
