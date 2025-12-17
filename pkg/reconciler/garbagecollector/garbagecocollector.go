/*
Copyright 2022 The KCP Authors.

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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	kcpapiextensionsv1 "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadataclient "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

// TODO replace with kcp-garbage-collector once stabilising.
const NewControllerName = "kcp-native-garbage-collector"

type Options struct {
	LogicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer
	CRDInformer            kcpapiextensionsv1.CustomResourceDefinitionClusterInformer
	DynRESTMapper          *dynamicrestmapper.DynamicRESTMapper
	Logger                 logr.Logger
	KubeClusterClient      kcpkubernetesclient.ClusterInterface
	MetadataClusterClient  kcpmetadataclient.ClusterInterface
	SharedInformerFactory  *informer.DiscoveringDynamicSharedInformerFactory

	DeletionWorkers int
}

type GarbageCollector struct {
	options Options

	log logr.Logger

	graph *Graph

	// TODO(ntnn): replace with a better structure to manage monitors
	// will probably need cluster->group->version->resource->registration
	monitors map[schema.GroupVersionResource]func()

	deletionQueue workqueue.TypedRateLimitingInterface[ObjectReference]
}

func NewGarbageCollector(options Options) *GarbageCollector {
	gc := &GarbageCollector{}

	gc.options = options
	if gc.options.DeletionWorkers <= 0 {
		gc.options.DeletionWorkers = 2
	}

	gc.log = logging.WithReconciler(options.Logger, NewControllerName)

	gc.graph = NewGraph()
	gc.monitors = make(map[schema.GroupVersionResource]func())
	gc.deletionQueue = workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[ObjectReference](),
		workqueue.TypedRateLimitingQueueConfig[ObjectReference]{
			Name: ControllerName,
		},
	)

	return gc
}

func (gc *GarbageCollector) Start(ctx context.Context) {
	// TODO(ntnn): Handle sharding. The GC of a shard should only care
	// about the logical clusters assigned to it.

	// // TODO(ntnn): Could probably noop add and update. Only removal is
	// // really interesting for garbage collection to delete dependent
	// // resources in other clusters.
	// lcHandlers := cache.ResourceEventHandlerFuncs{
	// 	AddFunc: func(obj interface{}) {
	// 		gc.handleLogicalClusterAdd(tombstone.Obj[*corev1alpha1.LogicalCluster](obj))
	// 	},
	// 	UpdateFunc: func(oldObj, newObj interface{}) {
	// 		// TODO implement garbage collection logic on LogicalCluster update
	// 	},
	// 	DeleteFunc: func(obj interface{}) {
	// 		gc.handleLogicalClusterRemove(tombstone.Obj[*corev1alpha1.LogicalCluster](obj))
	// 	},
	// }
	//
	// lcRegistration, err := gc.options.LogicalClusterInformer.Informer().AddEventHandler(lcHandlers)
	// if err != nil {
	// 	return err
	// }
	// defer gc.options.LogicalClusterInformer.Informer().RemoveEventHandler(lcRegistration)

	// A single crd handler will handle all resources as the "default"
	// kube resources are coming from CRDs in the system:system-crds
	// logical cluster and will be watched automatically through this
	// handler.
	// The CRD informer in turn manages the handlers that will watch the
	// resources across all clusters and feed changes into the graph and
	// deletion queue.
	crdHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gc.updateMonitors(
				&apiextensionsv1.CustomResourceDefinition{},
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj),
			)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			gc.updateMonitors(
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](oldObj),
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](newObj),
			)
		},
		DeleteFunc: func(obj interface{}) {
			gc.updateMonitors(
				tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj),
				&apiextensionsv1.CustomResourceDefinition{},
			)
		},
	}

	// TODO(ntnn): this is ugly. maybe pass in crdinformer.apiextensions().v1()....
	crdRegistration, err := gc.options.CRDInformer.Informer().AddEventHandler(crdHandlers)
	if err != nil {
		gc.log.Error(err, "error adding event handler for CRDs")
		return
	}
	defer func() {
		if err := gc.options.CRDInformer.Informer().RemoveEventHandler(crdRegistration); err != nil {
			gc.log.Error(err, "error removing event handler for CRDs")
		}
	}()

	for range gc.options.DeletionWorkers {
		go wait.UntilWithContext(ctx, gc.runDeletionQueueWorker, time.Second)
	}

	<-ctx.Done()
}

// func (gc *GarbageCollector) handleLogicalClusterAdd(lc *corev1alpha1.LogicalCluster) {
// 	// TODO(ntnn) unsure if logicalcluster.From is correct here. the
// 	// name of the lc should already be correct, but .From looks into
// 	// the annotation.
// 	// if err := gc.graph.AddCluster(logicalcluster.From(lc)); err != nil {
// 	// 	gc.log.Error(err, "error adding cluster to graph", "cluster", lc.Name)
// 	// 	return
// 	// }
// }
//
// func (gc *GarbageCollector) handleLogicalClusterRemove(lc *corev1alpha1.LogicalCluster) {
// 	// TODO(ntnn): as for handleLogicalClusterAdd
// 	deleted, _ := gc.graph.RemoveCluster(logicalcluster.From(lc))
// 	if deleted {
// 		return
// 	}
// 	// TODO delete deps
// }

func (gc *GarbageCollector) updateMonitors(oldCrd, newCrd *apiextensionsv1.CustomResourceDefinition) {
	// Determine added and removed versions.
	oldVersions := map[schema.GroupVersionResource]bool{}
	for _, version := range oldCrd.Spec.Versions {
		if !version.Served {
			continue
		}
		gvr := schema.GroupVersionResource{
			Group:    oldCrd.Spec.Group,
			Version:  version.Name,
			Resource: oldCrd.Spec.Names.Plural,
		}
		oldVersions[gvr] = true
	}

	newVersions := map[schema.GroupVersionResource]bool{}
	for _, version := range newCrd.Spec.Versions {
		if !version.Served {
			continue
		}
		gvr := schema.GroupVersionResource{
			Group:    newCrd.Spec.Group,
			Version:  version.Name,
			Resource: newCrd.Spec.Names.Plural,
		}
		if _, exists := oldVersions[gvr]; !exists {
			newVersions[gvr] = true
		}
	}

	// Stop monitors for removed versions.
	for gvr := range oldVersions {
		if _, exists := newVersions[gvr]; exists {
			continue
		}
		cancel, ok := gc.monitors[gvr]
		if !ok {
			// Version was never monitored.
			continue
		}
		cancel()
	}

	// Start monitors for added versions.
	for gvr := range newVersions {
		if _, exists := oldVersions[gvr]; exists {
			continue
		}
		cancel, err := gc.startMonitorForVersion(newCrd, gvr)
		if err != nil {
			gc.log.Error(err, "error starting monitor for CRD version", "crd", newCrd.Name, "gvr", gvr)
			continue
		}
		gc.monitors[gvr] = cancel
	}
}

func (gc *GarbageCollector) startMonitorForVersion(crd *apiextensionsv1.CustomResourceDefinition, gvr schema.GroupVersionResource) (func(), error) {
	// Start handler to add/update resources in the graph and to queue
	// deletion.
	// Add and update directly updates the graph.
	// Only deletion needs to be queued to cascade deletions when an
	// object is deleted that owns other objects.
	handlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			u := tombstone.Obj[*unstructured.Unstructured](obj)
			or := ObjectReferenceFrom(u)
			owners := ObjectReferencesFromOwnerReferences(
				logicalcluster.From(u),
				u.GetNamespace(),
				u.GetOwnerReferences(),
			)
			gc.graph.Add(or, nil, owners)
		},
		UpdateFunc: func(oldRaw, newRaw interface{}) {
			oldObj := tombstone.Obj[*unstructured.Unstructured](oldRaw)
			// oldOR := ObjectReferenceFrom(old)
			oldOwners := ObjectReferencesFromOwnerReferences(
				logicalcluster.From(oldObj),
				oldObj.GetNamespace(),
				oldObj.GetOwnerReferences(),
			)

			newObj := tombstone.Obj[*unstructured.Unstructured](newRaw)
			newOR := ObjectReferenceFrom(newObj)
			newOwners := ObjectReferencesFromOwnerReferences(
				logicalcluster.From(newObj),
				newObj.GetNamespace(),
				newObj.GetOwnerReferences(),
			)

			gc.graph.Add(newOR, oldOwners, newOwners)
		},
		DeleteFunc: func(obj interface{}) {
			u := tombstone.Obj[*unstructured.Unstructured](obj)
			or := ObjectReferenceFrom(u)
			gc.deletionQueue.Add(or)
		},
	}

	informer, err := gc.options.SharedInformerFactory.ForResource(gvr)
	if err != nil {
		return nil, fmt.Errorf("error getting informer for GVR %v: %w", gvr, err)
	}

	registration, err := informer.Informer().AddEventHandler(handlers)
	if err != nil {
		return nil, fmt.Errorf("error adding event handler for GVR %v: %w", gvr, err)
	}

	unregister := func() {
		if err := informer.Informer().RemoveEventHandler(registration); err != nil {
			gc.log.Error(err, "error removing event handler for CRD", "crd", crd.Name) // TODO: version etcpp
		}
	}

	return unregister, nil
}

func (gc *GarbageCollector) GVR(or ObjectReference) (schema.GroupVersionResource, error) {
	gvk := schema.FromAPIVersionAndKind(or.OwnerReference.APIVersion, or.OwnerReference.Kind)
	forCluster := gc.options.DynRESTMapper.ForCluster(or.ClusterName)
	mapping, err := forCluster.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}

func (gc *GarbageCollector) runDeletionQueueWorker(ctx context.Context) {
	for gc.processDeletionQueue(ctx) {
	}
}

func (gc *GarbageCollector) processDeletionQueue(ctx context.Context) bool {
	or, shutdown := gc.deletionQueue.Get()
	if shutdown {
		return false
	}
	defer gc.deletionQueue.Done(or)

	requeue, err := gc.processDeletionQueueItem(ctx, or)
	if err != nil {
		gc.log.Error(err, "error processing deletion queue item", "object", or)
		gc.deletionQueue.AddRateLimited(or)
		return true
	}
	if requeue {
		gc.deletionQueue.Add(or)
		return true
	}

	gc.deletionQueue.Forget(or)
	return true
}

func (gc *GarbageCollector) processDeletionQueueItem(ctx context.Context, or ObjectReference) (bool, error) {
	gvr, err := gc.GVR(or)
	if err != nil {
		return false, err
	}

	client := gc.options.MetadataClusterClient.Cluster(or.ClusterName.Path()).
		Resource(gvr).
		Namespace(or.Namespace)

	latest, err := client.Get(ctx, or.Name, metav1.GetOptions{})
	// TODO(ntnn): this is not good. better would probably to pass the
	// unstructured through the queue and to only work off of that data
	// until actual api interaction is necessary.
	// If we get a not found error, the object is already gone but the
	// graph might still have objects for it either because the gc,
	// queue or graph is lagging behind.
	if apierrors.IsNotFound(err) {
		// Object is already gone, remove it from the graph.
		gc.graph.Remove(or)
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if hasFinalizer(latest, metav1.FinalizerOrphanDependents) {
		if err := gc.orphanOwned(ctx, or); err != nil {
			return false, err
		}

		patch, err := patchRemoveFinalizer(latest, metav1.FinalizerOrphanDependents)
		if err != nil {
			return false, err
		}

		if _, err := client.Patch(ctx, or.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return false, err
		}
		// Requeue to continue deletion after orphaning is propagated.
		return true, nil
	}

	// metav1.FinalizerDeleteDependents doesn't need special handling.
	// If owned objects exist they will be added to the deletion queue
	// and this object will be requeued until dependents are gone.

	// Check if there are any owned objects.
	// If the owned objects were orphaned above this will requeue until
	// the informers have caught up and the graph is consistent.
	owned := gc.graph.Owned(or)
	if len(owned) > 0 {
		// There are owned objects. Add them to the deletion queue and then requeue.
		for _, ownedRef := range owned {
			gc.deletionQueue.Add(ownedRef)
		}
		// Requeue the original object to check later if owned objects are gone.
		return true, nil
	}
	// No owned objects. Proceed to delete the object.

	preconditions := metav1.Preconditions{UID: &or.UID}

	// FinalizerOrphanDependents and FinalizerDeleteDependents was
	// handled above, so the policy doesn't matter per se.
	policy := metav1.DeletePropagationBackground

	if err := gc.options.MetadataClusterClient.
		Cluster(or.ClusterName.Path()).
		Resource(gvr).
		Namespace(or.Namespace).
		Delete(ctx, or.Name, metav1.DeleteOptions{
			Preconditions:     &preconditions,
			PropagationPolicy: &policy,
		},
		); err != nil {
		// TODO(ntnn): Could add a handle for not found here, but
		// strictly speaking that should not happen as these workers
		// should be the only ones deleting objects.
		return false, err
	}

	// Object has been successfully deleted from the API server. Remove it from the graph.
	gc.graph.Remove(or)

	return false, nil
}

func (gc *GarbageCollector) orphanOwned(ctx context.Context, or ObjectReference) error {
	owned := gc.graph.Owned(or)
	if len(owned) == 0 {
		return nil
	}

	// Remove owner reference from all owned objects.
	var errs error
	for _, ownedRef := range owned {
		gvr, err := gc.GVR(ownedRef)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		client := gc.options.MetadataClusterClient.
			Cluster(ownedRef.ClusterName.Path()).
			Resource(gvr).
			Namespace(ownedRef.Namespace)

		latest, err := client.Get(ctx, ownedRef.Name, metav1.GetOptions{})
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		patch, err := patchRemoveOwnerReference(latest, or.UID)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		if _, err := client.Patch(ctx, ownedRef.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
	}

	// TODO(ntnn): don't think this should be necessary. the handler
	// should get an event for the update and update the graph so
	// eventually the graph is consistent and deletion can proceed
	// without meddling here.
	// gc.graph.Add(or, owned, nil)
	return errs
}
