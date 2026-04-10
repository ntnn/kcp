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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/lru"

	"github.com/kcp-dev/logicalcluster/v3"
)

// absentOwnerCacheKey uniquely identifies an owner that has been confirmed
// absent. Namespace is empty for cluster-scoped owners.
type absentOwnerCacheKey struct {
	ClusterName logicalcluster.Name
	UID         types.UID
	Namespace   string
}

// AbsentOwnerCache is an LRU cache of owner references confirmed to not
// exist. It avoids redundant API server GETs for owners known to be gone.
type AbsentOwnerCache struct {
	cache *lru.Cache
}

// NewAbsentOwnerCache creates a new cache with the given capacity.
func NewAbsentOwnerCache(maxEntries int) *AbsentOwnerCache {
	return &AbsentOwnerCache{
		cache: lru.New(maxEntries),
	}
}

// Add records an owner as absent.
func (c *AbsentOwnerCache) Add(key absentOwnerCacheKey) {
	c.cache.Add(key, nil)
}

// Has returns true if the owner has been confirmed absent.
func (c *AbsentOwnerCache) Has(key absentOwnerCacheKey) bool {
	_, found := c.cache.Get(key)
	return found
}
