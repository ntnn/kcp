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

// Package identityrotation drives APIExport identity rotation (enhancements
// KEP 0005). A rotation is requested through an APIExportIdentityRotation
// object (served by the platform-owned migration.kcp.io APIExport) living
// next to the APIExport in the provider workspace.
//
// Key rotation preserves the export's stable identity. It accepts the new key
// before switching the secret reference and retires the old key per policy,
// without changing consumer bindings or draining storage. Legacy identity
// migrations retain their per-shard progress tracking.
package identityrotation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	migrationv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/migration/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/identity"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-apiexport-identity-rotation"

	stableKeyRotationReason = "StableKeyRotation"
)

// NewController returns the identity rotation controller. It runs on every
// shard and handles rotation objects living on that shard. The rotated
// export may live in another workspace (and on another shard): reads resolve
// through the local and cache informers, writes to the export and reads of
// the new identity secret go through path-based external clients so they
// reach the export's home shard.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	externalKcpClusterClient kcpclientset.ClusterInterface,
	externalKubeClusterClient kcpkubernetesclientset.ClusterInterface,
	rotationInformer migrationv1alpha1informers.APIExportIdentityRotationClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalShardInformer corev1alpha1informers.ShardClusterInformer,
) (*Controller, error) {
	lookupIdentity, err := indexers.NewIdentityLookup(apiExportInformer.Informer(), globalAPIExportInformer.Informer())
	if err != nil {
		return nil, err
	}
	c := &Controller{
		lookupIdentity: lookupIdentity,
		// Cap retry backoff so transient conflicts do not delay key changes
		// or legacy migration progress.
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](5*time.Millisecond, 2*time.Second),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getRotation: func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportIdentityRotation, error) {
			return rotationInformer.Cluster(cluster).Lister().Get(name)
		},
		updateRotationStatus: func(ctx context.Context, cluster logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
			// per-shard migrators concurrently server-side-apply their
			// entries into status.shards, so plain updates conflict. Retry
			// with a fresh read, carrying over the migrator-owned shard
			// entries — this controller never writes them.
			// The retry is bounded: a persistent conflict is returned to the
			// workqueue, which re-runs the reconcile from a fresh object
			// instead of pushing a stale status indefinitely.
			client := kcpClusterClient.Cluster(cluster).MigrationV1alpha1().APIExportIdentityRotations()
			var updated *migrationv1alpha1.APIExportIdentityRotation
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				var err error
				updated, err = client.UpdateStatus(ctx, rotation, metav1.UpdateOptions{})
				if !apierrors.IsConflict(err) {
					return err
				}
				fresh, getErr := client.Get(ctx, rotation.Name, metav1.GetOptions{})
				if getErr != nil {
					return getErr
				}
				shards := fresh.Status.Shards
				fresh.Status = rotation.Status
				fresh.Status.Shards = shards
				rotation = fresh
				return err
			})
			return updated, err
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		updateAPIExport: func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			return externalKcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		},
		updateAPIExportStatus: func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			return externalKcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().UpdateStatus(ctx, export, metav1.UpdateOptions{})
		},
		getSecretHash: func(ctx context.Context, path logicalcluster.Path, namespace, name string) (string, error) {
			secret, err := externalKubeClusterClient.Cluster(path).CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return identity.KeyHash(secret)
		},
		listShardNames: func() ([]string, error) {
			shards, err := globalShardInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(shards))
			for _, shard := range shards {
				names = append(names, shard.Name)
			}
			return names, nil
		},
	}

	_, _ = rotationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	return c, nil
}

// Controller drives APIExportIdentityRotations through their lifecycle.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getRotation           func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportIdentityRotation, error)
	listShardNames        func() ([]string, error)
	updateRotationStatus  func(ctx context.Context, cluster logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error)
	getAPIExport          func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	updateAPIExport       func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error)
	updateAPIExportStatus func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error)
	getSecretHash         func(ctx context.Context, path logicalcluster.Path, namespace, name string) (string, error)
	lookupIdentity        func(string) (string, error)
}

