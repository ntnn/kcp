/*
Copyright 2026 The kcp Authors.

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

package logicalroot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	forestv1alpha1 "github.com/kcp-dev/sdk/apis/forest/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

// orgCluster is the logical cluster the LogicalRoots are created in, with the path root:org.
const orgCluster = logicalcluster.Name("org-cluster")

func TestLogicalRoot_Admit(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		attr     admission.Attributes
		wantErr  bool
		wantType *tenancyv1alpha1.WorkspaceTypeReference
	}{
		"empty type defaults to root:root": {
			attr:     createAttr(newRoot("", nil)),
			wantType: typeRef("root", "root"),
		},
		"type without name defaults to root:root": {
			attr:     createAttr(newRoot("", typeRef("root:org", ""))),
			wantType: typeRef("root", "root"),
		},
		"explicit type is kept": {
			attr:     createAttr(newRoot("", typeRef("root:org", "tree"))),
			wantType: typeRef("root:org", "tree"),
		},
		"type without path resolves in the own workspace": {
			attr:     createAttr(newRoot("", typeRef("", "tree"))),
			wantType: typeRef("root:org", "tree"),
		},
		"type without path falls back to the root workspace": {
			attr:     createAttr(newRoot("", typeRef("", "root"))),
			wantType: typeRef("root", "root"),
		},
		"unknown type without path is rejected": {
			attr:    createAttr(newRoot("", typeRef("", "unknown"))),
			wantErr: true,
		},
		"status updates are ignored": {
			attr:     statusAttr(newRoot("", nil)),
			wantType: nil,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			plugin := newPlugin(false, nil, nil)
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: orgCluster})

			err := plugin.Admit(ctx, cas.attr, nil)
			if cas.wantErr {
				require.Error(t, err)
				assert.True(t, apierrors.IsForbidden(err), "admission must reject with Forbidden, got: %v", err)
				return
			}
			require.NoError(t, err)

			got := &forestv1alpha1.LogicalRoot{}
			u, ok := cas.attr.GetObject().(*unstructured.Unstructured)
			require.True(t, ok)
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, got))
			assert.Equal(t, cas.wantType, got.Spec.Type)
		})
	}
}

func TestLogicalRoot_Validate(t *testing.T) {
	t.Parallel()

	allowAll := func(authorizer.Attributes) bool { return true }

	cases := map[string]struct {
		attr        admission.Attributes
		customNames bool
		allow       func(attr authorizer.Attributes) bool
		authzErr    error
		wantErr     bool
	}{
		"root type without root path is accepted": {
			attr:  createAttr(newRoot("", typeRef("root", "root"))),
			allow: allowAll,
		},
		"type from another workspace is accepted": {
			attr:  createAttr(newRoot("", typeRef("root:org", "tree"))),
			allow: allowAll,
		},
		"root path with the feature gate is accepted": {
			attr:        createAttr(newRoot("acme", typeRef("root", "root"))),
			customNames: true,
			allow:       allowAll,
		},
		"root path without the feature gate is rejected": {
			attr:    createAttr(newRoot("acme", typeRef("root", "root"))),
			allow:   allowAll,
			wantErr: true,
		},
		"missing type is rejected": {
			attr:    createAttr(newRoot("", nil)),
			allow:   allowAll,
			wantErr: true,
		},
		"type without path is rejected": {
			attr:    createAttr(newRoot("", typeRef("", "root"))),
			allow:   allowAll,
			wantErr: true,
		},
		"unknown type is rejected": {
			attr:    createAttr(newRoot("", typeRef("root", "unknown"))),
			allow:   allowAll,
			wantErr: true,
		},
		"type without limitAllowedParents is rejected": {
			attr:    createAttr(newRoot("", typeRef("root", "universal"))),
			allow:   allowAll,
			wantErr: true,
		},
		"type limiting parents to types is rejected": {
			attr:    createAttr(newRoot("", typeRef("root", "organization"))),
			allow:   allowAll,
			wantErr: true,
		},
		"missing use permission on the type is rejected": {
			attr: createAttr(newRoot("", typeRef("root", "root"))),
			allow: func(attr authorizer.Attributes) bool {
				return attr.GetVerb() != "use" || attr.GetName() != "root"
			},
			wantErr: true,
		},
		"missing use permission on an extended type is rejected": {
			attr: createAttr(newRoot("", typeRef("root", "root"))),
			allow: func(attr authorizer.Attributes) bool {
				return attr.GetVerb() != "use" || attr.GetName() != "universal"
			},
			wantErr: true,
		},
		"missing bind permission on a default APIBinding is rejected": {
			attr: createAttr(newRoot("", typeRef("root", "root"))),
			allow: func(attr authorizer.Attributes) bool {
				return attr.GetVerb() != "bind"
			},
			wantErr: true,
		},
		"authorizer error is rejected": {
			attr:     createAttr(newRoot("", typeRef("root", "root"))),
			allow:    allowAll,
			authzErr: errors.New("authorizer down"),
			wantErr:  true,
		},
		"status updates are ignored": {
			attr:  statusAttr(newRoot("acme", nil)),
			allow: allowAll,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			plugin := newPlugin(cas.customNames, cas.allow, cas.authzErr)
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: orgCluster})

			err := plugin.Validate(ctx, cas.attr, nil)
			if cas.wantErr {
				require.Error(t, err)
				assert.True(t, apierrors.IsForbidden(err), "admission must reject with Forbidden, got: %v", err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// newPlugin returns the plugin over a fixed set of WorkspaceTypes.
// allow decides every authorization request unless authzErr is set.
func newPlugin(customNames bool, allow func(attr authorizer.Attributes) bool, authzErr error) *logicalRoot {
	types := []*tenancyv1alpha1.WorkspaceType{
		newType("root", "universal", func(wt *tenancyv1alpha1.WorkspaceType) {
			wt.Spec.DefaultAPIBindings = []tenancyv1alpha1.APIExportReference{
				{
					Path:   "root",
					Export: "tenancy.kcp.io",
				},
			}
		}),
		newType("root", "root", func(wt *tenancyv1alpha1.WorkspaceType) {
			wt.Spec.LimitAllowedParents = &tenancyv1alpha1.WorkspaceTypeSelector{None: true}
			wt.Spec.Extend.With = []tenancyv1alpha1.WorkspaceTypeReference{*typeRef("root", "universal")}
		}),
		newType("root", "organization", func(wt *tenancyv1alpha1.WorkspaceType) {
			wt.Spec.LimitAllowedParents = &tenancyv1alpha1.WorkspaceTypeSelector{
				Types: []tenancyv1alpha1.WorkspaceTypeReference{*typeRef("root", "root")},
			}
		}),
		newType("root:org", "tree", func(wt *tenancyv1alpha1.WorkspaceType) {
			wt.Spec.LimitAllowedParents = &tenancyv1alpha1.WorkspaceTypeSelector{None: true}
		}),
	}

	getType := func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
		for _, wt := range types {
			if wt.Annotations[core.LogicalClusterPathAnnotationKey] == path.String() && wt.Name == name {
				return wt, nil
			}
		}
		return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetypes"), name)
	}

	plugin := &logicalRoot{
		Handler:                admission.NewHandler(admission.Create),
		customNamesEnabled:     func() bool { return customNames },
		getType:                getType,
		transitiveTypeResolver: workspacetypeexists.NewTransitiveTypeResolver(getType),
		getLogicalCluster: func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
			if path != orgCluster.Path() {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), path.String())
			}
			return &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: corev1alpha1.LogicalClusterName,
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:         orgCluster.String(),
						core.LogicalClusterPathAnnotationKey: "root:org",
					},
				},
			}, nil
		},
		createAuthorizer: func(logicalcluster.Name, kcpkubernetesclientset.ClusterInterface, delegated.Options) (authorizer.Authorizer, error) {
			return authorizer.AuthorizerFunc(func(_ context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
				if authzErr != nil {
					return authorizer.DecisionNoOpinion, "", authzErr
				}
				if allow(attr) {
					return authorizer.DecisionAllow, "", nil
				}
				return authorizer.DecisionNoOpinion, "denied by test", nil
			}), nil
		},
	}

	return plugin
}

func newType(path, name string, mutate func(wt *tenancyv1alpha1.WorkspaceType)) *tenancyv1alpha1.WorkspaceType {
	wt := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         path,
				core.LogicalClusterPathAnnotationKey: path,
			},
		},
	}
	mutate(wt)
	return wt
}

func typeRef(path, name string) *tenancyv1alpha1.WorkspaceTypeReference {
	return &tenancyv1alpha1.WorkspaceTypeReference{
		Name: tenancyv1alpha1.WorkspaceTypeName(name),
		Path: path,
	}
}

func newRoot(rootPath string, typ *tenancyv1alpha1.WorkspaceTypeReference) *forestv1alpha1.LogicalRoot {
	return &forestv1alpha1.LogicalRoot{
		TypeMeta: metav1.TypeMeta{
			APIVersion: forestv1alpha1.SchemeGroupVersion.String(),
			Kind:       "LogicalRoot",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "tree"},
		Spec: forestv1alpha1.LogicalRootSpec{
			RootPath: rootPath,
			Type:     typ,
		},
	}
}

func createAttr(root *forestv1alpha1.LogicalRoot) admission.Attributes {
	return newAttr(root, "")
}

func statusAttr(root *forestv1alpha1.LogicalRoot) admission.Attributes {
	return newAttr(root, "status")
}

func newAttr(root *forestv1alpha1.LogicalRoot, subresource string) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(root),
		nil,
		forestv1alpha1.Kind("LogicalRoot").WithVersion("v1alpha1"),
		"",
		root.Name,
		forestv1alpha1.Resource("logicalroots").WithVersion("v1alpha1"),
		subresource,
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}
