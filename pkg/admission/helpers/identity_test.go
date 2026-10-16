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

package helpers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/sdk/apis/apis"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/admission/apiexport"
	"github.com/kcp-dev/kcp/pkg/admission/clustercachedresource"
	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

type identityTestCase struct {
	name    string
	old     runtime.Object
	object  runtime.Object
	caller  user.Info
	create  bool
	wantErr bool
}

type clusterCachedResourceInitializer struct{}

func (clusterCachedResourceInitializer) Initialize(plugin admission.Interface) {
	factory := kcpinformers.NewSharedInformerFactory(nil, 0)
	plugin.(*clustercachedresource.ClusterCachedResourceAdmission).SetKcpInformers(factory, factory)
}

func identityObject(identity string, keys ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "test"},
	}}
	if identity != "" {
		_ = unstructured.SetNestedField(u.Object, identity, "status", "identityHash")
	}
	if keys != nil {
		_ = unstructured.SetNestedStringSlice(u.Object, keys, "status", "identityKeyHashes")
	}
	return u
}

func TestIdentityStatus(t *testing.T) {
	t.Parallel()

	privileged := &user.DefaultInfo{Name: "system:apiserver", Groups: []string{user.SystemPrivilegedGroup}}
	ordinary := &user.DefaultInfo{Name: "alice", Groups: []string{user.AllAuthenticated}}
	serviceAccount := &user.DefaultInfo{Name: "system:serviceaccount:default:controller", Groups: []string{user.AllAuthenticated, "system:serviceaccounts"}}
	baseTests := []identityTestCase{
		{name: "empty create", object: identityObject(""), caller: ordinary, create: true},
		{name: "untrusted initial identity", object: identityObject("victim"), caller: ordinary, create: true, wantErr: true},
		{name: "untrusted initial fingerprints", object: identityObject("", "attacker-key"), caller: ordinary, create: true, wantErr: true},
		{name: "privileged create", object: identityObject("new", "key"), caller: privileged, create: true},
		{name: "untrusted adoption", old: identityObject(""), object: identityObject("victim"), caller: ordinary, wantErr: true},
		{name: "untrusted change", old: identityObject("original"), object: identityObject("victim"), caller: ordinary, wantErr: true},
		{name: "untrusted clear", old: identityObject("original"), object: identityObject(""), caller: ordinary, wantErr: true},
		{name: "untrusted fingerprint addition", old: identityObject("original", "key"), object: identityObject("original", "key", "attacker"), caller: ordinary, wantErr: true},
		{name: "untrusted fingerprint replacement", old: identityObject("original", "key"), object: identityObject("original", "attacker"), caller: ordinary, wantErr: true},
		{name: "untrusted fingerprint removal", old: identityObject("original", "key"), object: identityObject("original"), caller: ordinary, wantErr: true},
		{name: "privileged initialize", old: identityObject(""), object: identityObject("new", "key"), caller: privileged},
		{name: "privileged key rotation", old: identityObject("original", "key"), object: identityObject("original", "new-key"), caller: privileged},
		{name: "privileged key removal", old: identityObject("original", "key"), object: identityObject("original"), caller: privileged},
		{name: "privileged cannot change identity", old: identityObject("original"), object: identityObject("new"), caller: privileged, wantErr: true},
		{name: "privileged cannot clear identity", old: identityObject("original"), object: identityObject(""), caller: privileged, wantErr: true},
		{name: "unchanged identity", old: identityObject("original", "key"), object: identityObject("original", "key"), caller: ordinary},
		{name: "empty fingerprints equivalent to omitted", old: identityObject("original"), object: identityObject("original", []string{}...), caller: ordinary},
		{name: "service account cannot initialize", old: identityObject(""), object: identityObject("new"), caller: serviceAccount, wantErr: true},
		{name: "service account cannot publish keys", old: identityObject("original"), object: identityObject("original", "key"), caller: serviceAccount, wantErr: true},
		{name: "nil caller cannot initialize", old: identityObject(""), object: identityObject("new"), wantErr: true},
		{name: "missing old object", object: identityObject("new"), caller: privileged, wantErr: true},
		{name: "typed old object", old: &metav1.Status{}, object: identityObject("new"), caller: privileged, wantErr: true},
		{name: "typed object", old: identityObject(""), object: &metav1.Status{}, caller: privileged, wantErr: true},
		{name: "nil object", old: identityObject(""), object: (*unstructured.Unstructured)(nil), caller: privileged, wantErr: true},
	}
	tests := make([]identityTestCase, 0, len(baseTests)+7)
	tests = append(tests, baseTests...)
	for name, status := range map[string]map[string]interface{}{
		"invalid identity":            {"identityHash": int64(1)},
		"invalid fingerprint list":    {"identityKeyHashes": "key"},
		"invalid fingerprint element": {"identityKeyHashes": []interface{}{int64(1)}},
	} {
		bad := &unstructured.Unstructured{Object: map[string]interface{}{"status": status}}
		tests = append(tests,
			identityTestCase{name: name, old: identityObject(""), object: bad, caller: privileged, wantErr: true},
			identityTestCase{name: name + " in old object", old: bad, object: identityObject(""), caller: privileged, wantErr: true},
		)
	}
	unrelated := identityObject("original", "key")
	_ = unstructured.SetNestedField(unrelated.Object, "Ready", "status", "phase")
	tests = append(tests, identityTestCase{name: "unrelated status update", old: identityObject("original", "key"), object: unrelated, caller: ordinary})

	plugins := admission.NewPlugins()
	clustercachedresource.Register(plugins)
	cached, err := plugins.InitPlugin(clustercachedresource.PluginName, nil, clusterCachedResourceInitializer{})
	require.NoError(t, err)
	for _, resource := range []struct {
		name   string
		gvr    schema.GroupVersionResource
		gvk    schema.GroupVersionKind
		plugin admission.ValidationInterface
	}{
		{"apiexport-v1alpha1", schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports"}, schema.GroupVersionKind{Group: "apis.kcp.io", Version: "v1alpha1", Kind: "APIExport"}, apiexport.NewAPIExportAdmission(func(apis.GroupResource) bool { return true })},
		{"apiexport-v1alpha2", schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, schema.GroupVersionKind{Group: "apis.kcp.io", Version: "v1alpha2", Kind: "APIExport"}, apiexport.NewAPIExportAdmission(func(apis.GroupResource) bool { return true })},
		{"clustercachedresource-v1alpha1", schema.GroupVersionResource{Group: "cache.kcp.io", Version: "v1alpha1", Resource: "clustercachedresources"}, schema.GroupVersionKind{Group: "cache.kcp.io", Version: "v1alpha1", Kind: "ClusterCachedResource"}, cached.(admission.ValidationInterface)},
	} {
		t.Run(resource.name, func(t *testing.T) {
			t.Parallel()

			require.True(t, resource.plugin.Handles(admission.Update))
			for _, subresource := range []string{"", "status"} {
				for _, tc := range tests {
					t.Run(subresource+"/"+tc.name, func(t *testing.T) {
						t.Parallel()

						op := admission.Update
						if tc.create {
							op = admission.Create
						}
						attr := admission.NewAttributesRecord(tc.object, tc.old, resource.gvk, "", "test", resource.gvr, subresource, op, nil, false, tc.caller)
						err := helpers.ValidateIdentityStatus(attr)
						if tc.wantErr {
							require.True(t, apierrors.IsForbidden(err), "expected forbidden, got %v", err)
						} else {
							require.NoError(t, err)
						}
						// ClusterCachedResource's create-only discovery/uniqueness checks need
						// informers; successful creation is covered by its own tests.
						if tc.create && !tc.wantErr && resource.name == "clustercachedresource-v1alpha1" {
							return
						}

						err = resource.plugin.Validate(context.Background(), attr, nil)
						if tc.wantErr {
							require.True(t, apierrors.IsForbidden(err), "expected forbidden, got %v", err)
						} else {
							require.NoError(t, err)
						}
					})
				}
			}
		})
	}
}

func TestCrossShardRotationIdentityStatus(t *testing.T) {
	t.Parallel()

	caller := &user.DefaultInfo{
		Name:   "shard-controller",
		Groups: []string{user.AllAuthenticated, bootstrap.SystemExternalLogicalClusterAdmin},
	}
	for _, resource := range []struct {
		group, name string
	}{
		{"apis.kcp.io", "apiexports"},
		{"cache.kcp.io", "clustercachedresources"},
	} {
		for _, tc := range []struct {
			name      string
			old, next *unstructured.Unstructured
			allowed   bool
		}{
			{"initialize", identityObject(""), identityObject("victim", "key"), false},
			{"reassign", identityObject("stable", "key"), identityObject("victim", "key"), false},
			{"clear", identityObject("stable", "key"), identityObject(""), false},
			{"publish key", identityObject("stable", "key"), identityObject("stable", "key", "new-key"), resource.name == "apiexports"},
			{"retire key", identityObject("stable", "key", "new-key"), identityObject("stable", "new-key"), resource.name == "apiexports"},
			{"publish before assignment", identityObject(""), identityObject("", "key"), false},
		} {
			t.Run(resource.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				attr := admission.NewAttributesRecord(
					tc.next, tc.old, schema.GroupVersionKind{}, "", "test",
					schema.GroupVersionResource{Group: resource.group, Version: "v1alpha1", Resource: resource.name},
					"status", admission.Update, nil, false, caller,
				)
				err := helpers.ValidateIdentityStatus(attr)
				if tc.allowed {
					require.NoError(t, err)
				} else {
					require.True(t, apierrors.IsForbidden(err), "expected forbidden, got %v", err)
				}
			})
		}
	}
}
