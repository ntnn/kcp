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
	"reflect"
	"sync"
	"time"

	"github.com/go-logr/logr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
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
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

// TODO replace with kcp-garbage-collector once stabilising.
const NewControllerName = "kcp-native-garbage-collector"

var errNamespacedOwnerOfClusterScopedObject = fmt.Errorf("cluster-scoped object has namespaced owner reference")

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

// monitorEntry tracks a running monitor and supports deferred removal.
type monitorEntry struct {
	cancel func()

	// pendingRemoval is non-nil when a CRD delete event has been
	// received but the monitor has not been stopped yet. A timer fires
	// to confirm the removal.
	mu             sync.Mutex
	pendingRemoval *time.Timer
}

func (m *monitorEntry) markPendingRemoval(timer *time.Timer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingRemoval = timer
}

func (m *monitorEntry) cancelPendingRemoval() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingRemoval != nil {
		stopped := m.pendingRemoval.Stop()
		m.pendingRemoval = nil
		return stopped
	}
	return false
}

type GarbageCollector struct {
	options Options

	log logr.Logger

	graph *Graph

	absentOwnerCache *AbsentOwnerCache

	// monitors tracks running informer registrations per GVR.
	monitors map[schema.GroupVersionResource]*monitorEntry

	// deletionQueue receives objects from informer delete events.
	// The worker cascades to dependents via attemptToDelete.
	deletionQueue workqueue.TypedRateLimitingInterface[ObjectReference]

	// attemptToDelete receives dependents that need their ownerRefs
	// checked. The worker classifies each ownerRef and decides whether
	// to delete, patch, or skip the dependent.
	attemptToDelete workqueue.TypedRateLimitingInterface[ObjectReference]
}

func NewGarbageCollector(options Options) *GarbageCollector {
	gc := &GarbageCollector{}

	gc.options = options
	if gc.options.DeletionWorkers <= 0 {
		gc.options.DeletionWorkers = 1
	}

	gc.log = logging.WithReconciler(options.Logger, NewControllerName)

	gc.graph = NewGraph()
	gc.absentOwnerCache = NewAbsentOwnerCache(500)
	gc.monitors = make(map[schema.GroupVersionResource]*monitorEntry)
	gc.deletionQueue = workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[ObjectReference](),
		workqueue.TypedRateLimitingQueueConfig[ObjectReference]{
			Name: ControllerName + "-deletion",
		},
	)
	gc.attemptToDelete = workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[ObjectReference](),
		workqueue.TypedRateLimitingQueueConfig[ObjectReference]{
			Name: ControllerName + "-attemptToDelete",
		},
	)

	return gc
}

func (gc *GarbageCollector) Start(ctx context.Context) {
	// Start monitors for builtin APIs.
	builtinCRDs := []*apiextensionsv1.CustomResourceDefinition{}
	for _, builtInAPI := range builtin.BuiltInAPIs {
		builtinCRDs = append(builtinCRDs, &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: builtInAPI.GroupVersion.Group,
				Names: builtInAPI.Names,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:   builtInAPI.GroupVersion.Version,
						Served: true,
					},
				},
			},
		})
	}
	for _, crd := range builtinCRDs {
		gc.updateMonitors(&apiextensionsv1.CustomResourceDefinition{}, crd)
	}

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
		go wait.UntilWithContext(ctx, gc.runAttemptToDeleteWorker, time.Second)
	}

	<-ctx.Done()
}