// exportPath is the workspace path the rotation's export reference resolves
// in: the referenced path, or the rotation's own workspace when empty.
// activeRotationOldHash reports whether the given active-rotation annotation
// value ("<cluster>|<rotation name>|<new hash>|<old hash>") records a flip
// performed for exactly this rotation onto exactly this new hash, and if so
// returns the pre-rotation key fingerprint it preserved.
func activeRotationOldHash(value string, clusterName logicalcluster.Name, rotationName, newHash string) (string, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 4 || parts[0] != clusterName.String() || parts[1] != rotationName || parts[2] != newHash || parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

func exportPath(clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) logicalcluster.Path {
	if path := logicalcluster.NewPath(rotation.Spec.Export.Path); !path.Empty() {
		return path
	}
	return clusterName.Path()
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	requeueAfter, err := c.process(ctx, key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	if requeueAfter > 0 {
		c.queue.AddAfter(key, requeueAfter)
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (time.Duration, error) {
	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return 0, nil
	}

	rotation, err := c.getRotation(clusterName, name)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rotation = rotation.DeepCopy()

	switch rotation.Status.Phase {
	case "", migrationv1alpha1.APIExportIdentityRotationPending:
		return 0, c.reconcilePending(ctx, clusterName, rotation)
	case migrationv1alpha1.APIExportIdentityRotationMigrating:
		return 0, c.reconcileMigrating(ctx, clusterName, rotation)
	case migrationv1alpha1.APIExportIdentityRotationAliasActive:
		return c.reconcileAliasActive(ctx, clusterName, rotation)
	default:
		return 0, nil // Completed and Failed are terminal
	}
}

// reconcilePending verifies the current key before accepting and switching keys.
func (c *Controller) reconcilePending(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	logger := klog.FromContext(ctx)

	path := exportPath(clusterName, rotation)
	export, err := c.getAPIExport(path, rotation.Spec.Export.Name)
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, clusterName, rotation, "APIExport %s|%s not found", path, rotation.Spec.Export.Name)
	}
	if err != nil {
		return err
	}
	if export.Status.IdentityHash == "" {
		// The external rotation client may publish fingerprints, but only
		// the identity controller can initialize the stable identity.
		return fmt.Errorf("APIExport %s|%s has no identity yet", path, rotation.Spec.Export.Name)
	}
	// all export-side operations happen in the export's workspace, which may
	// be on another shard: address it by cluster name through the external,
	// path-capable client.
	exportCluster := logicalcluster.From(export).Path()

	ref := rotation.Spec.NewIdentity.SecretRef
	if ref == nil {
		return c.fail(ctx, clusterName, rotation, "spec.newIdentity.secretRef is required")
	}
	newHash, err := c.getSecretHash(ctx, exportCluster, ref.Namespace, ref.Name)
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, clusterName, rotation, "identity secret %s/%s not found; pre-create the new identity secret in the export's workspace %s", ref.Namespace, ref.Name, path)
	}
	if err != nil {
		return err
	}
	if export.Spec.Identity == nil || export.Spec.Identity.SecretRef == nil {
		return c.fail(ctx, clusterName, rotation, "APIExport has no current identity secret")
	}
	currentRef := export.Spec.Identity.SecretRef
	oldHash, err := c.getSecretHash(ctx, exportCluster, currentRef.Namespace, currentRef.Name)
	if err != nil {
		return err
	}
	_, acceptedHashes, err := identity.Verify(export.Status.IdentityHash, export.Status.IdentityKeyHashes, oldHash, nil)
	if err != nil {
		return c.fail(ctx, clusterName, rotation, "current identity secret is not accepted: %v", err)
	}
	if err := c.verifyKeyIdentities(export.Status.IdentityHash, oldHash, newHash); err != nil {
		return err
	}
	activeRotation := export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey]
	if annOldHash, flipped := activeRotationOldHash(activeRotation, clusterName, rotation.Name, newHash); flipped {
		if sets.New(export.Status.IdentityAliasHashes...).Has(annOldHash) {
			return c.fail(ctx, clusterName, rotation, "legacy identity migration cannot be resumed as a stable key rotation; the existing identity and aliases are unchanged")
		}
		// Ownership may have been acquired before publishing the key, or the
		// secret may already have switched. Recover the original fingerprint
		// in either case and resume the idempotent publication/switch.
		oldHash = annOldHash
	} else if activeRotation != "" {
		// Admission enforces one active rotation per export, but it decides
		// from an informer cache; two near-simultaneous creates can both get
		// through. The export's annotation is the authoritative record of
		// which rotation owns the current flip, so a second one fails here
		// instead of flipping the export a second time underneath the first.
		return c.fail(ctx, clusterName, rotation, "APIExport %s|%s already has an active identity rotation (%s); at most one rotation per export may be active", path, rotation.Spec.Export.Name, activeRotation)
	} else if newHash == oldHash {
		return c.fail(ctx, clusterName, rotation, "the new identity secret hashes to the export's current identity; rotation requires a fresh secret")
	}

	// Acquire ownership with a resourceVersion-guarded metadata update before
	// publishing a key. A losing concurrent rotation must not leave an accepted
	// fingerprint behind.
	updatedExport := export.DeepCopy()
	if activeRotation == "" {
		if updatedExport.Annotations == nil {
			updatedExport.Annotations = map[string]string{}
		}
		updatedExport.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey] = clusterName.String() + "|" + rotation.Name + "|" + newHash + "|" + oldHash
		if updatedExport, err = c.updateAPIExport(ctx, exportCluster, updatedExport); err != nil {
			return err
		}
	}
	// Accept before switching the secret so identity verification stays valid
	// if either subsequent write needs to be retried.
	updatedExport.Status.IdentityKeyHashes = sets.List(sets.New(acceptedHashes...).Insert(newHash))
	conditions.MarkFalse(updatedExport, apisv1alpha2.IdentityRotationInProgress, "RotationActive", conditionsv1alpha1.ConditionSeverityInfo,
		"identity rotation %s is replacing key %s with %s", rotation.Name, oldHash, newHash)
	if updatedExport, err = c.updateAPIExportStatus(ctx, exportCluster, updatedExport); err != nil {
		return err
	}
	updatedExport.Spec.Identity = &apisv1alpha2.Identity{SecretRef: ref}
	if _, err = c.updateAPIExport(ctx, exportCluster, updatedExport); err != nil {
		return err
	}

	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationMigrating
	rotation.Status.OldIdentityHash = oldHash
	rotation.Status.NewIdentityHash = newHash
	conditions.Set(rotation, &conditionsv1alpha1.Condition{
		Type: migrationv1alpha1.IdentityRotationValid, Status: corev1.ConditionTrue, Reason: stableKeyRotationReason,
	})
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	logger.V(2).Info("identity rotation started", "export", rotation.Spec.Export.Name, "path", path, "old", oldHash, "new", newHash)
	return err
}

