# Comprehensive E2E Test Scenarios for kcp GC

Organized by behaviour from `GC_ANALYSIS.md`. Each scenario is a single test
function. Where a behaviour can be exercised with different resource kinds
(built-in, CRD, bound API type), the kind variant is noted. Tests that validate
kcp-specific multi-workspace properties are marked with **(kcp)**.

---

## 1. Core deletion behaviours

### 1.1 Background cascading deletion (B1)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.1.1 | Single owner, single dependent | Owner deleted → dependent GC'd. Baseline. |
| 1.1.2 | Single owner, multiple dependents | Owner deleted → all dependents GC'd. |
| 1.1.3 | All owners dangling | Object with N owners, all deleted → object GC'd. |
| 1.1.4 | Non-existent owner at creation | Object created with ownerRef to fabricated UID → GC'd. |
| 1.1.5 | Re-added dangling ref after orphan | Object orphaned, then patched to re-add stale ownerRef → GC'd. |

### 1.2 Dangling reference cleanup (B2)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.2.1 | One of two owners deleted | Dependent survives, dangling ownerRef patched out, surviving ref intact. |
| 1.2.2 | Two of three owners deleted | Dependent survives with one remaining ownerRef. Both dangling refs removed. |

### 1.3 Foreground cascading deletion (B3)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.3.1 | Foreground delete, blocking dependent | Owner enters `deletingDependents` state. Blocking dependent deleted first. Owner finalized after. |
| 1.3.2 | Foreground delete, blocking dependent with finalizer | Owner blocked until dependent's finalizer is removed. Removing finalizer unblocks the chain. |
| 1.3.3 | Foreground delete, non-blocking dependent with finalizer | Owner is NOT blocked — non-blocking dependents don't gate finalizer removal. Owner deleted while dependent still exists (held by its own finalizer). |
| 1.3.4 | Foreground delete, non-blocking dependent (unset) | `BlockOwnerDeletion` not set at all (nil) — same as false: owner not blocked. |
| 1.3.5 | Foreground delete, mixed blocking and non-blocking | Owner has both blocking and non-blocking dependents. Owner waits only for blocking ones. |
| 1.3.6 | Foreground delete, dependent with solid second owner | Dependent has blocking ref to foreground-deleted owner AND a solid second owner. GC cleans the dangling ref; dependent survives. Owner is unblocked because the dependent's blocking ref is removed. |
| 1.3.7 | Double foreground delete | Foreground-delete an object twice. Second delete does not re-add `FinalizerDeleteDependents`. Object is deleted once custom finalizer is removed. |

### 1.4 Orphan deletion (B4)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.4.1 | Orphan propagation, single dependent | Owner deleted with `PropagationPolicy=Orphan`. Dependent survives with ownerRef removed. |
| 1.4.2 | Orphan propagation, multiple dependents | All dependents survive with ownerRef removed. |
| 1.4.3 | Orphan finalizer in chain | A→B(orphan finalizer)→C. Delete A. B is cascade-deleted but its orphan finalizer causes C to be orphaned (survives with empty ownerRefs). |

### 1.5 Foreground cascading through ownership tree (B5)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.5.1 | WaitingForDependentsDeletion owner with dependents | Object's owner is in foreground deletion. Object itself has dependents. Object is foreground-deleted, propagating the cascade downward. |

### 1.6 Object with no ownerReferences (B7)

| ID | Scenario | Description |
|----|----------|-------------|
| 1.6.1 | Standalone object not collected | Object without ownerReferences is never deleted by GC, even when GC is actively collecting other objects in the same namespace. |

---

## 2. Ownership chains and graphs

| ID | Scenario | Description |
|----|----------|-------------|
| 2.1 | Deep chain (A→B→C) | Delete A → entire chain GC'd. |
| 2.2 | Diamond (A→B, A→C, B→D, C→D) | Delete A → B, C, D all GC'd. D waits for both parents. |
| 2.3 | Fan-out (A owns B1..B10) | Delete A → all 10 dependents GC'd. |
| 2.4 | Fan-in (B1..B3 all own C) | Delete B1 and B2 → C survives (B3 alive). Delete B3 → C GC'd. |

---

## 3. Cross-type ownership