// ---------------------------------------------------------------------------
// Monitor lifecycle
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) updateMonitors(oldCrd, newCrd *apiextensionsv1.CustomResourceDefinition) {
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

	// Stop monitors for removed versions — defer removal to avoid
	// premature cancellation during transient discovery failures.
	for gvr := range oldVersions {
		if _, exists := newVersions[gvr]; exists {
			continue
		}
		entry, ok := gc.monitors[gvr]
		if !ok {
			continue
		}
		gc.deferMonitorRemoval(gvr, entry)
	}

	// Start monitors for added versions.
	for gvr := range newVersions {
		if _, exists := oldVersions[gvr]; exists {
			continue
		}
		// If there's a pending removal for this GVR, cancel it.
		if existing, ok := gc.monitors[gvr]; ok {
			existing.cancelPendingRemoval()
			continue
		}
		cancel, err := gc.startMonitorForVersion(newCrd, gvr)
		if err != nil {
			gc.log.Error(err, "error starting monitor for CRD version", "crd", newCrd.Name, "gvr", gvr)
			continue
		}
		gc.monitors[gvr] = &monitorEntry{cancel: cancel}
	}
}

const monitorRemovalGracePeriod = 30 * time.Second

// deferMonitorRemoval schedules removal of a monitor after a grace period.
// If the CRD reappears before the timer fires, the removal is cancelled.
func (gc *GarbageCollector) deferMonitorRemoval(gvr schema.GroupVersionResource, entry *monitorEntry) {
	timer := time.AfterFunc(monitorRemovalGracePeriod, func() {
		entry.mu.Lock()
		isPending := entry.pendingRemoval != nil
		entry.pendingRemoval = nil
		entry.mu.Unlock()
		if isPending {
			gc.log.V(4).Info("Removing monitor after grace period", "gvr", gvr)
			entry.cancel()
			delete(gc.monitors, gvr)
		}
	})
	entry.markPendingRemoval(timer)
}

// ---------------------------------------------------------------------------
// Informer event handlers
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) anyToRef(gvr schema.GroupVersionResource, obj any) (*unstructured.Unstructured, ObjectReference) {
	u := tombstone.Obj[*unstructured.Unstructured](obj)
	ref := ObjectReferenceFrom(u)

	switch {
	case u.GetKind() == "PartialObjectMetadata":
		clusterName := logicalcluster.From(u)
		gvk, err := gc.options.DynRESTMapper.ForCluster(clusterName).KindFor(gvr)
		if err != nil {
			gc.log.Error(err, "error getting GVK for GVR", "gvr", gvr, "cluster", clusterName)
			return nil, ObjectReference{}
		}
		ref.APIVersion = gvk.GroupVersion().String()
		ref.Kind = gvk.Kind
	}

	return u, ref
}

