# Garbage Collector Behaviour Analysis

Analysis of `pkg/controller/garbagecollector/` for the purpose of writing a
replacement with feature parity but less complexity.

## Architecture overview

The garbage collector has two logical halves that communicate through work
queues:

```
Informers ──> graphChanges queue ──> GraphBuilder (single-threaded)
                                          │
                                          ├──> attemptToDelete queue ──> N delete workers
                                          └──> attemptToOrphan  queue ──> N orphan workers
```

The **GraphBuilder** is the sole writer of an in-memory directed graph
(`uidToNode`). It consumes informer events, maintains the graph, and enqueues
nodes to the two work queues when action may be needed.

The **GarbageCollector** workers are readers of the graph. They dequeue nodes,
consult the API server for live state, and issue delete/patch requests.

### Files and responsibilities

| File | Purpose |
|------|---------|
| `garbagecollector.go` | GC struct, Run/Sync lifecycle, all delete/orphan worker logic |
| `graph_builder.go` | GraphBuilder, monitor lifecycle, single-threaded graph processing, virtual node resolution |
| `graph.go` | `node`, `objectReference`, `concurrentUIDToNode` data structures |
| `operations.go` | API calls: `getObject`, `deleteObject`, `patchObject`, `removeFinalizer` |
| `patch.go` | Patch construction: `getMetadata` (cache-then-API), SMP/JMP helpers |
| `uid_cache.go` | `ReferenceCache` - LRU of known-absent owner references |
| `errors.go` | `restMappingError` type |
| `dump.go` | DOT-format graph debug handler at `/graph` |
| `garbagecollector_kcp.go` | KCP: single-shot `ResyncMonitors()` |
| `metaonly/types.go` | Minimal metadata-only decode types |
| `metrics/metrics.go` | Single counter: `resources_sync_error_total` |
| `config/types.go` | Configuration struct |

---

## Invariants

1. **Single writer**: Only `processGraphChanges` (single-threaded) mutates
   `uidToNode`, node owners, node identity, and the `virtual`/`beingDeleted`/
   `deletingDependents` flags. Workers only read.

2. **UID is the node key**: Every node is keyed by its object UID in the graph.
   Two objects with the same UID are assumed to be the same object (or a
   reincarnation, detected via API GET).

3. **Owner references are the edges**: Edges are derived from
   `metadata.ownerReferences`. The graph is `dependent -> owner`.

4. **Deletion is one-way**: Once `beingDeleted` is set to true on a node, it is
   never set back to false.

5. **Virtual before observed**: A node starts virtual if created from an owner
   reference before the informer has seen the object. Once observed via an
   informer event, `virtual` is set to false and never reverts.

---

## Core behaviours

### B1: Object with all owners dangling is deleted

When every ownerReference on an object points to a non-existent owner, the
object is deleted.

**Path**: `attemptToDeleteItem` -> `classifyReferences` returns all dangling ->
fall-through to default case -> `deleteObject` with propagation policy derived
from existing finalizers.

**Propagation policy selection**:
- `FinalizerOrphanDependents` present -> `DeletePropagationOrphan`
- `FinalizerDeleteDependents` present -> `DeletePropagationForeground`
- Neither -> `DeletePropagationBackground`

### B2: Object with at least one solid owner is kept alive

If at least one ownerReference points to a live, non-deleting owner, the object
is not deleted. Any dangling or "waiting for dependents deletion" references are
cleaned up from the object's ownerReferences via patch.

**Path**: `attemptToDeleteItem` -> `classifyReferences` -> `len(solid) != 0` ->
patch to remove dangling + waitingForDependentsDeletion refs.

### B3: Foreground cascading deletion (owner waiting for dependents)

When an owner has `DeletionTimestamp != nil` and `FinalizerDeleteDependents`:

1. GraphBuilder sets `deletingDependents = true` on the node and enqueues all
   dependents + the owner to `attemptToDelete`.
2. For each blocking dependent (those with `BlockOwnerDeletion=true`), the GC
   enqueues them to `attemptToDelete`.
3. Once all blocking dependents are gone, the GC removes
   `FinalizerDeleteDependents` from the owner, allowing its final deletion.