| ID | Scenario | Description |
|----|----------|-------------|
| 3.1 | CRD owner, core dependent | Sheriff(CRD) owns ConfigMap. Delete Sheriff → ConfigMap GC'd. |
| 3.2 | Core owner, CRD dependent | ConfigMap owns Sheriff(CRD). Delete ConfigMap → Sheriff GC'd. |
| 3.3 | Bidirectional mixed chain | ConfigMap→Sheriff(CRD)→Secret. Delete ConfigMap → chain GC'd. |

---

## 4. CRD-specific behaviours

| ID | Scenario | Description |
|----|----------|-------------|
| 4.1 | Multi-version CRD, owner v1 | Owner created as v1, dependents as v1 and v2. Delete owner → both GC'd. |
| 4.2 | Multi-version CRD, owner v2 | Owner created as v2, dependents as v1 and v2. Delete owner → both GC'd. |
| 4.3 | Cluster-scoped CRD | Cluster-scoped CR owns another cluster-scoped CR. Delete owner → dependent GC'd. |
| 4.4 | Same CRD in different workspaces **(kcp)** | Two workspaces install the same CRD independently. GC in each is isolated. Delete in ws1 doesn't affect ws2. |
| 4.5 | CRD deletion cascades to instances and their dependents | Delete the CRD itself. All CR instances are deleted. Core resources owned by those CRs are subsequently GC'd. |
| 4.6 | CRD installed after GC start | Install CRD, create owner CR and dependent ConfigMap, delete owner → ConfigMap GC'd. Verifies dynamic monitor discovery. |

---

## 5. Namespace scoping

| ID | Scenario | Description |
|----|----------|-------------|
| 5.1 | Cross-namespace ownerRef (invalid) | Object in ns-A has ownerRef with UID of object in ns-B but wrong Kind. GC detects namespace mismatch, deletes dependent, emits `OwnerRefInvalidNamespace` warning event. |
| 5.2 | Valid same-namespace ownerRef unaffected by cross-ns invalid | Alongside 5.1, valid children in the correct namespace are not affected. |
| 5.3 | Late-arriving invalid cross-ns ref | After GC processes an initial batch of invalid cross-ns refs, a new one is created. It is also collected. |

---

## 6. Unknown and unresolvable owner types

| ID | Scenario | Description |
|----|----------|-------------|
| 6.1 | OwnerRef to unknown apiVersion/kind | Object with ownerRef to `nonexistent.example.com/v1 Phantom` — GC cannot resolve the type, so the object is preserved. |
| 6.2 | Known type confirms GC is active | Alongside 6.1, an object with ownerRef to a known type with fake UID is GC'd, proving the GC is running. |

---

## 7. APIExport/APIBinding — bound types (kcp)

These tests exercise the GC with types that exist in a workspace via APIBinding
rather than CRD installation.

| ID | Scenario | Description |
|----|----------|-------------|
| 7.1 | Bound type owner, core dependent | Cowboy(bound) owns ConfigMap. Delete Cowboy → ConfigMap GC'd. |
| 7.2 | Core owner, bound type dependent | ConfigMap owns Cowboy(bound). Delete ConfigMap → Cowboy GC'd. |
| 7.3 | Bound type owns bound type | Cowboy(bound) owns another Cowboy(bound). Delete owner → dependent GC'd. |
| 7.4 | Deep chain with bound type in middle | ConfigMap → Cowboy(bound) → Secret. Delete ConfigMap → all GC'd. |
| 7.5 | Bound type dangling ref cleanup | ConfigMap owned by Cowboy(bound) + ConfigMap. Delete Cowboy. ConfigMap survives with only the ConfigMap ownerRef remaining. |
| 7.6 | Bound type foreground delete with blocking dependent | Foreground-delete Cowboy(bound). Blocking dependent (ConfigMap with finalizer) holds owner in `deletingDependents`. Remove finalizer → both deleted. |
| 7.7 | Bound type orphan | Delete Cowboy(bound) with `PropagationPolicy=Orphan`. Dependent ConfigMaps survive with ownerRefs removed. |
| 7.8 | Non-existent bound type owner | ConfigMap with ownerRef to Cowboy(bound) that was never created (fake UID) → GC'd. |
| 7.9 | Multiple workspaces, same binding, same names | Two workspaces bind same APIExport. Same-named Cowboy→ConfigMap in each. Delete in ws1 → ws2 unaffected. |