func (gc *GarbageCollector) startMonitorForVersion(crd *apiextensionsv1.CustomResourceDefinition, gvr schema.GroupVersionResource) (func(), error) {
	handlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			u, or := gc.anyToRef(gvr, obj)
			if u == nil {
				return
			}
			owners := ObjectReferencesFromOwnerReferences(
				or.ClusterName,
				u.GetNamespace(),
				u.GetOwnerReferences(),
			)
			gc.log.V(4).Info("Adding object to graph", "object", or, "owners", owners)
			gc.graph.Add(or, nil, owners)

			// If the object is already being deleted with
			// FinalizerDeleteDependents, enqueue it for processing.
			if u.GetDeletionTimestamp() != nil {
				finalizers := u.GetFinalizers()
				for _, f := range finalizers {
					if f == metav1.FinalizerDeleteDependents {
						gc.attemptToDelete.Add(or)
						break
					}
				}
			}

			// If owners aren't yet in the graph, enqueue the
			// dependent so isDangling can verify via the API server.
			for _, owner := range owners {
				if gc.graph.Owned(owner) == nil {
					gc.attemptToDelete.Add(or)
					break
				}
			}
		},
		UpdateFunc: func(oldRaw, newRaw interface{}) {
			oldObj, oldRef := gc.anyToRef(gvr, oldRaw)
			if oldObj == nil {
				return
			}
			oldOwners := ObjectReferencesFromOwnerReferences(
				oldRef.ClusterName,
				oldObj.GetNamespace(),
				oldObj.GetOwnerReferences(),
			)

			newObj, newRef := gc.anyToRef(gvr, newRaw)
			if newObj == nil {
				return
			}
			newOwners := ObjectReferencesFromOwnerReferences(
				newRef.ClusterName,
				newObj.GetNamespace(),
				newObj.GetOwnerReferences(),
			)

			gc.log.V(4).Info("Updating object in graph", "object", newRef, "oldOwners", oldOwners, "newOwners", newOwners)
			gc.graph.Add(newRef, oldOwners, newOwners)

			// Detect transition to foreground deletion.
			oldDeleting := oldObj.GetDeletionTimestamp() != nil
			newDeleting := newObj.GetDeletionTimestamp() != nil
			if !oldDeleting && newDeleting {
				newFinalizers := newObj.GetFinalizers()
				for _, f := range newFinalizers {
					if f == metav1.FinalizerDeleteDependents || f == metav1.FinalizerOrphanDependents {
						gc.attemptToDelete.Add(newRef)
						// Also enqueue dependents so they can be cascade-deleted.
						for _, dep := range gc.graph.Owned(newRef) {
							gc.attemptToDelete.Add(dep)
						}
						break
					}
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			_, ref := gc.anyToRef(gvr, obj)
			gc.log.V(4).Info("Queuing object for deletion", "object", ref)
			gc.deletionQueue.Add(ref)

			// Enqueue all dependents so their ownerRefs are checked.
			for _, dep := range gc.graph.Owned(ref) {
				gc.attemptToDelete.Add(dep)
			}
		},
	}

	informer, err := gc.options.SharedInformerFactory.ForResource(gvr)
	if err != nil {
		return nil, fmt.Errorf("error getting informer for GVR %v: %w", gvr, err)
	}

	gc.log.Info("Starting monitor for CRD version", "crd", crd.Name, "gvr", gvr)
	registration, err := informer.Informer().AddEventHandler(handlers)
	if err != nil {
		return nil, fmt.Errorf("error adding event handler for GVR %v: %w", gvr, err)
	}

	unregister := func() {
		if err := informer.Informer().RemoveEventHandler(registration); err != nil {
			gc.log.Error(err, "error removing event handler for CRD", "crd", crd.Name)
		}
	}

	return unregister, nil
}

// ---------------------------------------------------------------------------
// REST mapping helpers
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) GVR(or ObjectReference) (schema.GroupVersionResource, error) {
	gvk := schema.FromAPIVersionAndKind(or.OwnerReference.APIVersion, or.OwnerReference.Kind)
	forCluster := gc.options.DynRESTMapper.ForCluster(or.ClusterName)
	mapping, err := forCluster.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}

// restMapping returns the full RESTMapping including scope.
func (gc *GarbageCollector) restMapping(or ObjectReference) (*meta.RESTMapping, error) {
	gvk := schema.FromAPIVersionAndKind(or.OwnerReference.APIVersion, or.OwnerReference.Kind)
	forCluster := gc.options.DynRESTMapper.ForCluster(or.ClusterName)
	return forCluster.RESTMapping(gvk.GroupKind(), gvk.Version)
}

// ---------------------------------------------------------------------------
// isDangling + classifyReferences
// ---------------------------------------------------------------------------