// reconcileMigrating completes stable key rotations without a storage drain.
// For legacy identity migrations it aggregates per-shard progress. Every shard's
// identity migrator reports its own bindings of the rotating export into
// status.shards (including shards with zero bindings). The drain is complete
// only when every shard known to the cache server has reported and every
// entry is fully migrated — this gates alias retirement, so a shard that is
// still draining can never have the alias pulled out from under it.
func (c *Controller) reconcileMigrating(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	if valid := conditions.Get(rotation, migrationv1alpha1.IdentityRotationValid); valid != nil && valid.Reason == stableKeyRotationReason {
		export, err := c.getAPIExport(exportPath(clusterName, rotation), rotation.Spec.Export.Name)
		if err != nil {
			return err
		}
		if sets.New(export.Status.IdentityKeyHashes...).Has(rotation.Status.NewIdentityHash) {
			rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationAliasActive
			rotation.Status.AliasActiveTimestamp = &metav1.Time{Time: time.Now()}
			conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationDrained)
			_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
			return err
		}
		return fmt.Errorf("waiting for the accepted new key to appear on APIExport %s", rotation.Spec.Export.Name)
	}
	shardNames, err := c.listShardNames()
	if err != nil {
		return err
	}
	known := sets.New(shardNames...)

	// status.shards entries are owned by the per-shard migrators (written
	// via server-side apply); this controller only reads them. Entries of
	// shards that no longer exist are ignored, so a removed shard does not
	// block the drain forever. Entries evaluated against any identity other
	// than this rotation's new one are stale (a shard counting against a
	// not-yet-replicated export status) and are ignored too.
	reported := sets.New[string]()
	var total, migrated int32
	drained := true
	for _, entry := range rotation.Status.Shards {
		if !known.Has(entry.Shard) || entry.IdentityHash != rotation.Status.NewIdentityHash {
			continue
		}
		reported.Insert(entry.Shard)
		total += entry.TotalBindings
		migrated += entry.MigratedBindings
		if entry.MigratedBindings != entry.TotalBindings {
			drained = false
		}
	}
	rotation.Status.TotalBindings = total
	rotation.Status.MigratedBindings = migrated

	if missing := known.Difference(reported); missing.Len() > 0 {
		conditions.MarkFalse(rotation, migrationv1alpha1.IdentityRotationDrained, "AwaitingShardReports", conditionsv1alpha1.ConditionSeverityInfo,
			"waiting for drain reports from shards: %v", sets.List(missing))
		_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
		return err
	}

	if drained {
		rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationAliasActive
		rotation.Status.AliasActiveTimestamp = &metav1.Time{Time: time.Now()}
		conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationDrained)
	}
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	return err
}

