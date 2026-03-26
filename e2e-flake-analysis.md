# E2E Flake Analysis

Analysis of all 55 e2e test files for patterns that could cause flaky tests due to
informer lag, timing, race conditions, and similar issues.

## HIGH Severity (likely to flake in CI)

### 1. `time.Sleep` replacing proper polling

| # | File | Line | Description |
|---|------|------|-------------|
| 1 | `virtual/apiexport/authorizer_test.go` | 809 | `time.Sleep(5 * time.Second)` — comment says "we need to wait for the APIBinding update to reach the informer in the authorizer". Immediately after, a delete is expected to be Forbidden. Under load 5s may not suffice; a polling loop checking the actual authorization outcome is needed. |
| 2 | `authorizer/authorizationorder_test.go` | 136 | `time.Sleep(3 * time.Second)` — workaround for server shutdown race (#3488). Fragile timing assumption that could still flake on slow machines. |

### 2. Audit log file race / order-dependent parsing

| # | File | Lines | Description |
|---|------|-------|-------------|
| 3 | `audit/audit_log_test.go` | 64-75 | Reads audit log file with `os.ReadFile` without polling for existence or completeness. Assumes `lines[0]` is the request event and `lines[1]` is the response event. If the audit backend hasn't flushed yet, the file won't exist; if concurrent tests generate audit events, the line ordering will be wrong. |

### 3. Watch started after resource creation (missed events)

| # | File | Lines | Description |
|---|------|-------|-------------|
| 4 | `virtualresources/cachedresources/vr_cachedresources_test.go` | 311-343 | Watch is created at line 311, then a Sheriff is created at line 343. No list+watch pattern is used, so if the watch starts after the Sheriff is written to etcd, the Added event is missed. The test then waits for `counter == 2` and times out. |
| 5 | `virtual/terminatingworkspaces/virtualworkspace_test.go` | 687 | `time.Tick(wait.ForeverTestTimeout)` in a `select` inside a watch loop. If the Modified event for a terminating workspace arrives late (e.g., controller lag), the test fatals. No backpressure or list fallback. |

### 4. Non-idempotent Create inside Eventually polling loop

| # | File | Lines | Description |
|---|------|-------|-------------|
| 6 | `apibinding/apibinding_webhook_test.go` | 227-231 | `require.Eventually` calls `Cowboys.Create()` and immediately `require.NoError(t, err)` inside the polling function. If create succeeds but the webhook counter hasn't incremented yet, the next poll attempt calls Create again, gets "AlreadyExists", and `require.NoError` fatals mid-poll. |
| 7 | `mounts/mounts_machinery_test.go` | 112-116 | `CreateResourceFromFS` inside Eventually. If the first attempt creates the resource but the polling condition hasn't been met, the next attempt will try to create it again and get "AlreadyExists". |
| 8 | `authorizer/workspace_test.go` | 223-236 | `ClusterRoles.Create()` and `ClusterRoleBindings.Create()` inside Eventually loops. If create succeeds but the poll doesn't notice, the next attempt will fail with "AlreadyExists". |

### 5. HTTP server startup race

| # | File | Lines | Description |
|---|------|-------|-------------|
| 9 | `proxy/proxy_test.go` | 66-82 | HTTP server started in a goroutine with no synchronization. The test makes requests to port 2443 without verifying the server is listening. Classic race: request can arrive before `ListenAndServeTLS` binds. |

### 6. Cascading delete timeouts (workspace deletion)

| # | File | Lines | Description |
|---|------|-------|-------------|
| 10 | `reconciler/workspacedeletion/controller_test.go` | 175-178, 244-277 | Polls for nested workspace deletion to complete with `wait.ForeverTestTimeout`. Cascading deletes (workspace -> logical cluster -> namespaces -> resources) involve multiple async controllers. Under load, 30s may not suffice. |

### 7. Webhook call race

| # | File | Lines | Description |
|---|------|-------|-------------|
| 11 | `apibinding/apibinding_webhook_test.go` | 200-206 | Polls `testWebhooks[sourcePath].Calls() >= 1` inside an Eventually loop that also calls `Create()`. The webhook counter and the create are coupled: if create succeeds but the webhook goroutine hasn't incremented the counter yet, the condition is false and the next attempt double-creates. |

## MEDIUM Severity (flakes under load)

### 8. Get-then-Update without `retry.RetryOnConflict`

| # | File | Lines | Description |
|---|------|-------|-------------|
| 12 | `workspace/inactive_test.go` | 52-57, 69-71 | Gets LogicalCluster, mutates annotations, then Updates without conflict retry. Line 71 reuses the old `lc` from the first Get, which may be stale after the first Update + controller reconciliation. |
| 13 | `workspace/deletion_test.go` | 66-73, 103-110 | Get-Modify-Update inside `EventuallyWithT` provides some retry, but doesn't use `retry.RetryOnConflict` — a concurrent controller update will cause a conflict error that wastes an iteration. |
| 14 | `apibinding/apibinding_permissionclaims_test.go` | 98-107 | Fetches APIBinding, modifies PermissionClaims, and updates. A concurrent controller update between Get and Update causes a conflict. |
| 15 | `virtual/apiexport/binding_test.go` | 375-384, 543-564 | Get -> modify -> Update of APIExport/APIBinding without conflict retry. |
| 16 | `virtual/apiexport/virtualworkspace_test.go` | 376-391 | Same pattern for APIBinding update. |
| 17 | `virtual/replication/virtualworkspace_test.go` | 495-506 | `setSheriffLabels` does Get -> modify labels -> Update without conflict retry. |
| 18 | `authorizer/impersonate_test.go` | 66-73 | Gets Workspace, modifies Status.Phase, calls UpdateStatus inside Eventually. No `retry.RetryOnConflict`. |
| 19 | `apibinding/default_apibinding_test.go` | 184-213 | Gets APIExport and updates without conflict retry. |
| 20 | `reconciler/workspace/apibinding_selector_inheritance_test.go` | 157-168 | Gets WorkspaceType, modifies defaultAPIBindings, Updates without conflict retry. |

### 9. Authorization webhook restart race

| # | File | Lines | Description |
|---|------|-------|-------------|
| 21 | `authorizer/authorizationorder_test.go` | 56-62, 86-96 | Stops the "allow" webhook and immediately starts a "deny" webhook on the same port, then makes a request expecting it to be denied. No waiting for the new webhook to be fully listening, and no polling to confirm the deny behavior. |

### 10. Informer lag: assertion immediately after mutation without polling

| # | File | Lines | Description |
|---|------|-------|-------------|
| 22 | `authorizer/scopes_test.go` | 167-173 | Creates SubjectAccessReview and immediately asserts on response without polling. Authorization decision depends on RBAC informer state. |
| 23 | `homeworkspaces/home_workspaces_test.go` | 63-66 | Gets home workspace and directly asserts `Status.Phase == Ready` without polling. Controller may not have finished reconciliation. |
| 24 | `openapi/openapi_test.go` | 48-68 | Retrieves OpenAPI v3 paths immediately after workspace creation without polling. Discovery endpoint may not be populated yet. |
| 25 | `conformance/crd_partial_metadata_test.go` | 292-302 | Lists CRs and asserts `require.Len(list.Items, 1)` without polling. If the CRD isn't fully established, the list may be empty. |
| 26 | `apibinding/apibinding_test.go` | 338-341 | After creating a cowboy, immediately lists and asserts exactly 1 result without retry. Informer-backed list may lag. |
| 27 | `reconciler/workspacedeletion/controller_test.go` | 116-122 | After workspace phase is Ready, immediately polls for default namespace. Namespace creation is a separate async controller. |

### 11. RBAC informer lag

Many tests create ClusterRoles/ClusterRoleBindings and then immediately check
authorization with polling. While the Eventually loops mitigate this,
`wait.ForeverTestTimeout` (30s) may be tight under load when RBAC informers take
longer to sync.

| # | File | Lines | Description |
|---|------|-------|-------------|
| 28 | `authorizer/authorizer_test.go` | 114-118 | "Priming the authorization cache" with 2s timeout — too short under load. |
| 29 | `authorizer/workspace_test.go` | 80-91, 119-123, 139-143 | Polls workspace creation/access after RBAC changes. |
| 30 | `authorizer/serviceaccounts_test.go` | 247-253, 274-280, 343-360, 400-417 | Multiple RBAC propagation waits across workspaces. |
| 31 | `virtual/apiexport/virtualworkspace_test.go` | 156-164 | Polls user-1 list access after RBAC change. |
| 32 | `virtual/terminatingworkspaces/virtualworkspace_test.go` | 301-316 | SAR cache acknowledged as 30-second, but polling at 100ms. |

### 12. Discovery cache lag after APIBinding creation

Many tests create an APIBinding and then poll `ServerResourcesForGroupVersion` or
`ServerGroups` to confirm the bound API group appears. Discovery is cached and can
lag significantly.

| # | File | Lines | Description |
|---|------|-------|-------------|
| 33 | `apibinding/apibinding_test.go` | 295-302 | Polls discovery for cowboys group after binding. |
| 34 | `apibinding/cross_workspace_auth_test.go` | 208-215, 380-387, 568-575, 758-765 | Same pattern in 4 subtests. |
| 35 | `apibinding/maximalpermissionpolicy_authorizer_test.go` | 210-217, 354-366 | Polls discovery after binding and deletion. |
| 36 | `virtual/replication/virtualworkspace_test.go` | 196-213 | Discovery polling with 1s interval for virtual resources. |
| 37 | `virtualresources/cachedresources/vr_cachedresources_test.go` | 207-224 | Discovery polling for wildwest group. |

### 13. CRD serving lag (informer-based CRD serving)

| # | File | Lines | Description |
|---|------|-------|-------------|
| 38 | `conformance/cross_logical_cluster_list_test.go` | 202-211, 349-354 | Creates sheriffs after CRD bootstrap without checking CRD is Established. |
| 39 | `cache/replication_api_cache_test.go` | 104-108 | Creates instances immediately after CRD establishment check — discovery mapper may not be updated. |
| 40 | `quota/quota_test.go` | 268-293 | Bootstraps CRDs and immediately creates sheriffs without Established check. |

### 14. APIExportEndpointSlice not populated

| # | File | Lines | Description |
|---|------|-------|-------------|
| 41 | `apibinding/apibinding_logicalcluster_test.go` | 142-143 | Gets APIExportEndpointSlice in Eventually but accesses `Status.APIExportEndpoints[0]` without checking slice length — panic if empty. |
| 42 | `reconciler/apiexportendpointslice/apiexportendpointslice_test.go` | 269-333 | Multiple polls with tight timeouts for endpoint reconciliation under multi-shard setup. |

### 15. JWT/OIDC authenticator initialization lag

| # | File | Lines | Description |
|---|------|-------|-------------|
| 43 | `authentication/workspace_test.go` | 237-240, 320-330, 391-394, 623-630 | Polls access after JWT authenticator initialization. Authenticator init is async and OIDC discovery can be slow. |

### 16. Controller injection lag

| # | File | Lines | Description |
|---|------|-------|-------------|
| 44 | `authorizer/rootcacertconfigmap_test.go` | 65-78 | After creating namespace, polls for auto-injected `kube-root-ca.crt` ConfigMap. Injection depends on controller reconciliation. |

### 17. Non-idempotent Create inside Eventually (with GenerateName)

| # | File | Lines | Description |
|---|------|-------|-------------|
| 45 | `reconciler/apiexportendpointslice/apiexportendpointslice_test.go` | 119-126 | Create with `GenerateName` inside Eventually. Each retry creates a new object. |
| 46 | `reconciler/cachedresourceendpointslice/cachedresourceendpointslice_test.go` | 114-121, 147-154 | Same pattern — Create with `GenerateName` inside Eventually creates orphaned objects. |

### 18. Watch event timing in nested loops

| # | File | Lines | Description |
|---|------|-------|-------------|
| 47 | `virtual/terminatingworkspaces/virtualworkspace_test.go` | 768-780 | Waits for bookmark event with `time.After(wait.ForeverTestTimeout)`. If server is slow to send bookmark, test fails. |
| 48 | `virtual/initializingworkspaces/virtualworkspace_test.go` | 290 | `time.Tick` inside watcher loop — missed first event causes full timeout. |

### 19. Quota status propagation

| # | File | Lines | Description |
|---|------|-------|-------------|
| 49 | `quota/quota_test.go` | 91-97, 218-224, 100-107, 408-416 | After creating ResourceQuota, polls for `status.Used` to reflect created resources. Quota controller sync can lag. |

### 20. Multiple sequential Eventually blocks compounding timeout

| # | File | Lines | Description |
|---|------|-------|-------------|
| 50 | `reconciler/partitionset/partitionset_test.go` | 82-254 | 8+ sequential Eventually blocks with `wait.ForeverTestTimeout` each. Total test time can exceed limits under load since each block can consume up to 30s. |

## LOW Severity (unlikely but possible)

These are patterns that are technically imperfect but unlikely to cause real flakes
because they're mitigated by surrounding code:

- **100ms polling intervals** throughout all tests — aggressive but paired with
  reasonable total timeouts (`wait.ForeverTestTimeout` = 30s). Only problematic when
  combined with the patterns above.
- **`require.Eventually` vs `kcptestinghelpers.Eventually`** — The former doesn't log
  intermediate failure reasons, making debugging harder when flakes occur, but doesn't
  cause flakes itself.
- **Admission informer race** in `workspacetype/controller_test.go:125-138, 219-232,
  276-291` — explicitly documented and mitigated with Eventually loops.
- **Order-dependent list assertions** in
  `reconciler/partitionset/partitionset_test.go:174-177` — handled by checking both
  orderings with `reflect.DeepEqual`.

## Summary by Pattern

| Pattern | HIGH | MEDIUM | Total |
|---------|------|--------|-------|
| `time.Sleep` / hardcoded waits | 2 | 0 | 2 |
| Non-idempotent Create in Eventually | 3 | 2 | 5 |
| Get-then-Update without conflict retry | 0 | 9 | 9 |
| Watch race (missed events) | 2 | 2 | 4 |
| Assertion without polling after mutation | 0 | 6 | 6 |
| RBAC informer lag | 0 | 5 | 5 |
| Discovery cache lag | 0 | 5 | 5 |
| CRD serving lag | 0 | 3 | 3 |
| Audit log file race | 1 | 0 | 1 |
| HTTP server startup race | 1 | 0 | 1 |
| Cascading delete timeouts | 1 | 0 | 1 |
| Webhook restart race | 0 | 1 | 1 |
| Other (authenticator, injection, quota) | 0 | 7 | 7 |
| **Total** | **11** | **40** | **50** |

All file paths are relative to `test/e2e/`.
