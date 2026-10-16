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
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/identity"
)

type identityInformer struct {
	cache.SharedIndexInformer
	synced bool
}

func (i *identityInformer) HasSynced() bool { return i.synced }

func TestIdentityLookup(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"APIExport", "ClusterCachedResource"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			testIdentityLookup(t, func(name, id string, hashes []string) runtime.Object {
				if kind == "APIExport" {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Status:     apisv1alpha2.APIExportStatus{IdentityHash: id, IdentityKeyHashes: hashes},
					}
				}
				return &cachev1alpha1.ClusterCachedResource{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Status:     cachev1alpha1.ClusterCachedResourceStatus{IdentityHash: id, IdentityKeyHashes: hashes},
				}
			})
		})
	}
}

func testIdentityLookup(t *testing.T, object func(string, string, []string) runtime.Object) {
	t.Helper()
	local := &identityInformer{SharedIndexInformer: cache.NewSharedIndexInformer(&cache.ListWatch{}, object("", "", nil), 0, cache.Indexers{}), synced: true}
	global := &identityInformer{SharedIndexInformer: cache.NewSharedIndexInformer(&cache.ListWatch{}, object("", "", nil), 0, cache.Indexers{}), synced: true}
	lookup, err := NewIdentityLookup(local, global)
	require.NoError(t, err)
	for i, informer := range []*identityInformer{local, global} {
		retired := fmt.Sprintf("retired-initial-key-%d", i)
		require.NoError(t, informer.GetStore().Add(object(retired, retired, []string{"replacement-" + retired})))
		id, hashes, err := identity.Verify("", nil, retired, lookup)
		require.ErrorContains(t, err, "reserved")
		require.Empty(t, id)
		require.Empty(t, hashes)
	}
	require.NoError(t, local.GetStore().Add(object("legacy", "legacy-key", nil)))
	require.NoError(t, global.GetStore().Add(object("rotated", "stable", []string{"new-key"})))
	for key, want := range map[string]string{"legacy-key": "legacy-key", "new-key": "stable", "unknown": ""} {
		id, err := lookup(key)
		require.NoError(t, err)
		require.Equal(t, want, id)
	}
	require.NoError(t, local.GetStore().Add(object("conflict", "different", []string{"new-key"})))
	_, err = lookup("new-key")
	require.ErrorContains(t, err, "conflicting identities")
	global.synced = false
	_, err = lookup("unknown")
	require.ErrorContains(t, err, "has not synced")
}