// isDangling checks whether the given ownerRef points to an absent owner.
// It checks the absent owner cache first, then the API server.
func (gc *GarbageCollector) isDangling(ctx context.Context, ownerRef ObjectReference, dependent ObjectReference) (dangling bool, owner *metav1.PartialObjectMetadata, err error) {
	// Fast path: check absent owner cache (cluster-scoped).
	clusterKey := absentOwnerCacheKey{ClusterName: ownerRef.ClusterName, UID: ownerRef.UID}
	if gc.absentOwnerCache.Has(clusterKey) {
		return true, nil, nil
	}
	// Check namespaced key.
	nsKey := absentOwnerCacheKey{ClusterName: ownerRef.ClusterName, UID: ownerRef.UID, Namespace: dependent.Namespace}
	if gc.absentOwnerCache.Has(nsKey) {
		return true, nil, nil
	}

	// Resolve scope.
	mapping, err := gc.restMapping(ownerRef)
	if err != nil {
		return false, nil, err
	}
	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace

	// Namespace scoping validation: cluster-scoped dependent with
	// namespaced owner is invalid.
	if len(dependent.Namespace) == 0 && namespaced {
		return false, nil, errNamespacedOwnerOfClusterScopedObject
	}

	ns := ""
	if namespaced {
		ns = dependent.Namespace
	}

	// Slow path: GET owner from API server.
	owner, err = gc.options.MetadataClusterClient.
		Cluster(ownerRef.ClusterName.Path()).
		Resource(mapping.Resource).
		Namespace(ns).
		Get(ctx, ownerRef.Name, metav1.GetOptions{})

	if apierrors.IsNotFound(err) {
		cacheKey := absentOwnerCacheKey{ClusterName: ownerRef.ClusterName, UID: ownerRef.UID, Namespace: ns}
		gc.absentOwnerCache.Add(cacheKey)
		return true, nil, nil
	}
	if err != nil {
		return false, nil, err
	}

	// UID mismatch — object was reincarnated.
	if owner.GetUID() != ownerRef.UID {
		cacheKey := absentOwnerCacheKey{ClusterName: ownerRef.ClusterName, UID: ownerRef.UID, Namespace: ns}
		gc.absentOwnerCache.Add(cacheKey)
		return true, nil, nil
	}

	return false, owner, nil
}

