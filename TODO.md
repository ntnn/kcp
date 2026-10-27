# TODO

Consolidated TODO/FIXME/HACK/XXX/NOTE/TBD markers in the kcp codebase.
Grouped by the work item they point at (duplicates merged), ordered by urgency:
security, performance, functionality, then everything else.

Locations are relative to the repo root.

## 1. Security

### 1.1 Front-proxy / proxy bypasses the authz stack via the privileged system group
- `cmd/sharded-test-server/main.go:172` — needs a separate group valid only for
  the proxy so it can make wildcard requests against shards; for now it uses
  the privileged system group to bypass the authz stack.
- `pkg/server/config.go:305` — non-identity root-shard informers authenticate
  as `system:admin`; switch to a dedicated `kcp-admin` user.

### 1.2 Admin auth material stored as a file path, not a Secret
- `pkg/server/options/authentication.go:56` — `ShardAdminTokenHashFilePath`
  should be moved into an in-cluster Secret (e.g. an `"in-cluster"` string
  value).

### 1.3 Root CA for the serviceaccount controller is read once at startup
- `pkg/server/controllers.go:371` — make CA loading dynamic so rotations of
  the file on disk are picked up.

### 1.4 APIBinder cannot work in a sharded environment
- `pkg/reconciler/tenancy/initialization/apibinder_initializer_controller.go:227`
  — default-APIBinding initialization only sees local workspaces; this is a
  security/authorization gap in sharded setups.

### 1.5 Load-testing setup uses insecure auth
- `test/load/setup/manifests/metrics-viewer-kubeconfig-req.yaml:10` — replace
  placeholder auth once kcp-dev/kcp#4062 is resolved.

## 2. Performance

### 2.1 Watch cache disabled on the cache server
- `pkg/cache/server/options/options.go:89`, `pkg/server/options/options.go:445`
  — re-enable the watch cache once the shard name can be passed for key
  calculation and `wildcardClusterNameRegex` /
  `crdWildcardPartialMetadataClusterNameRegex` are updated.

### 2.2 Logical cluster migration triggers mass relists
- `pkg/reconciler/migration/logicalclustermigration/reconciler.go:370` —
  relisting every ~10h is fine, but migrating 5000 of 10000 workspaces on a
  shard means 5000 relists. Manually filling the stores would be cheapest but
  brittle; find a middle ground.

### 2.3 Per-workspace authenticator rebuilds not optimized
- `pkg/authentication/index_lazy.go:177` — WACs are now pushed to the
  cache-server and RVs are already tracked in cached authenticators; handlers
  on the WAC should rebuild WAC/union authenticators instead of from scratch
  (saves cycles when the same WAC is reused across many workspaces).
- `pkg/authentication/index_lazy.go:79` — make authenticator cache TTLs
  configurable; a longer SuccessTTL may be preferable on busy instances.

### 2.4 Discovery of virtual workspace types done repeatedly
- `pkg/server/aggregatingcrdversiondiscovery/verbs_provider_virtualstorage.go:39`
  — cache already discovered types.
- `pkg/server/aggregatingcrdversiondiscovery/verbs_provider_virtualstorage.go:86`
  — skip real discovery for known virtual resources.

### 2.5 Informers extended to unknown resources via a client pool
- Repeated in: `staging/src/github.com/kcp-dev/client-go/informers/generic.go:136`,
  `staging/src/github.com/kcp-dev/client-go/apiextensions/informers/generic.go:90`,
  `staging/src/github.com/kcp-dev/sdk/client/informers/externalversions/generic.go:100,161`,
  `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-informer-gen/generators/generic.go:223,243`,
  `staging/src/github.com/kcp-dev/code-generator/examples/pkg/generated/informers/externalversions/generic.go:60`,
  `staging/src/github.com/kcp-dev/code-generator/examples/pkg/kcpexisting/clients/informers/externalversions/generic.go:97`,
  `staging/src/github.com/kcp-dev/code-generator/examples/pkg/kcp/clients/informers/externalversions/generic.go:101,158`,
  `test/e2e/fixtures/wildwest/client/informers/externalversions/generic.go:94,109`.
  The generator and generated clients all carry the same marker.

### 2.6 Reflector improvements (vendored third_party)
- `staging/src/github.com/kcp-dev/apimachinery/third_party/reflector/reflector.go:254`
  — expose the configuration through SharedInformerFactory (#136943).
- `staging/src/github.com/kcp-dev/apimachinery/third_party/reflector/reflector.go:808,850,1300`
  — follow-ups on #115478 (large list / slow client / p&f paths, moving
  helpers to k8s/utils).
