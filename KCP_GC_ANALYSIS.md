# KCP-Native Garbage Collector Gap Analysis

Comparison of the kcp-native GC (`pkg/reconciler/garbagecollector/`) against the
upstream Kubernetes GC behavioural contract documented in `GC_ANALYSIS.md`.

---

## Issues the KCP-native GC does not address

### 1. No dangling reference cleanup (B2)

The upstream GC classifies ownerReferences as solid/dangling/waitingForDependentsDeletion.
When an object has *some* live owners and *some* dangling owners, the upstream GC
**patches out** the dangling references while keeping the object alive.

The kcp-native GC has no equivalent. `processDeletionQueueItem` only runs when a
delete event arrives — it never inspects an object's ownerReferences to clean up
stale ones. An object with 2 owners where only 1 is deleted will never have the
dead reference removed.

### 2. No ownerReference-driven deletion trigger (B1)

The upstream GC deletes an object when **all** of its ownerReferences point to
absent owners. It does this by checking each ownerReference against the API server
from `attemptToDeleteItem`.

The kcp-native GC only processes objects that arrive in the `deletionQueue` via
informer **delete events** — i.e., it only cascades *downward* from a deleted
owner. It never asks "are all my owners gone?" from the dependent's perspective.
If an object has 2 owners and both are deleted in separate events, the dependent's
deletion depends entirely on the graph processing both delete cascades, but
`processDeletionQueueItem` (`garbagecocollector.go:462`) doesn't check
ownerReferences at all — it just tries to delete the object outright.

### 3. No foreground cascading delete (B3)

The upstream GC implements `FinalizerDeleteDependents` semantics: when an owner is
being deleted with this finalizer, it sets `deletingDependents=true`, deletes
blocking dependents first (those with `BlockOwnerDeletion=true`), and then removes
the finalizer to allow the owner's final deletion.

The kcp-native GC at `garbagecocollector.go:494` explicitly says:

```go
// metav1.FinalizerDeleteDependents doesn't need special handling.
```

This is incorrect. The upstream GC's foreground deletion is a protocol: the owner
sits in a "deleting dependents" state with its finalizer, and the GC must actively
remove that finalizer once blocking dependents are gone. The kcp-native GC never
removes `FinalizerDeleteDependents` from owners, so owners with this finalizer
will get **stuck forever** — the API server won't finalize them.

### 4. No BlockOwnerDeletion handling (B3, E9)

`BlockOwnerDeletion` is never read or acted upon. The `ObjectReferenceFrom`
function (`object_reference.go:54`) has a TODO:

```go
// TODO(ntnn): handle BlockOwnerDeletion
```

In upstream, blocking dependents must be deleted *before* the owner's finalizer is
removed. Without this, foreground deletion ordering guarantees are violated.

### 5. No cycle detection (B3 contract item 6, E9)

The upstream GC detects when a blocking dependent is itself `isDeletingDependents()`
and breaks the cycle by unsetting `BlockOwnerDeletion`. The kcp-native GC has no
cycle detection at all. Circular ownership with `BlockOwnerDeletion=true` could
deadlock.

### 6. No virtual node management (V1–V7)

The upstream GC handles "forward references" — when a dependent's ownerReference
points to a UID not yet seen by any informer, a virtual node is created and
resolved later. This is critical for startup ordering (dependents may be observed
before their owners).

The kcp-native GC has no concept of virtual nodes. If a dependent is observed
before its owner, the owner's graph entry is created (via `StoreIfAbsent` in
`graph.go:72`), but there's no mechanism to validate whether the owner actually
exists. The graph silently creates entries for UIDs it has never seen, with no
resolution or cleanup path.

### 7. No object reincarnation detection (E1)

The upstream GC handles UID recycling: if an object is deleted and a new object
with the same name but different UID appears, the GC detects the UID mismatch
and enqueues a virtual delete for the old UID.

The kcp-native GC uses UID as its graph key (`ID` type). If a UID were recycled,
the graph would silently conflate the old and new objects. More practically, when
`processDeletionQueueItem` fetches the latest object from the API server
(`garbagecocollector.go:475`), it never compares the returned UID against the
expected UID. A reincarnated object could be wrongly deleted.