**Cycle detection**: If a dependent is itself `isDeletingDependents()`, the GC
unblocks all of its `BlockOwnerDeletion` references first (sets them to false)
before issuing a foreground delete. This is an approximate heuristic with
acknowledged false positives (comment at `garbagecollector.go:600`).

### B4: Orphan deletion

When an owner has `DeletionTimestamp != nil` and `FinalizerOrphanDependents`:

1. GraphBuilder enqueues the owner to `attemptToOrphan`.
2. The orphan worker snapshots all dependents.
3. In parallel goroutines, each dependent has the owner's UID removed from its
   ownerReferences via strategic merge patch (with UID precondition).
4. After all dependents are processed, `FinalizerOrphanDependents` is removed
   from the owner.
5. If any dependent patch fails (except NotFound), the whole operation is
   requeued.

### B5: Owner reference cleanup for waitingForDependentsDeletion

When an object has some owners that are `waitingForDependentsDeletion` (i.e.
`DeletionTimestamp` set + `FinalizerDeleteDependents`) and the object itself has
dependents, the object is deleted with foreground propagation. This propagates
the cascading deletion downward through the ownership tree.

**Path**: `attemptToDeleteItem` -> `len(waitingForDependentsDeletion) != 0 &&
item.dependentsLength() != 0` -> foreground delete.

### B6: Object already being deleted is skipped

If `item.isBeingDeleted() && !item.isDeletingDependents()`, `attemptToDeleteItem`
returns nil immediately. The object is on a one-way path to final deletion; the
GC will process its dependents when the delete event arrives.

### B7: Object with no ownerReferences is ignored

If `len(ownerReferences) == 0` after fetching the live object,
`attemptToDeleteItem` returns nil. Objects without owners are not subject to
garbage collection.

---

## Virtual node behaviours

Virtual nodes are the most complex part of the system and the primary source of
edge cases.

### V1: Virtual node creation

When a dependent is added to the graph and its ownerReference points to a UID
not yet in the graph, a virtual node is created:

```go
ownerNode = &node{
    identity: objectReference{
        OwnerReference: ownerReferenceCoordinates(owner),
        Namespace:      dependent.identity.Namespace,
    },
    virtual: true,
}
```

The virtual node's coordinates (apiVersion, kind, name, namespace) come from the
dependent's ownerReference. The namespace is inherited from the dependent (since
ownerReferences don't carry namespace).

The virtual node is enqueued to `attemptToDelete`, which will GET the object
from the API server to determine if it exists.

### V2: Virtual node observation

When the real object arrives via informer, `processGraphChanges` detects the
existing virtual node and marks it as observed. If the observed identity differs
from the virtual identity (e.g. different apiVersion or kind), the node is
cloned with the correct identity. Dependents whose ownerReferences don't match
the observed identity are enqueued to `attemptToDelete` for validation.

### V3: Virtual delete event

When `attemptToDeleteItem` processes a virtual node and the API server returns
404 (or UID mismatch), a virtual delete event is enqueued to `graphChanges`:

```go
gc.dependencyGraphBuilder.enqueueVirtualDeleteEvent(item.identity)
```

This is distinct from a real delete event (from an informer) and carries
`event.virtual = true`.

### V4: Virtual delete event processing - both virtual

When a virtual delete event arrives for a node that is still virtual in the
graph:

1. **Partition dependents** into those whose ownerReferences match the deleted
   identity vs those that don't.
2. If **all match**: remove the node from the graph, enqueue all dependents.
3. If **some don't match**: keep the virtual node in the graph. Mark the deleted
   identity as absent in the cache. Enqueue matching dependents for deletion.
   Find an alternate identity from the non-matching dependents and replace the
   virtual node's identity with it. Re-enqueue the replacement node.

The alternate identity search uses lexicographic ordering to ensure deterministic
cycling through possible identities, preventing infinite loops.

### V5: Virtual delete event processing - real node exists

When a virtual delete event arrives but the node has been observed (is real):

- If the delete event's identity matches the real node: remove normally.
- If it doesn't match: don't remove the real node. Instead, enqueue any
  dependents whose ownerReferences match the (now-confirmed-absent) deleted
  identity.

### V6: Virtual node requeue on success