- `staging/src/github.com/kcp-dev/apimachinery/third_party/reflector/controller.go:211`
  — consider parallel processing.
- `staging/src/github.com/kcp-dev/apimachinery/third_party/informers/shared_informer.go:942`
  — bounded DeltaFIFO follow-up.

## 3. Functionality

### 3.1 Removed APIBinding schemas never get unlocked
- `pkg/reconciler/apis/apibinding/apibinding_reconcile.go:280` — a
  distinguishable way is needed to unlock schemas that were removed.

### 3.2 Logical cluster deletion
- `pkg/reconciler/core/logicalclusterdeletion/logicalcluster_deletion_controller.go:292`
  — ClusterRole/ClusterRoleBinding cleanup missing.
- `pkg/reconciler/core/logicalclusterdeletion/deletion/logicalcluster_resource_deletor.go:206`
  — blocked on kubernetes/kubernetes#22413.
- `pkg/reconciler/core/logicalclusterdeletion/deletion/logicalcluster_resource_deletor.go:516`
  — no grace period handling for certain resources.
- `config/system-crds/bootstrap.go:53` — get rid of the CRD-bootstrap special
  case; enforce/support schema evolution while wildcard informers still work.

### 3.3 Sharding and proxy gaps
- `pkg/proxy/mapping.go:89` — handle virtual workspace apiservers per shard.
- `cmd/sharded-test-server/main.go:273` — support multiple virtual workspace
  servers (multiple ports).

### 3.4 Admission policies with Params only work on a single shard
- `test/e2e/conformance/mutatingadmissionpolicy_test.go:412`,
  `test/e2e/conformance/validatingadmissionpolicy_test.go:418` — tests are
  marked as such; multi-shard support is the missing piece.

### 3.5 APIExport v1alpha2 design items
- `staging/src/github.com/kcp-dev/sdk/apis/apis/v1alpha2/types_apiexport.go:120`
  (same in v1alpha1 `types_apiexport.go:132`) — re-evaluate field name before
  beta.
- `staging/src/github.com/kcp-dev/sdk/apis/apis/v1alpha2/types_apiexport.go:203`
  — resource selector TBD.
- `pkg/virtual/apiexport/schemas/builtin/builtin.go:60,119` — refactor once
  fully on v1alpha2.

### 3.6 Virtual workspaces
- `staging/src/github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver/discovery.go:126`
  — add scale subresource.
- `staging/src/github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry/rest.go:94`
  — decide what happens on Destroy().

### 3.7 Workspaces and types
- `pkg/virtual/initializingworkspaces/builder/build.go:292` — remove the
  fallback in favor of always requiring the field.
- `pkg/virtual/initializingworkspaces/builder/build.go:469` — make `/services`
  hard-coded everywhere instead of configurable.
- `pkg/index/index.go:290` — make the migrating annotation part of the API.
- `pkg/reconciler/tenancy/workspacemounts/workspacemounts_reconcile_updater.go:37`
  — extend beyond status-of-mount-annotation updates.
- `pkg/admission/workspacetypeexists/admission.go:451` — move the check out of
  admission into a shared location.
- `pkg/server/home_workspaces.go:168` — drop the temporary phase-A checks.
- `staging/src/github.com/kcp-dev/cli/pkg/bind/plugin/bind.go:357` — add
  selector support to `kcp bind`.
- `staging/src/github.com/kcp-dev/sdk/apis/tenancy/v1alpha1/types_workspacetype.go:34`
  — tie the reserved-names definition to the patches.

### 3.8 Cache / replication
- `pkg/reconciler/cache/clustercachedresources/clustercachedresources_reconcile_replication.go:69`
  — add locking when multiple workers are supported.
- `pkg/reconciler/cache/clustercachedresourceendpointslice/clustercachedresourceendpointslice_reconcile.go:121`
  — decide if this is the right place to check identity matches the export's
  Storage.Virtual.IdentityHash.

### 3.9 Observability and user-facing errors
- `pkg/proxy/metrics/metrics.go:53` — include shard URL in proxy metrics.
- `pkg/virtual/apiexport/controllers/apireconciler/apiexport_apireconciler_reconcile.go:239,258`,
  `pkg/virtual/replication/controllers/apireconciler/clustercachedresource_apireconciler_reconcile.go:124`
  — expose user-visible errors.

