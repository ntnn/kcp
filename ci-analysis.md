# kcp Prow E2E Job Analysis

## Current E2E Jobs

All four e2e jobs share the same trigger (`run_if_changed`), suite (`control-plane`),
resources (6Gi/4cpu), and test packages (`./test/e2e...`).

| Job | Make target | Server topology | COUNT | Purpose |
|-----|------------|-----------------|-------|---------|
| `pull-kcp-test-e2e` | `test-e2e` | Per-test-binary (each binary starts own server) | 1 | Baseline e2e |
| `pull-kcp-test-e2e-multiple-runs` | `test-e2e` | Per-test-binary | 2 | Flake detection (runs every test twice) |
| `pull-kcp-test-e2e-shared` | `test-e2e-shared-minimal` | Shared single server | 1 | Single-shard production-like topology |
| `pull-kcp-test-e2e-sharded` | `test-e2e-sharded-minimal` | Shared 2-shard server | 1 | Multi-shard production-like topology |

## How Tests Obtain Servers

- **`SharedKcpServer(t)`** (~95% of tests): In `test-e2e`, starts a new server per
  test binary. In `shared`/`sharded` modes, connects to the pre-started external
  server via `--kcp-kubeconfig`.
- **`PrivateKcpServer(t)`** (5 tests: audit, home workspaces, authorization order,
  2 reconciler tests): Always starts its own server with custom arguments,
  regardless of which job runs it. Behavior is identical across all four jobs.

## Redundancy

1. **`pull-kcp-test-e2e` and `pull-kcp-test-e2e-multiple-runs`** run the exact same
   tests with the same topology. The only difference is `COUNT=2` and a 20m timeout.
   Combined, every test runs 3 times (1 + 2) in per-test-binary mode alone.

2. **All four jobs run the same test suite.** The per-test-binary server mode in
   `test-e2e` is just a heavier way to run single-shard -- the `shared` job already
   covers single-shard, and `sharded` adds multi-shard coverage on top.

3. Every test in the suite runs a minimum of **4 times per PR** across the four jobs
   (1 from `test-e2e`, 2 from `multiple-runs`, 1 from `shared` or `sharded`).

## Recommendation: Drop `pull-kcp-test-e2e` and `pull-kcp-test-e2e-multiple-runs`

### What you keep

| Aspect | `test-e2e-shared` | `test-e2e-sharded` |
|--------|-------------------|--------------------|
| SharedKcpServer tests | Pre-started single server | Pre-started 2-shard server |
| PrivateKcpServer tests | Own server (custom args) | Own server (custom args) |
| `KUBE_CACHE_MUTATION_DETECTOR` | Yes | Yes |
| Topology | Single-shard, production-like | Multi-shard, production-like |

### What you lose

Nothing in terms of test coverage:

- The per-test-binary server mode does not test a unique topology. It is a heavier
  way to achieve single-shard, which `shared` already covers.
- The 5 `PrivateKcpServer` tests start their own servers in every mode -- they run
  identically in `shared` and `sharded`.
- The `multiple-runs` job is pure flake detection. If valued, it should be a
  periodic/postsubmit job, not a presubmit gate.

### Impact

- Cuts presubmit e2e compute by ~50% (2 of 4 jobs removed).
- Each test goes from running 4+ times per PR to 2 times (once per topology).
- No loss of topology coverage (single-shard + multi-shard still covered).
- Flake detection can move to a periodic job where it doesn't block PRs.