Even after `attemptToDeleteItem` succeeds for a virtual node, if the node is
still unobserved (`!n.isObserved()`), it is requeued. This prevents virtual
nodes from getting stuck in the graph if they were added and removed during a
watch interruption (k8s issue #56121).

### V7: Virtual node skip conditions

`attemptToDeleteWorker` skips processing a virtual node if:
- It no longer exists in the graph (was cleaned up)
- It has been marked as observed (real object arrived via informer)

---

## Absent owner cache behaviours

### C1: Cache lookup order

`isDangling` checks the cache in two steps:
1. Cluster-scoped key: `{ownerRef coordinates, Namespace: ""}`
2. Namespaced key: `{ownerRef coordinates, Namespace: item.identity.Namespace}`

This handles both cluster-scoped and namespace-scoped owners.

### C2: Cache population

The cache is populated when:
- `isDangling` gets a 404 from the API server
- `isDangling` gets a UID mismatch (object reincarnated)
- `processGraphChanges` removes a node that has dependents
- `processGraphChanges` processes a virtual delete for a confirmed-absent identity

### C3: Cache is an LRU with 500 entries

The cache is never explicitly invalidated. It relies on LRU eviction. A stale
cache entry (owner came back) is corrected because `attemptToDeleteItem` always
fetches the live object from the API server and re-checks ownerReferences.

### C4: No removal mechanism

There is no `Delete` method on `ReferenceCache`. Once an owner is cached as
absent, it stays until LRU eviction. This is safe because the delete path always
verifies against the live API state, but it means the cache can cause extra
`attemptToDelete` processing.

---

## Monitor lifecycle behaviours

### M1: Discovery-driven monitor sync

`Sync()` periodically discovers resources from the API server and compares with
the current monitor set. New monitors are created for new resources; monitors
for removed resources are stopped.

### M2: Partial discovery failure handling

If some API groups fail discovery, existing synced monitors for those groups are
preserved rather than removed. This prevents mass deletion of GC knowledge
during transient API server issues.

### M3: Monitor start gating

`startMonitors()` waits for `informersStarted` (a channel closed after all
controllers are initialized) before actually starting monitors. This prevents
events arriving before the system is ready.

### M4: Stop-before-restart safety

`stopMonitors()` panics if called while `running == true`. The sequence in
`Run()` is: set `running = false` (under lock), then `stopMonitors()`. This
prevents new monitors from being started concurrently with shutdown.

### M5: KCP single-shot sync

KCP's `ResyncMonitors()` is a single-shot version of `Sync()`. It always
compares against an empty `oldResources` map, so it always triggers a full sync.
The `oldResources` local variable assignment at the end is dead code (local to
the closure). The 30-second timeout is hard-coded.

---

## Concurrency model

### Threading

| Thread(s) | What it does | What it reads/writes |
|-----------|-------------|---------------------|
| GraphBuilder (1) | Processes `graphChanges` queue | **Writes**: `uidToNode`, all node fields. **Reads**: node dependents (for enqueuing) |
| Delete workers (N) | Processes `attemptToDelete` queue | **Reads**: node flags, identity, owners, dependents. **Writes**: API server (delete/patch) |
| Orphan workers (N) | Processes `attemptToOrphan` queue | **Reads**: node dependents, identity. **Writes**: API server (patch) |
| Sync goroutine (1) | Periodic discovery + monitor resync | **Writes**: monitor set |

### Lock structure

Each `node` has four independent RWMutex fields:
- `dependentsLock` - protects `dependents` map
- `beingDeletedLock` - protects `beingDeleted`
- `virtualLock` - protects `virtual`
- `deletingDependentsLock` - protects `deletingDependents`

The graph-level `concurrentUIDToNode` has its own RWMutex.

**Critical design note**: Node fields are **not atomically consistent** with
each other. A worker reading `isBeingDeleted()` and then `isDeletingDependents()`
may see a state that never existed as a whole. The comment in `graph.go:60` warns
about this. The system tolerates this by making deletion decisions idempotent and
re-verifying state via API server calls.

### Lock-free reads of `owners`

`node.owners` has no lock. It is written only by the single-threaded
`processGraphChanges` and read by workers. This is a data race according to the
Go memory model, but is treated as safe because:
- Writes are to the slice header (pointer + length), not individual elements
- The GraphBuilder replaces the slice atomically from its perspective
- Workers use the owners list only as hints; they verify against live state

This is technically incorrect but works in practice due to x86 memory model
guarantees. A replacement implementation should fix this.

---

## Edge cases catalogue

### E1: Object reincarnation (UID recycling)

**Scenario**: Object A (uid-1) is deleted. New object B gets the same name but
different UID (uid-2).

**Handling**: `attemptToDeleteItem` fetches the live object. If
`latest.GetUID() != item.identity.UID`, it enqueues a virtual delete event for
the old UID and returns `enqueuedVirtualDeleteEventErr`.

`isDangling` similarly checks UID: if the fetched owner has a different UID than
the reference, the reference is treated as dangling.

### E2: Cluster-scoped child with namespaced owner reference

**Scenario**: A cluster-scoped object has an ownerReference pointing to a
namespaced type.

**Handling**: `isDangling` checks `len(item.identity.Namespace) == 0 &&
namespaced` and returns `namespacedOwnerOfClusterScopedObjectErr`. This is a
non-retryable error (the worker forgets the item).

`getObject` also guards against this: if the type is namespaced but no namespace
coordinate is available, it returns the same marker error.

### E3: Cross-namespace owner reference

**Scenario**: Object in namespace A has ownerReference pointing to an object
that exists in namespace B.

**Handling**: The GC has no way to discover this directly (ownerReferences don't
carry namespace). It infers the owner's namespace from the dependent's namespace.
If the graph already has the owner observed in a different namespace, the
GraphBuilder detects the namespace mismatch in `addDependentToOwners` and:
- Emits a warning event (`OwnerRefInvalidNamespace`)
- Enqueues the dependent to `attemptToDelete` for validation

The dependent will be treated as having a dangling reference (the owner won't be
found in the dependent's namespace).

### E4: Watch interruption and missed delete events

**Scenario**: While a watch connection is interrupted, an object is created and
deleted. The informer may receive a synthetic delete via `DeletedFinalStateUnknown`.

**Handling**: The DeleteFunc unwraps `cache.DeletedFinalStateUnknown`. For the
create-then-delete-during-interruption case, the virtual node requeue behaviour
(V6) ensures the node eventually gets cleaned up.

### E5: Conflict on delete (resourceVersion mismatch)

**Scenario**: Object is rapidly updated by another controller while GC tries to
delete it.

**Handling**: `deleteObject` receives a 409 Conflict. It then:
1. GETs the live object
2. If NotFound: success (someone else deleted it)
3. If live and ownerReferences unchanged: retry delete without resourceVersion
   precondition
4. Otherwise: return the conflict error (will be requeued)

This prevents rapid updates to non-ownerReference fields from starving the GC.

### E6: Conflict on finalizer removal

`removeFinalizer` uses `retry.RetryOnConflict` with `DefaultBackoff` (up to 5
retries). If the finalizer is already gone, it returns nil. If the object is
NotFound, it returns nil.

### E7: Strategic merge patch unsupported

`patch()` first tries strategic merge patch. If the API server returns
`UnsupportedMediaType` (415), it falls back to JSON merge patch. All patch
operations have both SMP and JMP implementations.

### E8: Dependent has multiple ownerReferences to the same UID

`partitionDependents` handles this: a dependent with multiple ownerReferences
for the same UID (with different coordinates) can end up in both the "matching"
and "non-matching" lists.

### E9: Blocking dependent is itself deleting dependents

**Scenario**: Owner A is foreground-deleting. Dependent B has
`BlockOwnerDeletion=true` and is itself foreground-deleting its own dependents.

**Handling**: The GC detects `dep.isDeletingDependents()` and unblocks B's
ownerReferences (sets `BlockOwnerDeletion=false`) before issuing a foreground
delete. This breaks potential cycles.

**Limitation**: The check has false positives (acknowledged in comment). Multiple
concurrent workers may race on the detection.

### E10: REST mapper doesn't know the type yet

**Scenario**: A CRD is installed but discovery hasn't picked it up yet.

**Handling**: `apiResource` returns a `restMappingError`. The worker logs at V(5)
and requeues the item. On the next discovery sync, the monitor for the new
resource type will be created.

### E11: Owner exists but has different coordinates than expected

**Scenario**: Virtual node created with coordinates from dependent's
ownerReference, but the real object has different apiVersion (e.g. v1beta1 vs v1).

**Handling**: When the real object is observed via informer, `processGraphChanges`
detects `observedIdentity != existingNode.identity`. It:
1. Clones the node with the correct identity
2. Enqueues dependents whose ownerReferences don't match the observed identity
3. Those dependents are validated via `attemptToDelete`

### E12: Empty discovery results

If `GetDeletableResources` returns zero resources, the GC skips the sync
entirely and increments the error counter. This prevents the GC from removing
all monitors during a complete discovery failure.

### E13: Race between virtual node processing and real observation

**Scenario**: Virtual node for uid-1 is in `attemptToDelete` queue. Before the
worker processes it, the real object arrives via informer and the node is marked
observed.

**Handling**: `attemptToDeleteWorker` checks `n.isObserved()` on the node from
the graph. If the graph's node is now observed, the worker skips the virtual
node (returns `forgetItem`).

### E14: Delta FIFO combining create + delete

The delta FIFO may coalesce a create and delete into a single event. The
`processTransitions` call after handling add/update events accounts for this by
checking deletion state transitions even on add events.

### E15: Orphan worker processes stale dependent list

`attemptToOrphanWorker` snapshots dependents under `dependentsLock.RLock()`.
Between the snapshot and the actual patch operations, dependents may be added or
removed. This is safe because:
- Removing an ownerReference from a non-existent dependent returns NotFound
  (ignored)
- Removing an ownerReference that doesn't exist is a no-op for strategic merge
  patch
- New dependents will be handled in a subsequent cycle

### E16: `unblockOwnerReferencesJSONMergePatch` uses node.owners not live state

The JMP version of `unblockOwnerReferences` reads `n.owners` (the in-graph
snapshot) to construct the patch, while the SMP version also reads `n.owners`.
Neither reads live state from the API. If the owners have changed since the
graph was updated, the patch may be stale. This is mitigated by the
resourceVersion in the patch body causing a conflict, which triggers a requeue.

### E17: `processGraphChanges` defer of `dependentsLock.RUnlock()`

In the delete event handling path, `existingNode.dependentsLock.RLock()` is
acquired and deferred for unlock. This means the lock is held while iterating
owners and potentially enqueuing items. The function returns `true` (continue
processing) while still holding this lock via defer. This is intentional but
subtle: it prevents dependents from being modified during the enqueue pass.

---

## Complexity hotspots

These areas are the primary sources of complexity and are the main candidates
for simplification in a rewrite:

### 1. Virtual node coordinate resolution (`graph_builder.go:809-861`, `956-1010`)

The algorithm for handling disagreeing coordinates among dependents of a virtual
node is ~100 lines of intricate logic. It involves:
- Partitioning dependents by coordinate match
- Finding alternate identities via lexicographic ordering
- Handling cluster-scoped vs namespaced inference
- Creating replacement virtual nodes

**Why it exists**: OwnerReferences carry apiVersion/kind/name but not namespace.
Multiple dependents may reference the same UID with different coordinates (e.g.
v1 vs v2). The GC needs to try all possible coordinate combinations to locate
the owner.

**Simplification opportunity**: If the new GC can make one API call per UID
(e.g. a metadata-only get-by-UID) rather than get-by-name-in-namespace, the
entire coordinate resolution system becomes unnecessary.

### 2. Three-way ownerReference classification (`garbagecollector.go:464-487`)

The solid/dangling/waitingForDependentsDeletion classification requires one API
call per ownerReference. The switch statement that follows has three cases that
are easy to confuse.

**Simplification opportunity**: The three categories map to three deletion
actions. A clearer model might be: for each ownerReference, compute a verdict
(`keep`, `clean`, `cascade`), then combine verdicts.

### 3. Patch dual-path (SMP + JMP fallback)

Every patch operation has two implementations (strategic merge patch + JSON
merge patch fallback). The JMP versions need to fetch live state from the API
server to construct a full replacement.

**Simplification opportunity**: If the target environment supports strategic merge
patch for all types (which is true for all built-in types and CRDs), the JMP
fallback can be removed.

### 4. Node lock granularity (`graph.go:63-83`)

Four separate RWMutex fields on each node, with technically racy reads of
`owners`. The lock documentation warns about inconsistent reads across fields.

**Simplification opportunity**: A single RWMutex per node (or making the
GraphBuilder communicate decisions to workers via the work queue items rather
than shared mutable state) would eliminate this complexity.

### 5. `getMetadata` cache-then-API pattern (`patch.go:34-63`)

The `getMetadata` function tries to read from the informer's local store first,
then falls back to an API call. It reaches into the GraphBuilder's monitor map
and store directly.

**Simplification opportunity**: Always use the metadata client. The informer
cache optimization adds code complexity for a minor latency benefit.

---

## Interaction with the API server

| Operation | When | Preconditions |
|-----------|------|--------------|
| `GET metadata` | `attemptToDeleteItem` (check object), `isDangling` (check owner) | None |
| `DELETE` | `attemptToDeleteItem` (delete orphaned object) | UID + ResourceVersion preconditions |
| `PATCH` (SMP/JMP) | Remove dangling ownerRefs, unblock BlockOwnerDeletion, orphan dependents | UID precondition (in patch body) |
| `PATCH` (MergePatch) | Remove finalizer | ResourceVersion precondition |
| Discovery | `Sync`/`ResyncMonitors` | None |

---

## KCP-specific notes

1. **`ResyncMonitors`**: One-shot sync instead of periodic. The `oldResources`
   variable is always empty, making the `reflect.DeepEqual` check always false
   (always triggers sync). The final `oldResources = newResources` is dead code.

2. **Hard-coded 30s sync timeout**: vs upstream's configurable `period` parameter.

3. **No periodic re-sync**: KCP relies on the informer framework for ongoing
   updates rather than periodic discovery re-sync. This means CRDs
   added after initial startup won't be picked up unless `ResyncMonitors` is
   called again.

---

## State machine per node

```
                    ┌──────────┐
                    │  (absent)│
                    └────┬─────┘
                         │ dependent observed with ownerRef to this UID
                         v
                    ┌──────────┐
                    │  virtual │──── attemptToDelete verifies existence
                    └────┬─────┘
                         │ informer ADD event
                         v
                    ┌──────────┐
                    │ observed │
                    └────┬─────┘
                         │ DeletionTimestamp set
                         v
               ┌─────────────────────┐
               │  beingDeleted       │
               │  (may also set      │
               │  deletingDependents │
               │  if has the         │
               │  finalizer)         │
               └─────────┬───────────┘
                         │ final delete event from informer
                         v
                    ┌──────────┐
                    │ removed  │──── dependents enqueued to attemptToDelete
                    └──────────┘
```

---

## Behavioural contract summary

For a rewrite to have feature parity, it must implement:

1. **Dependency graph maintenance**: Track ownerReference edges between objects.
2. **Garbage collection**: Delete objects when all owners are absent.
3. **Dangling reference cleanup**: Remove stale ownerReferences while keeping
   the object alive if it has surviving owners.
4. **Foreground cascading delete**: When owner has `FinalizerDeleteDependents`,
   delete blocking dependents first, then remove the finalizer.
5. **Orphaning**: When owner has `FinalizerOrphanDependents`, remove
   ownerReferences from dependents, then remove the finalizer.
6. **Cycle detection**: Break potential deadlocks when foreground-deleting
   objects form cycles via `BlockOwnerDeletion`.
7. **Virtual node management**: Handle forward references to not-yet-observed
   owners, including coordinate disagreements.
8. **Conflict tolerance**: Handle concurrent modifications, UID reincarnation,
   and watch interruptions gracefully.
9. **Partial failure tolerance**: Continue operating when some API groups are
   unavailable.
10. **Dynamic resource discovery**: Add/remove monitors as CRDs appear/disappear.
11. **Namespace scoping**: Correctly handle cluster-scoped vs namespaced objects
    and cross-namespace reference detection.
12. **Debug introspection**: DOT-format graph dump via HTTP.