### 3.10 Authenticator failure goes stale silently
- `pkg/authentication/index_eager.go:98` — if an authenticator fails to build
  off a WAC it just goes stale with no re-attempts; decide on retry/visibility
  behavior.

### 3.11 Misc features
- `pkg/server/options/authorization.go:193` — link the markdown docs.
- `pkg/server/localproxy.go:165` — return a custom error message.
- `pkg/server/handler.go:118,169` — verify the Codecs used are the right ones.
- `pkg/reconciler/dynamicrestmapper/defaultrestmapper_kcp.go:62,94,124` —
  restrict plural acceptance, verify name mappings across differing versions,
  eliminate the shared-mapper shortcut.
- `pkg/server/options/authentication.go` / flags — make controller tuning
  configurable (`pkg/server/controllers.go:1944`, `:1760`).
- `test/load/setup/manifests/kcp.yaml:54,95,150,205` — blocked on
  kcp-operator#163.

## 4. Everything else (cleanup, tests, docs, generated/vendored code)

### 4.1 v1alpha1 APIExport removal cleanup
- `pkg/admission/apiexport/admission.go:204,215`,
  `pkg/admission/apiexport/admission.go:245`,
  `pkg/admission/apibinding/apibinding_admission.go:348` — relax/remove
  migrations once all APIExports are on the new format.
- `pkg/reconciler/apis/apiexport/apiexport_reconcile.go:132` — remove after
  feature gate removal.
- `pkg/reconciler/apis/apiexportendpointslice/apiexportendpointslice_reconcile.go:49`
  — remove once confident it is unused.

### 4.2 Duplicated code to unify
- `pkg/reconciler/apis/apibinding/apibinding_reconcile_test.go:1285` and
  `pkg/reconciler/apis/apibindingdeletion/apibinding_deletor_test.go:230` —
  unify the copy of apibinding admission into a reusable package.
- `pkg/reconciler/cache/clustercachedresources/clustercachedresources_reconcile.go:229`
  — copy from the apiexport controller; move to a shared location.
- `test/e2e/cache/replication_api_cache_test.go:373` — consolidate with
  `kcp/test/e2e/reconciler/cache/replication_test.go`; does not support an
  external shared cache.
- `test/e2e/cache/replication_api_cache_test.go:378` and
  `test/e2e/reconciler/cache/replication_test.go:813` — associate a shard
  instance with a cache server (same item in both tests).

### 4.3 `SplitMetaClusterNamespaceKey` / `ClusterFrom` to return `tenancy.Name`
- `pkg/admission/reservedcrdannotations/admission.go:75`,
  `pkg/reconciler/apis/apibindingdeletion/apibinding_deletion_controller.go:255`,
  `pkg/reconciler/apis/identitycache/api_export_identity_controller.go:98,120`,
  `pkg/reconciler/apis/crdcleanup/crdcleanup_controller.go:324` — drop the
  `logicalcluster.Name(cluster.String())` conversions once done.

### 4.4 Controller/config cleanups
- `pkg/server/controllers.go:734` — remove and move to batteries.
- `pkg/server/controllers.go:997,1001,1005,1059,1063,1067` — move
  verification up (six identical markers).
- `pkg/server/controllers.go:1862` — set user agent.
- `pkg/server/controllers.go:1753` — figure out whether config is needed.
- `pkg/server/server.go:140` — document/fix the discovery client used for CEL
  admission lookups.
- `pkg/server/server.go:150` — remove when P&F is wired.
- `pkg/server/server.go:717` — split everything after that line into its own
  commands.
- `pkg/server/config.go:665` and `pkg/reconciler/kubequota/kubequota_controller.go:64`
  — find a way to support the default configuration (same item in both places).
- `pkg/reconciler/kubequota/unscoped_informer_factory.go:140` and
  `pkg/reconciler/kubequota/kubequota_controller.go:80` — fix the wrong
  discovery function; possibly rewrite the admission.
- `pkg/reconciler/migration/logicalclustermigration/datacleanup.go:43` — data
  cleanup only assumes the happy path.
- `pkg/index/index.go:155` — move to a separate function once a second case
  appears.
- `pkg/schemacompat/schemacompat.go:39` — implementation is explicitly
  incomplete (ongoing work); keep consistent.
- `pkg/virtual/apiexport/schemas/builtin/builtin.go:48,122` — handle via code
  generation; make the helper unexported.

### 4.5 HACK markers to revisit
- `staging/src/github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver/handler.go:182`
  — support client-go scheme resources the way existing clients expect.
