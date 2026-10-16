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
	APIExportByIdentityKeyHash      = "IdentityByKeyHash"
	CachedResourceByIdentityKeyHash = "IdentityByKeyHash"
)

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
	case *cachev1alpha1.CachedResource:
		return obj.Status.IdentityHash, obj.Status.IdentityKeyHashes, nil
	default:
		return "", nil, fmt.Errorf("unsupported identity object %T", obj)
	}
}

// NewIdentityLookup searches local and global identity informers. It refuses
// ambiguous identities and unsynchronized caches rather than choosing an ID.
func NewIdentityLookup(informers ...cache.SharedIndexInformer) (func(string) (string, error), error) {
	for _, informer := range informers {
		if _, found := informer.GetIndexer().GetIndexers()[APIExportByIdentityKeyHash]; !found {
			if err := informer.AddIndexers(cache.Indexers{APIExportByIdentityKeyHash: IndexByIdentityKeyHash}); err != nil {
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
		return result, nil
	}, nil
}
