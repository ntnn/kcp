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
	"slices"

	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/garbagecollector/syncmap"
)

// Graph is a bidirectional ownership graph tracking owner→dependents and
// dependent→owners relationships.
type Graph struct {
	// ownerToDependents maps an owner's ID to its list of dependent objects.
	//
	// The value is a pointer to a slice because the value must be
	// comparable for the syncmap to be able to do atomic operations on
	// it. That also means that when modifying the slice, a new slice
	// must be created.
	ownerToDependents *syncmap.SyncMap[ID, *[]ObjectReference]

	// dependentToOwners maps a dependent's ID to its list of owner objects.
	dependentToOwners *syncmap.SyncMap[ID, *[]ObjectReference]
}

func NewGraph() *Graph {
	return &Graph{
		ownerToDependents: syncmap.NewSyncMap[ID, *[]ObjectReference](),
		dependentToOwners: syncmap.NewSyncMap[ID, *[]ObjectReference](),
	}
}

// ClusterOwned returns all object references owned by objects in the given cluster.
func (g *Graph) ClusterOwned(clusterName logicalcluster.Name) []ObjectReference {
	var allDependents []ObjectReference
	g.ownerToDependents.Range(func(id ID, deps *[]ObjectReference) bool {
		if id.ClusterName == clusterName && deps != nil {
			allDependents = append(allDependents, *deps...)
		}
		return true
	})
	return allDependents
}

// RemoveCluster removes the given cluster from the graph.
// It returns true if the cluster was removed or not present.
// If returned false the cluster had objects with owned objects, and the owned objects are returned.
func (g *Graph) RemoveCluster(clusterName logicalcluster.Name) (bool, []ObjectReference) {
	owned := g.ClusterOwned(clusterName)
	if len(owned) > 0 {
		return false, owned
	}
	// TODO this function might not be necessary.
	return true, nil
}

// Add adds the given object and updates its owners in the graph.
func (g *Graph) Add(obj ObjectReference, oldOwners, newOwners []ObjectReference) {
	// Ensure the object exists in the ownerToDependents map (it may own
	// things later).
	g.ownerToDependents.StoreIfAbsent(obj.ID(), ptr.To([]ObjectReference{}))

	// TODO diff oldOwners and newOwners to avoid unnecessary
	// modifications.

	// Remove obj from old owners' dependent lists.
	for _, owner := range oldOwners {
		g.ownerToDependents.Modify(owner.ID(), func(deps *[]ObjectReference, exists bool) *[]ObjectReference {
			if !exists || deps == nil {
				deps = ptr.To([]ObjectReference{})
			}
			newDeps := slices.DeleteFunc(*deps, func(dep ObjectReference) bool {
				return dep.Equals(obj)
			})
			return &newDeps
		})
	}

	// Add obj to new owners' dependent lists.
	for _, owner := range newOwners {
		// Ensure the owner exists in the ownerToDependents map.
		g.ownerToDependents.StoreIfAbsent(owner.ID(), ptr.To([]ObjectReference{}))

		g.ownerToDependents.Modify(owner.ID(), func(deps *[]ObjectReference, exists bool) *[]ObjectReference {
			return ptr.To(
				append(
					ptr.Deref(deps, []ObjectReference{}),
					obj,
				),
			)
		})
	}

	// Update the reverse map: dependent→owners.
	g.dependentToOwners.Modify(obj.ID(), func(_ *[]ObjectReference, _ bool) *[]ObjectReference {
		if len(newOwners) == 0 {
			return ptr.To([]ObjectReference{})
		}
		owners := make([]ObjectReference, len(newOwners))
		copy(owners, newOwners)
		return &owners
	})
}

// Owned returns all objects owned by the given object reference.
func (g *Graph) Owned(or ObjectReference) []ObjectReference {
	ownedObjs, exists := g.ownerToDependents.Load(or.ID())
	if !exists || ownedObjs == nil {
		return nil
	}
	return *ownedObjs
}

// Owners returns all owners of the given dependent object reference.
func (g *Graph) Owners(dependent ObjectReference) []ObjectReference {
	owners, exists := g.dependentToOwners.Load(dependent.ID())
	if !exists || owners == nil {
		return nil
	}
	return *owners
}

// Remove removes the given object from the graph.
// It returns true if the object was removed or is not present.
// If the object owns objects it returns false and the owned objects.
func (g *Graph) Remove(or ObjectReference) (bool, []ObjectReference) {
	ownedObjs, exists := g.ownerToDependents.Load(or.ID())
	if !exists {
		return true, nil
	}
	if ownedObjs != nil && len(*ownedObjs) > 0 {
		return false, *ownedObjs
	}

	// Delete the object from the ownerToDependents map.
	g.ownerToDependents.Delete(or.ID())

	// Delete from dependentToOwners map.
	g.dependentToOwners.Delete(or.ID())

	// Remove the object from all owners' dependent lists.
	g.ownerToDependents.Range(func(ownerID ID, ownedObjs *[]ObjectReference) bool {
		g.ownerToDependents.Modify(ownerID, func(ownedObjs *[]ObjectReference, _ bool) *[]ObjectReference {
			if ownedObjs == nil {
				return ownedObjs
			}
			newOwnedObjs := slices.DeleteFunc(*ownedObjs, func(ownedObj ObjectReference) bool {
				return ownedObj.ID() == or.ID()
			})
			if len(newOwnedObjs) == len(*ownedObjs) {
				return ownedObjs
			}
			return &newOwnedObjs
		})
		return true
	})

	// Remove the object from all dependents' owner lists.
	g.dependentToOwners.Range(func(depID ID, owners *[]ObjectReference) bool {
		g.dependentToOwners.Modify(depID, func(owners *[]ObjectReference, _ bool) *[]ObjectReference {
			if owners == nil {
				return owners
			}
			newOwners := slices.DeleteFunc(*owners, func(owner ObjectReference) bool {
				return owner.ID() == or.ID()
			})
			if len(newOwners) == len(*owners) {
				return owners
			}
			return &newOwners
		})
		return true
	})

	return true, nil
}
