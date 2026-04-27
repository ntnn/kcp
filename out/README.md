# Permission Claims Selector Manual Testing

Two scenarios for testing `matchLabels` and `matchExpressions` selectors on
permission claims. Both follow the same pattern: a provider workspace exports
cowboys + claims on core resources, and a consumer workspace binds with a
scoped selector on one of the claimed resources.

## Prerequisites

- A running kcp instance
- `kubectl` with the kcp plugin installed
- Two workspaces: one for the provider, one for the consumer

## Scenario 1: matchLabels (configmaps)

The consumer accepts the configmaps permission claim with
`matchLabels: {app: claimed}`. Only configmaps carrying that label are visible
to the export owner through the virtual workspace.

### Steps

```sh
# 1. Create the provider workspace and apply the schema + export
kubectl ws create provider --enter
kubectl apply -f out/matchlabels/01-apiresourceschema.yaml
kubectl apply -f out/matchlabels/02-apiexport.yaml

# 2. Wait for the identity hash, then note it
kubectl get apiexport today-cowboys -o jsonpath='{.status.identityHash}'

# 3. Edit 03-apibinding.yaml:
#    - Replace <REPLACE_WITH_PROVIDER_WORKSPACE_PATH> with the provider ws path
#    - Replace <REPLACE_WITH_IDENTITY_HASH> with the hash from step 2
#    Also update 02-apiexport.yaml with the identity hash if the sheriffs
#    claim is relevant to your test (or remove it from both files).

# 4. Switch to the consumer workspace and create the binding
kubectl ws ..
kubectl ws create consumer --enter
kubectl apply -f out/matchlabels/03-apibinding.yaml

# 5. Wait for claims to be valid and applied
kubectl wait apibinding cowboys --for=condition=PermissionClaimsValid
kubectl wait apibinding cowboys --for=condition=PermissionClaimsApplied

# 6. Create test configmaps directly in the consumer workspace
kubectl apply -f out/matchlabels/04-test-configmaps.yaml

# 7. List via the consumer workspace -- should show both configmaps
kubectl get configmaps -n default

# 8. Get the APIExport virtual workspace URL
kubectl ws <provider-path>
kubectl get apiexportendpointslice today-cowboys -o jsonpath='{.status.apiExportEndpoints[0].url}'

# 9. List configmaps via the VW -- should show ONLY test-configmap-matching
kubectl --server=<VW_URL> get configmaps -n default
```

### Expected results

| Action                                           | Result                              |
|--------------------------------------------------|-------------------------------------|
| List configmaps via VW                           | Only `test-configmap-matching`      |
| List configmaps via consumer workspace           | Both configmaps + kube default      |
| Create configmap without labels via VW           | Succeeds, `app=claimed` auto-added  |
| Delete `test-configmap-not-matching` via VW      | Fails (not visible)                 |
| Delete `test-configmap-matching` via VW          | Succeeds                            |
| Update: remove `app` label via VW               | No-op, mutation re-adds it          |
| Update: change `app=claimed` to `app=other`      | Fails (protected label)             |

---

## Scenario 2: matchExpressions (secrets)

The consumer accepts the secrets permission claim with
`matchExpressions: [{key: tier, operator: In, values: [frontend, backend]}]`.
Only secrets where `tier` is `frontend` or `backend` are visible through the VW.

### Steps

```sh
# 1. Create the provider workspace and apply the schema + export
kubectl ws create provider --enter
kubectl apply -f out/matchexpressions/01-apiresourceschema.yaml
kubectl apply -f out/matchexpressions/02-apiexport.yaml

# 2. Wait for the identity hash
kubectl get apiexport today-cowboys -o jsonpath='{.status.identityHash}'

# 3. Edit 03-apibinding.yaml:
#    - Replace <REPLACE_WITH_PROVIDER_WORKSPACE_PATH> with the provider ws path
#    - Replace <REPLACE_WITH_IDENTITY_HASH> with the hash from step 2

# 4. Switch to the consumer workspace and create the binding
kubectl ws ..
kubectl ws create consumer --enter
kubectl apply -f out/matchexpressions/03-apibinding.yaml

# 5. Wait for claims to be valid and applied
kubectl wait apibinding cowboys --for=condition=PermissionClaimsValid
kubectl wait apibinding cowboys --for=condition=PermissionClaimsApplied

# 6. Create test secrets directly in the consumer workspace
kubectl apply -f out/matchexpressions/04-test-secrets.yaml

# 7. List via the consumer workspace -- should show all 3 secrets
kubectl get secrets -n default

# 8. Get the APIExport virtual workspace URL
kubectl ws <provider-path>
kubectl get apiexportendpointslice today-cowboys -o jsonpath='{.status.apiExportEndpoints[0].url}'

# 9. List secrets via the VW -- should show ONLY frontend + backend
kubectl --server=<VW_URL> get secrets -n default
```

### Expected results

| Action                                            | Result                                        |
|---------------------------------------------------|-----------------------------------------------|
| List secrets via VW                               | Only `test-secret-frontend`, `test-secret-backend` |
| List secrets via consumer workspace               | All 3 secrets                                 |
| Create secret without matching label via VW       | Fails (no auto-injection for expressions)     |
| Create secret with `tier=frontend` via VW         | Succeeds                                      |
| Delete `test-secret-database` via VW              | Fails (not visible)                           |
| Delete `test-secret-frontend` via VW              | Succeeds                                      |
| Update: remove `tier` label via VW               | Fails (no auto-injection for expressions)     |
| Update: change `tier=frontend` to `tier=database` | Fails (no longer matches expression)          |

---

## Key difference between matchLabels and matchExpressions

- **matchLabels**: The VW can auto-inject the exact label on create (there is
  only one possible value), and silently re-add it on update if the user tries
  to remove it.
- **matchExpressions**: The VW cannot auto-inject because the expression may
  allow multiple values (e.g. `In [frontend, backend]`). Creates and updates
  that would result in a non-matching object are rejected outright.