// classifyReferences classifies each ownerReference as solid, dangling, or
// waitingForDependentsDeletion.
func (gc *GarbageCollector) classifyReferences(ctx context.Context, dependent ObjectReference, ownerRefs []ObjectReference) (
	solid, dangling, waitingForDependentsDeletion []ObjectReference, err error,
) {
	for _, ownerRef := range ownerRefs {
		isDangling, owner, err := gc.isDangling(ctx, ownerRef, dependent)
		if err != nil {
			if errors.Is(err, errNamespacedOwnerOfClusterScopedObject) {
				// Non-retryable: skip this reference.
				dangling = append(dangling, ownerRef)
				continue
			}
			return nil, nil, nil, err
		}
		if isDangling {
			dangling = append(dangling, ownerRef)
			continue
		}
		if owner.GetDeletionTimestamp() != nil && hasFinalizer(owner, metav1.FinalizerDeleteDependents) {
			waitingForDependentsDeletion = append(waitingForDependentsDeletion, ownerRef)
		} else {
			solid = append(solid, ownerRef)
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Deletion queue (owner-side): "owner was deleted, cascade to dependents"
// ---------------------------------------------------------------------------

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

	gc.log.V(4).Info("Processing deletion queue item", "object", or)
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
		return false, fmt.Errorf("error getting GVR for object %v: %w", or, err)
	}

	client := gc.options.MetadataClusterClient.Cluster(or.ClusterName.Path()).
		Resource(gvr).
		Namespace(or.Namespace)

	latest, err := client.Get(ctx, or.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Owner is gone. Add to absent owner cache and enqueue
		// dependents for ownerRef-driven deletion check.
		gc.absentOwnerCache.Add(absentOwnerCacheKey{ClusterName: or.ClusterName, UID: or.UID, Namespace: or.Namespace})
		for _, dep := range gc.graph.Owned(or) {
			gc.attemptToDelete.Add(dep)
		}
		gc.graph.Remove(or)
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// UID mismatch: the object was reincarnated.
	if latest.GetUID() != or.UID {
		gc.absentOwnerCache.Add(absentOwnerCacheKey{ClusterName: or.ClusterName, UID: or.UID, Namespace: or.Namespace})
		for _, dep := range gc.graph.Owned(or) {
			gc.attemptToDelete.Add(dep)
		}
		gc.graph.Remove(or)
		return false, nil
	}

	// Object still exists. If it is being deleted with
	// FinalizerDeleteDependents, enqueue it for foreground processing.
	if latest.GetDeletionTimestamp() != nil && hasFinalizer(latest, metav1.FinalizerDeleteDependents) {
		for _, dep := range gc.graph.Owned(or) {
			gc.attemptToDelete.Add(dep)
		}
		gc.attemptToDelete.Add(or)
		return false, nil
	}

	// Handle FinalizerOrphanDependents: orphan owned objects.
	owned := gc.graph.Owned(or)
	if len(owned) > 0 {
		if hasFinalizer(latest, metav1.FinalizerOrphanDependents) {
			if err := gc.orphanOwned(ctx, or); err != nil {
				return false, err
			}
			return true, nil
		}

		// Enqueue dependents into attemptToDelete — do NOT delete them
		// directly, they may have other live owners.
		for _, dep := range owned {
			gc.attemptToDelete.Add(dep)
		}
		return true, nil
	}

	// No owned objects — proceed to delete.
	if hasFinalizer(latest, metav1.FinalizerOrphanDependents) {
		if err := gc.removeFinalizer(ctx, or, metav1.FinalizerOrphanDependents); err != nil {
			return false, err
		}
	}

	if err := gc.deleteObject(ctx, or, latest, metav1.DeletePropagationBackground); err != nil {
		return false, err
	}

	gc.graph.Remove(or)
	return false, nil
}

// ---------------------------------------------------------------------------
// attemptToDelete queue (dependent-side): "check if I should be deleted"
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) runAttemptToDeleteWorker(ctx context.Context) {
	for gc.processAttemptToDelete(ctx) {
	}
}

func (gc *GarbageCollector) processAttemptToDelete(ctx context.Context) bool {
	or, shutdown := gc.attemptToDelete.Get()
	if shutdown {
		return false
	}
	defer gc.attemptToDelete.Done(or)

	gc.log.V(4).Info("Processing attemptToDelete item", "object", or)
	err := gc.attemptToDeleteItem(ctx, or)
	if err != nil {
		gc.log.Error(err, "error processing attemptToDelete item", "object", or)
		gc.attemptToDelete.AddRateLimited(or)
		return true
	}

	gc.attemptToDelete.Forget(or)
	return true
}

func (gc *GarbageCollector) attemptToDeleteItem(ctx context.Context, item ObjectReference) error {
	logger := gc.log.WithValues("object", item)

	gvr, err := gc.GVR(item)
	if err != nil {
		return err
	}

	client := gc.options.MetadataClusterClient.Cluster(item.ClusterName.Path()).
		Resource(gvr).
		Namespace(item.Namespace)

	latest, err := client.Get(ctx, item.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		gc.graph.Remove(item)
		return nil
	}
	if err != nil {
		return err
	}

	// UID mismatch — stale reference.
	if latest.GetUID() != item.UID {
		gc.graph.Remove(item)
		return nil
	}

	// If being deleted and NOT waiting for dependents, let deletion
	// proceed naturally.
	beingDeleted := latest.GetDeletionTimestamp() != nil
	deletingDependents := beingDeleted && hasFinalizer(latest, metav1.FinalizerDeleteDependents)

	if beingDeleted && !deletingDependents {
		return nil
	}

	// Foreground cascade: object is waiting for its dependents to be
	// deleted. Check if blocking dependents are gone.
	if deletingDependents {
		return gc.processDeletingDependentsItem(ctx, item)
	}

	// Classify owner references.
	ownerRefs := ObjectReferencesFromOwnerReferences(item.ClusterName, item.Namespace, latest.GetOwnerReferences())
	if len(ownerRefs) == 0 {
		return nil
	}

	solid, dangling, waitingForDependentsDeletion, err := gc.classifyReferences(ctx, item, ownerRefs)
	if err != nil {
		return err
	}

	logger.V(4).Info("Classified references",
		"solid", len(solid), "dangling", len(dangling),
		"waitingForDependentsDeletion", len(waitingForDependentsDeletion))

	switch {
	case len(solid) > 0:
		// Has at least one live owner — do NOT delete.
		// Patch out dangling + waitingForDependentsDeletion refs.
		if len(dangling) == 0 && len(waitingForDependentsDeletion) == 0 {
			return nil
		}
		ownerUIDs := make([]types.UID, 0, len(dangling)+len(waitingForDependentsDeletion))
		for _, ref := range dangling {
			ownerUIDs = append(ownerUIDs, ref.UID)
		}
		for _, ref := range waitingForDependentsDeletion {
			ownerUIDs = append(ownerUIDs, ref.UID)
		}
		return gc.patchRemoveOwnerReferences(ctx, item, ownerUIDs)

	case len(waitingForDependentsDeletion) > 0:
		owned := gc.graph.Owned(item)
		if len(owned) > 0 {
			// Check for cycles: if any dependent is itself
			// deletingDependents, break the cycle by unblocking
			// BlockOwnerDeletion on this item's ownerRefs.
			if gc.detectAndBreakCycle(ctx, item, owned) {
				logger.V(2).Info("Broke potential ownership cycle")
			}
			return gc.deleteObject(ctx, item, latest, metav1.DeletePropagationForeground)
		}
		// No dependents — fall through to delete.
		fallthrough

	default:
		// All owners are dangling — delete.
		var policy metav1.DeletionPropagation
		switch {
		case hasFinalizer(latest, metav1.FinalizerOrphanDependents):
			policy = metav1.DeletePropagationOrphan
		case hasFinalizer(latest, metav1.FinalizerDeleteDependents):
			policy = metav1.DeletePropagationForeground
		default:
			policy = metav1.DeletePropagationBackground
		}
		return gc.deleteObject(ctx, item, latest, policy)
	}
}

// processDeletingDependentsItem handles an object that has
// FinalizerDeleteDependents and is waiting for its blocking dependents to be
// deleted.
func (gc *GarbageCollector) processDeletingDependentsItem(ctx context.Context, item ObjectReference) error {
	blocking := gc.blockingDependents(item)
	if len(blocking) == 0 {
		// All blocking dependents are gone — remove the finalizer.
		return gc.removeFinalizer(ctx, item, metav1.FinalizerDeleteDependents)
	}
	// Enqueue blocking dependents for deletion.
	for _, dep := range blocking {
		gc.attemptToDelete.Add(dep)
	}
	return nil
}

// blockingDependents returns dependents that have BlockOwnerDeletion=true for
// the given owner.
func (gc *GarbageCollector) blockingDependents(owner ObjectReference) []ObjectReference {
	owned := gc.graph.Owned(owner)
	var blocking []ObjectReference
	for _, dep := range owned {
		depOwners := gc.graph.Owners(dep)
		for _, ownerRef := range depOwners {
			if ownerRef.UID == owner.UID &&
				ownerRef.BlockOwnerDeletion != nil &&
				*ownerRef.BlockOwnerDeletion {
				blocking = append(blocking, dep)
				break
			}
		}
	}
	return blocking
}

// detectAndBreakCycle checks if any dependent of item is also
// deletingDependents, which indicates a potential ownership cycle. If found,
// it patches the item's ownerRefs to set BlockOwnerDeletion=false.
func (gc *GarbageCollector) detectAndBreakCycle(ctx context.Context, item ObjectReference, owned []ObjectReference) bool {
	for _, dep := range owned {
		depOwners := gc.graph.Owners(dep)
		for _, ownerRef := range depOwners {
			if ownerRef.UID != item.UID || ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
				continue
			}
			// This dependent blocks item. Check if the dependent
			// itself is deletingDependents.
			depGVR, err := gc.GVR(dep)
			if err != nil {
				continue
			}
			depLatest, err := gc.options.MetadataClusterClient.
				Cluster(dep.ClusterName.Path()).
				Resource(depGVR).
				Namespace(dep.Namespace).
				Get(ctx, dep.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if depLatest.GetDeletionTimestamp() != nil && hasFinalizer(depLatest, metav1.FinalizerDeleteDependents) {
				// Potential cycle — unblock.
				_ = gc.patchUnblockOwnerReferences(ctx, item)
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// API operations with conflict handling
// ---------------------------------------------------------------------------

// deleteObject deletes the given object with conflict handling.
// On 409 Conflict, it re-GETs the object and retries without RV precondition
// if ownerReferences are unchanged.
func (gc *GarbageCollector) deleteObject(ctx context.Context, item ObjectReference, latest *metav1.PartialObjectMetadata, policy metav1.DeletionPropagation) error {
	gvr, err := gc.GVR(item)
	if err != nil {
		return err
	}

	client := gc.options.MetadataClusterClient.Cluster(item.ClusterName.Path()).
		Resource(gvr).
		Namespace(item.Namespace)

	preconditions := metav1.Preconditions{UID: &item.UID}
	rv := ""
	if latest != nil {
		rv = latest.GetResourceVersion()
	}
	if len(rv) > 0 {
		preconditions.ResourceVersion = &rv
	}

	err = client.Delete(ctx, item.Name, metav1.DeleteOptions{
		Preconditions:     &preconditions,
		PropagationPolicy: &policy,
	})

	if apierrors.IsConflict(err) && len(rv) > 0 {
		liveObj, liveErr := client.Get(ctx, item.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(liveErr) {
			return nil
		}
		if liveErr == nil && liveObj.GetUID() == item.UID &&
			reflect.DeepEqual(liveObj.GetOwnerReferences(), latest.GetOwnerReferences()) {
			// ownerRefs unchanged — retry without RV precondition.
			return gc.deleteObject(ctx, item, nil, policy)
		}
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// removeFinalizer removes the given finalizer from the object, retrying on
// conflict.
func (gc *GarbageCollector) removeFinalizer(ctx context.Context, item ObjectReference, finalizer string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		gvr, err := gc.GVR(item)
		if err != nil {
			return err
		}
		client := gc.options.MetadataClusterClient.Cluster(item.ClusterName.Path()).
			Resource(gvr).
			Namespace(item.Namespace)

		latest, err := client.Get(ctx, item.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !hasFinalizer(latest, finalizer) {
			return nil
		}

		patch, err := patchRemoveFinalizer(latest, finalizer)
		if err != nil {
			return err
		}
		_, err = client.Patch(ctx, item.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	})
}

// patchRemoveOwnerReferences removes the given owner UIDs from the object's
// ownerReferences, retrying on conflict.
func (gc *GarbageCollector) patchRemoveOwnerReferences(ctx context.Context, item ObjectReference, ownerUIDs []types.UID) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		gvr, err := gc.GVR(item)
		if err != nil {
			return err
		}
		client := gc.options.MetadataClusterClient.Cluster(item.ClusterName.Path()).
			Resource(gvr).
			Namespace(item.Namespace)

		latest, err := client.Get(ctx, item.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		patch, err := patchRemoveOwnerReferencesByUIDs(latest, ownerUIDs)
		if err != nil {
			return err
		}
		_, err = client.Patch(ctx, item.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	})
}

// patchUnblockOwnerReferences sets BlockOwnerDeletion=false on all
// ownerReferences of the given object.
func (gc *GarbageCollector) patchUnblockOwnerReferences(ctx context.Context, item ObjectReference) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		gvr, err := gc.GVR(item)
		if err != nil {
			return err
		}
		client := gc.options.MetadataClusterClient.Cluster(item.ClusterName.Path()).
			Resource(gvr).
			Namespace(item.Namespace)

		latest, err := client.Get(ctx, item.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		patch, err := patchUnblockOwnerRefs(latest)
		if err != nil {
			return err
		}
		if patch == nil {
			return nil
		}
		_, err = client.Patch(ctx, item.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	})
}

// ---------------------------------------------------------------------------
// Orphan logic
// ---------------------------------------------------------------------------

func (gc *GarbageCollector) orphanOwned(ctx context.Context, or ObjectReference) error {
	owned := gc.graph.Owned(or)
	if len(owned) == 0 {
		return nil
	}

	var errs error
	for _, ownedRef := range owned {
		if err := gc.patchRemoveOwnerReferences(ctx, ownedRef, []types.UID{or.UID}); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}
