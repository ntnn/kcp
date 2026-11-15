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

// Package logicalroot admits LogicalRoots.
package logicalroot

import (
	"context"
	"errors"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	forestv1alpha1 "github.com/kcp-dev/sdk/apis/forest/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	apibindingadmission "github.com/kcp-dev/kcp/pkg/admission/apibinding"
	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	// PluginName is the name of the LogicalRoot admission plugin.
	PluginName = "forest.tenancy.kcp.io/LogicalRoot"
)

// Register registers the LogicalRoot admission plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			plugin := &logicalRoot{
				Handler: admission.NewHandler(admission.Create),
				customNamesEnabled: func() bool {
					return kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.LogicalRootAllowCustomNames)
				},
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			plugin.getType = func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
				return indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), plugin.typeIndexer, plugin.globalTypeIndexer, path, name)
			}
			plugin.transitiveTypeResolver = workspacetypeexists.NewTransitiveTypeResolver(plugin.getType)
			plugin.getLogicalCluster = func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return indexers.ByPathAndNameWithFallback[*corev1alpha1.LogicalCluster](corev1alpha1.Resource("logicalclusters"), plugin.logicalClusterIndexer, plugin.cacheLogicalClusterIndexer, path, corev1alpha1.LogicalClusterName)
			}

			return plugin, nil
		})
}

// logicalRoot does the following on creation of a LogicalRoot
//   - it defaults the WorkspaceType to the root type in the root workspace,
//   - it checks that the WorkspaceType exists, may be used and allows no parent,
//   - it rejects a root path while the LogicalRootAllowCustomNames feature gate is off.
type logicalRoot struct {
	*admission.Handler

	// customNamesEnabled is a field to set the feature gate in tests.
	customNamesEnabled func() bool

	getType func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)

	typeIndexer       cache.Indexer
	globalTypeIndexer cache.Indexer

	getLogicalCluster          func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
	logicalClusterIndexer      cache.Indexer
	cacheLogicalClusterIndexer cache.Indexer

	transitiveTypeResolver workspacetypeexists.TransitiveTypeResolver

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.MutationInterface(&logicalRoot{})
	_ = admission.ValidationInterface(&logicalRoot{})
	_ = admission.InitializationValidator(&logicalRoot{})
	_ = kcpinitializers.WantsKcpInformers(&logicalRoot{})
	_ = kcpinitializers.WantsDeepSARClient(&logicalRoot{})
)

// Admit defaults the WorkspaceType and completes its path.
func (o *logicalRoot) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if !isLogicalRoot(a) {
		return nil
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	root := &forestv1alpha1.LogicalRoot{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, root); err != nil {
		return fmt.Errorf("failed to convert unstructured to LogicalRoot: %w", err)
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, errors.New("not yet ready to handle request"))
	}

	if root.Spec.Type == nil || root.Spec.Type.Name == "" {
		root.Spec.Type = &tenancyv1alpha1.WorkspaceTypeReference{
			Name: tenancyv1alpha1.RootWorkspaceTypeName,
			Path: core.RootCluster.String(),
		}
	}

	if root.Spec.Type.Path == "" {
		logicalCluster, err := o.getLogicalCluster(clusterName.Path())
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}
		thisPath := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]
		if thisPath == "" {
			thisPath = clusterName.Path().String()
		}

		wt, err := workspacetypeexists.ResolveWorkspaceTypeReference(logicalcluster.NewPath(thisPath), *root.Spec.Type, o.getType)
		if err != nil {
			return admission.NewForbidden(a, err)
		}
		root.Spec.Type.Path = wt.Annotations[core.LogicalClusterPathAnnotationKey]
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(root)
	if err != nil {
		return fmt.Errorf("failed to convert LogicalRoot to unstructured: %w", err)
	}
	u.Object = raw

	return nil
}