- `pkg/admission/apiresourceschema/validation.go:104` — relaxed naming
  constraints for legacy CRD-registered schemas.
- `pkg/server/apiextensions.go:225` — placeholder identity annotation on bound
  CRD deletion.
- `pkg/server/aggregatingcrdversiondiscovery/server.go:200` — core resources
  through CRDs.
- `pkg/server/filters/filters.go:130` — testing workaround.

### 4.6 Code generator
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/types/helpers.go:33`
  — remove the string special case once last uses are cleared.
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/generators/client_generator.go:323`
  — take inputs as Go patterns.
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/generators/generator_for_clientset.go:76`
  and `generators/fake/generator_fake_for_clientset.go:96` — type information
  not actually needed for clientset generation.
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/generators/fake/generator_fake_for_type.go:252,273,296`
  — sub-resource watch/delete semantics; parametrize template verbs.
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/generators/fake/fake_client_generator.go:36,96`
  — dedupe with client-generator; generate per-release fakes.
- `staging/src/github.com/kcp-dev/code-generator/cmd/cluster-client-gen/main.go:40`
  — move the input path out of client-gen.
- `hack/update-codegen-clients.sh:36` — the hack to let CI pass.

### 4.7 Tests
- `test/integration/logicalclustermigration/per_cluster_context_test.go:66,105`
  — verify the event matches the created configmap.
- `test/e2e/garbagecollector/apis_test.go:128` — use WaitForAPIReady.
- `test/e2e/reconciler/cache/replication_test.go:770,782` — investigate why
  generation is not equal (specially rbacv1) and other noted anomalies.
- `test/e2e/virtual/initializingworkspaces/virtualworkspace_test.go:77` —
  discovery shows an empty group; find out why.
- `test/e2e/reconciler/core/logicalclusterdeletion/...`
  `pkg/reconciler/core/logicalclusterdeletion/deletion/logicalcluster_resource_deletor_test.go:38`
  — rework the test to use Workspace.
- `pkg/authorization/workspace_content_authorizer_test.go:354,394` — add
  global fixtures.
- `test/load/testing/workspace_simple_crud_test.go:89` — the QPS*4 factor is a
  workaround for 4 network calls per action; fix in the framework.

### 4.8 Vendored / third-party (staging, upstream k8s code)
- `staging/src/github.com/kcp-dev/client-go/scale/clientset.go:102` — dynamic
  RESTMapper or periodic invalidation.
- `staging/src/github.com/kcp-dev/client-go/third_party/k8s.io/client-go/tools/cache/mutation_cache.go:39`
  — layer into an informer/lister.
- `staging/src/github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake/simple.go:509,546`
  — list options ignored in the fake.
- `staging/src/github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing/fixture.go:215,1316`
  — subresource creation handled as update; share the function with apimachinery.
- `staging/src/github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing/actions.go:676`
  — acknowledged-wrong behavior, "no worse than before".
- `staging/src/github.com/kcp-dev/client-go/kubernetes/typed/core/v1/fake/*` —
  fake patch strategies should take a PatchType / be configurable.
- `staging/src/github.com/kcp-dev/sdk/testing/server/fixture.go:318,404` —
  context cancellation handling in the test-server fixture.
- `staging/src/github.com/kcp-dev/sdk/apis/apis/fuzzer/fuzzer.go:349,350` —
  remove the old fuzzer after rebasing to 1.32.
- `staging/src/github.com/kcp-dev/virtual-workspace-framework/crdpuller/discovery.go:580,593,606`
  — regexp validation for discovery fields.
- `staging/src/github.com/kcp-dev/virtual-workspace-framework/pkg/wrappers/rbac/merging.go:297`
  — figure out if RBAC merging can do better.
- `staging/src/github.com/kcp-dev/apimachinery/third_party/reflector/reflector.go:1050`
  — do consumers need "last known" state access?

### 4.9 Docs
- `docs/content/contributing/monorepo.md:118` — replace PR link with doc link.
- `docs/content/contributing/governance/security-self-assessment.md:108` —
  remove the Syncer mention.
- `docs/content/concepts/apis/exporting-apis.md:422` — bare "TODO".
- `docs/content/concepts/sharding/cache-server.md:209` — add the
  URL-to-storage-prefix mapping link.
- `docs/content/concepts/authorization/authorizers.md:265` — add example.

### 4.10 i18n
- `docs/generators/crd-ref/run-crd-ref-gen.sh:27` — i18n for crd-ref generation.