// reconcileAliasActive retires the alias per the requested policy.
func (c *Controller) reconcileAliasActive(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) (time.Duration, error) {
	policy := rotation.Spec.AliasRetirement.Policy
	if policy == "" {
		policy = migrationv1alpha1.AliasRetirementManual
	}

	switch policy {
	case migrationv1alpha1.AliasRetirementManual:
		return 0, nil // wait for the provider to tighten the policy
	case migrationv1alpha1.AliasRetirementAfter:
		if rotation.Spec.AliasRetirement.After == nil {
			// admission rejects this; guard pre-existing objects by saying
			// why the alias is not being retired rather than idling forever.
			if cond := conditions.Get(rotation, migrationv1alpha1.IdentityRotationAliasRetired); cond == nil || cond.Reason != "MissingAfterDuration" {
				conditions.MarkFalse(rotation, migrationv1alpha1.IdentityRotationAliasRetired, "MissingAfterDuration", conditionsv1alpha1.ConditionSeverityWarning,
					"spec.aliasRetirement.after is required for the After policy; the alias stays active until it is set or the policy is changed")
				_, err := c.updateRotationStatus(ctx, clusterName.Path(), rotation)
				return 0, err
			}
			return 0, nil
		}
		if rotation.Status.AliasActiveTimestamp == nil {
			rotation.Status.AliasActiveTimestamp = &metav1.Time{Time: time.Now()}
			_, err := c.updateRotationStatus(ctx, clusterName.Path(), rotation)
			return 0, err
		}
		deadline := rotation.Status.AliasActiveTimestamp.Add(rotation.Spec.AliasRetirement.After.Duration)
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining, nil
		}
	case migrationv1alpha1.AliasRetirementImmediate:
	}

	// Retire only the old key; storage identity and consumer bindings stay put.
	export, err := c.getAPIExport(exportPath(clusterName, rotation), rotation.Spec.Export.Name)
	if err != nil {
		return 0, err
	}
	if len(export.Status.IdentityKeyHashes) > 0 {
		if export.Spec.Identity == nil || export.Spec.Identity.SecretRef == nil {
			return 0, fmt.Errorf("cannot retire the old key without a current identity secret")
		}
		ref := export.Spec.Identity.SecretRef
		currentHash, err := c.getSecretHash(ctx, logicalcluster.From(export).Path(), ref.Namespace, ref.Name)
		if err != nil {
			return 0, err
		}
		if currentHash != rotation.Status.NewIdentityHash || !sets.New(export.Status.IdentityKeyHashes...).Has(currentHash) {
			return 0, fmt.Errorf("cannot retire the old key until the accepted new identity secret is current")
		}
		if err := c.verifyKeyIdentities(export.Status.IdentityHash, currentHash); err != nil {
			return 0, err
		}
		if sets.New(export.Status.IdentityKeyHashes...).Has(rotation.Status.OldIdentityHash) {
			if err := c.verifyKeyIdentities(export.Status.IdentityHash, rotation.Status.OldIdentityHash); err != nil {
				return 0, err
			}
		}
	}
	// TODO: coordinate retirement across exports sharing this stable identity
	// on different shards. Local retirement must not resolve conflicting IDs.
	if sets.New(export.Status.IdentityKeyHashes...).Has(rotation.Status.OldIdentityHash) ||
		sets.New(export.Status.IdentityAliasHashes...).Has(rotation.Status.OldIdentityHash) {
		export = export.DeepCopy()
		export.Status.IdentityKeyHashes = sets.List(sets.New(export.Status.IdentityKeyHashes...).Delete(rotation.Status.OldIdentityHash))
		export.Status.IdentityAliasHashes = sets.List(sets.New(export.Status.IdentityAliasHashes...).Delete(rotation.Status.OldIdentityHash))
		conditions.Delete(export, apisv1alpha2.IdentityRotationInProgress)
		if _, err := c.updateAPIExportStatus(ctx, logicalcluster.From(export).Path(), export); err != nil {
			return 0, err
		}
	}
	// the drain is over; stop the per-shard migrators from reporting.
	if _, ok := export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey]; ok {
		export = export.DeepCopy()
		delete(export.Annotations, migrationv1alpha1.ActiveRotationAnnotationKey)
		if _, err := c.updateAPIExport(ctx, logicalcluster.From(export).Path(), export); err != nil {
			return 0, err
		}
	}

	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationCompleted
	conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationAliasRetired)
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	klog.FromContext(ctx).V(2).Info("identity rotation completed", "export", rotation.Spec.Export.Name, "retired", rotation.Status.OldIdentityHash)
	return 0, err
}

func (c *Controller) fail(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation, format string, args ...interface{}) error {
	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationFailed
	conditions.MarkFalse(rotation, migrationv1alpha1.IdentityRotationValid, "ValidationFailed", conditionsv1alpha1.ConditionSeverityError, format, args...)
	_, err := c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	return err
}

func (c *Controller) verifyKeyIdentities(id string, hashes ...string) error {
	if c.lookupIdentity == nil {
		return nil
	}
	for _, hash := range hashes {
		found, err := c.lookupIdentity(hash)
		if err != nil {
			var reserved *indexers.IdentityReservedError
			if errors.As(err, &reserved) && reserved.Identity == id {
				// An authorized rotation may re-accept its own original key;
				// an uninitialized export cannot use this reservation.
				continue
			}
			return err
		}
		if found != "" && found != id {
			return fmt.Errorf("key hash %q belongs to conflicting identity %q, not %q", hash, found, id)
		}
	}
	return nil
}