---

## 8. APIBinding deletion interaction with GC (kcp)

| ID | Scenario | Description |
|----|----------|-------------|
| 8.1 | APIBinding deleted, dependents of bound resources GC'd | Cowboy(bound) owns ConfigMap. Delete APIBinding. APIBinding deletion controller removes Cowboys. GC then collects the orphaned ConfigMap. |
| 8.2 | APIBinding deleted in one workspace, other workspace unaffected | Two workspaces bind same export. Delete APIBinding in ws1. ws1's dependents GC'd. ws2's resources untouched. |

---

## 9. Workspace isolation (kcp)

| ID | Scenario | Description |
|----|----------|-------------|
| 9.1 | Same-named objects in different workspaces | Two workspaces, each with ConfigMap `owner` and ConfigMap `child`. Delete `owner` in ws1 → `child` in ws1 GC'd. ws2 completely unaffected. |
| 9.2 | Same-named CRDs in different workspaces | Two workspaces install the same CRD group independently. GC operates on each independently. |

---

## 10. Propagation policy selection

| ID | Scenario | Description |
|----|----------|-------------|
| 10.1 | Default (no finalizer) → background | Delete owner with no explicit policy and no finalizers. Dependents are background-deleted. |
| 10.2 | Explicit foreground | Delete with `PropagationPolicy=Foreground`. Owner gets `FinalizerDeleteDependents`, waits for dependents. |
| 10.3 | Explicit orphan | Delete with `PropagationPolicy=Orphan`. Dependents survive, ownerRefs removed. |
| 10.4 | Explicit background | Delete with `PropagationPolicy=Background`. Owner deleted immediately, dependents collected asynchronously. |

---

## Summary count

| Category | Tests |
|----------|-------|
| 1. Core deletion behaviours | 18 |
| 2. Ownership chains/graphs | 4 |
| 3. Cross-type ownership | 3 |
| 4. CRD-specific | 6 |
| 5. Namespace scoping | 3 |
| 6. Unknown owner types | 2 |
| 7. Bound types (APIBinding) | 9 |
| 8. APIBinding deletion + GC | 2 |
| 9. Workspace isolation | 2 |
| 10. Propagation policy selection | 4 |
| **Total** | **53** |

---

## Suggested file organization

| File | Contents |
|------|----------|
| `cascading_test.go` | 1.1.x, 1.2.x, 1.6.x, 2.x, 10.x — basic ownership and cascading |
| `foreground_test.go` | 1.3.x, 1.5.x — foreground deletion and BlockOwnerDeletion |
| `orphan_test.go` | 1.4.x — orphan propagation |
| `cross_type_test.go` | 3.x — mixed CRD/core ownership |
| `crd_test.go` | 4.x — CRD lifecycle, multi-version, cluster-scoped |
| `namespace_test.go` | 5.x — cross-namespace validation |
| `unknown_owner_test.go` | 6.x — unresolvable types |
| `bound_type_test.go` | 7.x — APIExport/APIBinding bound type GC |
| `apibinding_deletion_test.go` | 8.x — APIBinding deletion controller handoff |
| `workspace_isolation_test.go` | 9.x — cross-workspace isolation |
| `support.go` | Shared helpers, embedded YAML, CRD bootstrapping |

## Notes on deduplication

Many scenarios share setup (create workspace, create owner, create dependent,
delete, assert). The key to avoiding duplication is:

- A **helper that creates the provider workspace + APIExport + binding** for all
  section 7/8 tests, returning a ready-to-use consumer path and typed client.
- A **generic ownership assertion helper**: `waitForDeletion(t, client, name)`
  and `assertSurvivesWithOwnerRefs(t, client, name, expectedUIDs)`.
- **Table-driven tests** within a single function where only the resource kinds
  or policies vary (e.g., 10.1–10.4 are one test with subtests; 1.1.1–1.1.4 can
  share setup).
- Section 3 (cross-type) can be table-driven with `(ownerGVR, dependentGVR)`
  pairs.