### 8. No conflict handling on delete or patch (E5, E6)

The upstream GC handles 409 Conflict on delete by re-fetching the object and
retrying without a resourceVersion precondition if ownerReferences haven't
changed. It also uses `retry.RetryOnConflict` for finalizer removal.

The kcp-native GC uses `DeletePropagationBackground` with a UID precondition but
no resourceVersion (`garbagecocollector.go:534`). If the delete returns a
conflict, it's treated as a generic error and requeued with rate limiting. The
`patchRemoveFinalizer` and `patchRemoveOwnerReference` calls (`utils.go`) don't
include UID preconditions or retry-on-conflict logic.

### 9. No partial discovery failure tolerance (M2, E12)

The upstream GC preserves existing monitors when some API groups fail discovery.
If `GetDeletableResources` returns zero results, it skips the sync entirely.

The kcp-native GC adds/removes monitors directly in CRD informer event handlers
(`garbagecocollector.go:155-174`). There's no protection against a scenario where
CRD informer state is incomplete (e.g., during startup or after a watch
interruption), which could cause monitors to be prematurely removed.

### 10. No namespace scoping validation (E2, E3)

The upstream GC validates that cluster-scoped objects don't have namespaced owners
(returns `namespacedOwnerOfClusterScopedObjectErr`) and detects cross-namespace
ownership mismatches.

The kcp-native GC inherits the namespace from the dependent's ownerReference
context (`ObjectReferencesFromOwnerReferences` at `object_reference.go:88`) but
never validates scope. A cluster-scoped object with a namespaced ownerReference
would silently produce a broken graph entry.

### 11. No absent owner cache (C1–C4)

The upstream GC uses an LRU cache of confirmed-absent owners to avoid redundant
API server GETs. The kcp-native GC has no such cache. Every requeue cycle for an
object with absent owners will hit the API server.

### 12. Deletion logic inverts the ownership model

The upstream GC works from the **dependent's** perspective: "all my owners are
gone, therefore I should be deleted." The kcp-native GC works from the **owner's**
perspective: "I was deleted, so let me queue my dependents." This means:

- `processDeletionQueueItem` queues dependents for deletion when the owner is
  deleted (`garbagecocollector.go:511`), but these dependents may still have
  **other live owners**. The code never checks — it just queues them for deletion.
- An object queued as a dependent will be fetched from the API, then
  unconditionally deleted (`garbagecocollector.go:540-553`) regardless of whether
  it has surviving owners.

This is a correctness bug: **objects with multiple owners will be prematurely
deleted when any single owner is deleted.**

### 13. No debug introspection (contract item 12)

The upstream GC exposes a DOT-format graph dump via HTTP at `/graph`. The
kcp-native GC has no equivalent.

### 14. Graph `Owned` panics on missing entries

`graph.go:103`: `Owned` dereferences `ownedObjs` without checking if it's nil
(the `_` discards the `exists` bool from `Load`). If called with an
ObjectReference not in the graph, this will panic on the `*ownedObjs` dereference.

### 15. Graph `Remove` doesn't clean up owner→dependent edges atomically

`graph.go:109-134`: `Remove` deletes the node and then iterates the entire graph
to remove it from all owner entries. Between the delete and the range, another
goroutine could observe an inconsistent state where the node is gone but still
appears as a dependent.

---

## Summary of severity

| Severity | Issues |
|----------|--------|
| **Correctness** (will cause wrong behavior) | #2 (no ownerRef-driven deletion), #3 (foreground delete stuck forever), #12 (premature deletion of multi-owner objects) |
| **Completeness** (missing features) | #1, #4, #5, #6, #7, #8, #9, #10, #11, #13 |
| **Crash** | #14 (nil deref panic in `Owned`) |
| **Race condition** | #15 (non-atomic graph cleanup) |

The most critical issue is **#12**: the fundamental deletion logic is inverted.
The kcp-native GC will delete objects that still have live owners. This must be
fixed before any other issue.
