/*
Copyright 2026 The KCP Authors.

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

package indexers

import (
	"fmt"

	"k8s.io/client-go/tools/cache"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

const (
	APIExportByIdentityKeyHash             = "IdentityByKeyHash"
	ClusterCachedResourceByIdentityKeyHash = "IdentityByKeyHash"
	IdentityByStableID                     = "IdentityByStableID"
)

// IdentityReservedError prevents an unaccepted initial key from reclaiming an ID.
type IdentityReservedError struct {
	Identity string
}

func (e *IdentityReservedError) Error() string {
	return fmt.Sprintf("stable identity %q is reserved and does not accept this key", e.Identity)
}

// IndexByIdentityKeyHash indexes accepted keys, with a fallback for legacy IDs.
func IndexByIdentityKeyHash(obj interface{}) ([]string, error) {
	id, hashes, err := identityStatus(obj)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	if len(hashes) == 0 {
		return []string{id}, nil
	}
	return hashes, nil
}

func identityStatus(obj interface{}) (string, []string, error) {
	switch obj := obj.(type) {
	case *apisv1alpha2.APIExport:
		return obj.Status.IdentityHash, obj.Status.IdentityKeyHashes, nil
	case *cachev1alpha1.ClusterCachedResource:
		return obj.Status.IdentityHash, obj.Status.IdentityKeyHashes, nil
	default:
		return "", nil, fmt.Errorf("unsupported identity object %T", obj)
	}
}

// NewIdentityLookup searches local and global identity informers. It refuses
// ambiguous identities, reserved fallback IDs, and unsynchronized caches.
func NewIdentityLookup(informers ...cache.SharedIndexInformer) (func(string) (string, error), error) {
	for _, informer := range informers {
		needed := cache.Indexers{}
		existing := informer.GetIndexer().GetIndexers()
		if _, found := existing[APIExportByIdentityKeyHash]; !found {
			needed[APIExportByIdentityKeyHash] = IndexByIdentityKeyHash
		}
		if _, found := existing[IdentityByStableID]; !found {
			needed[IdentityByStableID] = func(obj interface{}) ([]string, error) {
				id, _, err := identityStatus(obj)
				if err != nil || id == "" {
					return nil, err
				}
				return []string{id}, nil
			}
		}
		if len(needed) > 0 {
			if err := informer.AddIndexers(needed); err != nil {
				return nil, err
			}
		}
	}
	return func(hash string) (string, error) {
		var result string
		for _, informer := range informers {
			if !informer.HasSynced() {
				return "", fmt.Errorf("identity informer has not synced")
			}
			objects, err := informer.GetIndexer().ByIndex(APIExportByIdentityKeyHash, hash)
			if err != nil {
				return "", err
			}
			for _, obj := range objects {
				id, _, err := identityStatus(obj)
				if err != nil {
					return "", err
				}
				if result != "" && result != id {
					return "", fmt.Errorf("conflicting identities %q and %q for key hash %q", result, id, hash)
				}
				result = id
			}
		}
		if result == "" {
			// A deterministic fallback must not re-create an identity whose
			// initial key was retired. Key membership and ID reservation are
			// separate indexes, since retiring a key never retires the ID.
			// TODO: enforce authoritative cross-shard ID reservations so
			// informer lag and simultaneous creation cannot bypass this check.
			for _, informer := range informers {
				reserved, err := informer.GetIndexer().ByIndex(IdentityByStableID, hash)
				if err != nil {
					return "", err
				}
				if len(reserved) > 0 {
					return "", &IdentityReservedError{Identity: hash}
				}
			}
		}
		return result, nil
	}, nil
}