// Validate ensures that
// - the root path is only set with the LogicalRootAllowCustomNames feature gate,
// - the WorkspaceType exists, allows no parent and may be used by the user.
func (o *logicalRoot) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if !isLogicalRoot(a) {
		return nil
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	root := &forestv1alpha1.LogicalRoot{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, root); err != nil {
		return fmt.Errorf("failed to convert unstructured to LogicalRoot: %w", err)
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, errors.New("not yet ready to handle request"))
	}

	if root.Spec.RootPath != "" && !o.customNamesEnabled() {
		return admission.NewForbidden(a, fmt.Errorf("spec.rootPath requires the %s feature gate", kcpfeatures.LogicalRootAllowCustomNames))
	}

	if root.Spec.Type == nil || root.Spec.Type.Name == "" {
		return admission.NewForbidden(a, errors.New("spec.type must be set"))
	}
	if root.Spec.Type.Path == "" {
		return admission.NewForbidden(a, errors.New("spec.type.path must be set"))
	}

	wt, err := workspacetypeexists.ResolveWorkspaceTypeReference(clusterName.Path(), *root.Spec.Type, o.getType)
	if err != nil {
		return admission.NewForbidden(a, err)
	}
	if wt.Spec.LimitAllowedParents == nil || !wt.Spec.LimitAllowedParents.None {
		return admission.NewForbidden(a, fmt.Errorf("workspace type %s:%s must set limitAllowedParents.none to be used by a LogicalRoot", root.Spec.Type.Path, root.Spec.Type.Name))
	}

	wtAliases, err := o.transitiveTypeResolver.Resolve(wt)
	if err != nil {
		return admission.NewForbidden(a, err)
	}

	newAuthorizer := func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
		return o.createAuthorizer(clusterName, o.deepSARClient, delegated.Options{})
	}

	for _, alias := range wtAliases {
		authz, err := newAuthorizer(logicalcluster.From(alias))
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("unable to determine access to workspace type %s:%s", root.Spec.Type.Path, root.Spec.Type.Name))
		}

		if err := workspacetypeexists.AuthorizeUse(ctx, authz, a.GetUserInfo(), alias); err != nil {
			return admission.NewForbidden(a, err)
		}

		if err := apibindingadmission.CheckDefaultAPIBindingsAccess(ctx, a.GetUserInfo(), logicalcluster.From(alias), alias.Spec.DefaultAPIBindings, o.getLogicalCluster, newAuthorizer); err != nil {
			// Do not leak the export path when creating a LogicalRoot.
			if notPermitted, ok := errors.AsType[*apibindingadmission.DefaultAPIBindingAccessError](err); ok {
				notPermitted.ExportPath = ""
			}
			return admission.NewForbidden(a, fmt.Errorf("unable to create LogicalRoot %q: %w", root.Name, err))
		}
	}

	return nil
}

func (o *logicalRoot) ValidateInitialization() error {
	if o.typeIndexer == nil {
		return errors.New(PluginName + " plugin needs a WorkspaceType indexer")
	}
	if o.globalTypeIndexer == nil {
		return errors.New(PluginName + " plugin needs a global WorkspaceType indexer")
	}
	if o.logicalClusterIndexer == nil {
		return errors.New(PluginName + " plugin needs a LogicalCluster indexer")
	}
	if o.cacheLogicalClusterIndexer == nil {
		return errors.New(PluginName + " plugin needs a global LogicalCluster indexer")
	}
	if o.deepSARClient == nil {
		return errors.New(PluginName + " plugin needs a deep SAR client")
	}
	return nil
}

func (o *logicalRoot) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	localTypesReady := local.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced
	globalTypesReady := global.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced

	logicalClusterReady := local.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	cacheLogicalClusterReady := global.Core().V1alpha1().LogicalClusters().Informer().HasSynced

	o.SetReadyFunc(func() bool {
		return localTypesReady() && globalTypesReady() && logicalClusterReady() && cacheLogicalClusterReady()
	})

	o.typeIndexer = local.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()
	o.globalTypeIndexer = global.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()

	o.logicalClusterIndexer = local.Core().V1alpha1().LogicalClusters().Informer().GetIndexer()
	o.cacheLogicalClusterIndexer = global.Core().V1alpha1().LogicalClusters().Informer().GetIndexer()

	for _, indexer := range []cache.Indexer{o.typeIndexer, o.globalTypeIndexer, o.logicalClusterIndexer, o.cacheLogicalClusterIndexer} {
		indexers.AddIfNotPresentOrDie(indexer, cache.Indexers{
			indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		})
	}
}

func (o *logicalRoot) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

func isLogicalRoot(a admission.Attributes) bool {
	return a.GetResource().GroupResource() == forestv1alpha1.Resource("logicalroots") && a.GetSubresource() == ""
}
