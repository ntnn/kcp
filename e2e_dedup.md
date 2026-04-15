# E2E Test Harness Deduplication Plan

Inventory of reusable components across e2e tests and concrete extraction
targets for the GC test rewrite.

---

## Already shared (keep using as-is)

| Helper | Package | Used by |
|--------|---------|---------|
| `kcptesting.SharedKcpServer(t)` | `sdk/testing` | Every e2e test |
| `kcptesting.NewWorkspaceFixture(...)` | `sdk/testing` | Every e2e test |
| `kcptesting.WaitForAPIReady(t, disco, gv)` | `sdk/testing` | CRD and binding tests |
| `kcptestinghelpers.Eventually(...)` | `sdk/testing/helpers` | Every polling assertion |
| `kcptestinghelpers.EventuallyCondition(...)` | `sdk/testing/helpers` | APIBinding readiness waits |
| `apifixtures.BindToExport(...)` | `fixtures/apifixtures` | 22 test files |
| `apifixtures.NewSheriffsCRDWithSchemaDescription(group, desc)` | `fixtures/apifixtures` | CRD tests across many packages |
| `apifixtures.NewSheriffsCRDWithVersions(group, versions...)` | `fixtures/apifixtures` | Multi-version CRD tests |
| `apifixtures.CreateSheriffsSchemaAndExport(...)` | `fixtures/apifixtures` | Sheriff APIExport tests |
| `framework.Suite(t, "control-plane")` | `test/e2e/framework` | 63 test files |
| `framework.UniqueGroup(suffix)` | `test/e2e/framework` | CRD tests needing unique groups |
| `configcrds.CreateSingle(ctx, client, crd)` | `config/crds` | CRD bootstrapping in 4+ packages |
| `helpers.CreateResourceFromFS(ctx, client, mapper, nil, file, fs)` | `config/helpers` | APIResourceSchema creation in 5 packages |

---

## Duplicated — extract to shared packages

### 1. `apiresourceschema_cowboys.yaml` — 5 identical copies

All have the same md5 (`06b27c1f16f0b25925fba87be90462ce`):

- `test/e2e/apibinding/apiresourceschema_cowboys.yaml`
- `test/e2e/garbagecollector/apiresourceschema_cowboys.yaml`
- `test/e2e/reconciler/workspace/apiresourceschema_cowboys.yaml`
- `test/e2e/virtual/apiexport/apiresourceschema_cowboys.yaml`
- `test/e2e/reconciler/apiexportendpointslice/apiresourceschema_cowboys.yaml`

Each package has its own `//go:embed *.yaml` and `var testFiles embed.FS`.

**Action**: Move to `test/e2e/fixtures/apifixtures/`, embed there, expose via
an exported `var`. Delete the 5 copies. Each package currently doing
`helpers.CreateResourceFromFS(..., "apiresourceschema_cowboys.yaml", testFiles)`
switches to using the shared FS.

### 2. Cowboys APIExport creation — ~15 copy-paste blocks

The following 20-line block is repeated across 13 files in 5 packages:

```go
mapper := restmapper.NewDeferredDiscoveryRESTMapper(
    memory.NewMemCacheClient(discoveryClient.Cluster(providerPath)))
err = helpers.CreateResourceFromFS(t.Context(),
    dynamicClusterClient.Cluster(providerPath), mapper, nil,
    "apiresourceschema_cowboys.yaml", testFiles)
require.NoError(t, err)

cowboysAPIExport := &apisv1alpha2.APIExport{
    ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
    Spec: apisv1alpha2.APIExportSpec{
        Resources: []apisv1alpha2.ResourceSchema{{
            Name: "cowboys", Group: "wildwest.dev",
            Schema: "today.cowboys.wildwest.dev",
            Storage: apisv1alpha2.ResourceSchemaStorage{
                CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
            },
        }},
    },
}
_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().
    APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
```

**Action**: Add `apifixtures.CreateCowboysSchemaAndExport(ctx, t, providerPath,
cfg)`. This parallels the existing `CreateSheriffsSchemaAndExport` but uses the
embedded YAML schema (cowboys have a richer schema defined in YAML rather than
Go). It creates the mapper, discovery client, and dynamic client internally from
`cfg` so callers just pass the rest config.

### 3. `bootstrapCRD` — thin wrapper duplicated

Currently in `garbagecollector/support.go`:

```go
func bootstrapCRD(t *testing.T, clusterName logicalcluster.Path,
    client kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
    crd *apiextensionsv1.CustomResourceDefinition) {
    err := configcrds.CreateSingle(t.Context(), client.Cluster(clusterName), crd)
    require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}
```

Other packages (`quota_test.go`, `cross_logical_cluster_list_test.go`,
`url_test.go`) inline the same `configcrds.CreateSingle` + `require.NoError`
two-liner.

**Action**: Add `apifixtures.BootstrapCRD(t, clusterPath, crdClusterClient,
crd)` in `test/e2e/fixtures/apifixtures/`. Trivial but saves every CRD test
from the same 2-line call and ensures consistent error messages.

### 4. `newClusterScopedCRD` — only in GC support.go

Currently in `garbagecollector/support.go`. Parallels
`NewSheriffsCRDWithSchemaDescription` (namespaced) in `apifixtures` but for
cluster-scoped resources. The `apifixtures` package already has namespaced CRD
factories — a cluster-scoped variant belongs beside them.

**Action**: Add `apifixtures.NewClusterScopedCRD(group, name)` in
`test/e2e/fixtures/apifixtures/`. Remove from GC support.go.

### 5. Client creation boilerplate — 4 clients from one config

Every test that uses CRDs or bound types repeats:

```go
kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
require.NoError(t, err)
dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
require.NoError(t, err)
crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
require.NoError(t, err)
kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
require.NoError(t, err)
```

**Action**: Add to `test/e2e/framework`:

```go
type ClientSet struct {
    Kube    kcpkubernetesclientset.ClusterInterface
    Dynamic kcpdynamic.ClusterInterface
    CRD     kcpapiextensionsclientset.ClusterInterface
    KCP     kcpclientset.ClusterInterface
}

func NewClientSet(t *testing.T, cfg *rest.Config) ClientSet
```

`NewClientSet` creates all four clients and calls `require.NoError` on each.
Tests that only need a subset can still use individual constructors, but most
CRD/binding tests need all four.

---

## GC-specific (keep in `garbagecollector/support.go`)

| Helper | Reason to keep local |
|--------|---------------------|
| `pluralize(name)` | Only used by `NewClusterScopedCRD`; trivial; moves with it if the CRD factory moves |

---

## Summary of changes

| Target | What moves there |
|--------|-----------------|
| `test/e2e/fixtures/apifixtures/` | `apiresourceschema_cowboys.yaml` (single copy + embed), `CreateCowboysSchemaAndExport`, `BootstrapCRD`, `NewClusterScopedCRD` |
| `test/e2e/framework/` | `ClientSet` struct + `NewClientSet` constructor |
| Delete | 4 duplicate `apiresourceschema_cowboys.yaml` copies |
| Update | 13+ test files to use `CreateCowboysSchemaAndExport` instead of inlined blocks |
| Update | 4+ test files to use `BootstrapCRD` instead of inlined `configcrds.CreateSingle` |
