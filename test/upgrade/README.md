# Upgrade tests

This suite verifies the kcp upgrade path: a previous kcp version is started
and seeded with data, shut down, and the same root directory (embedded etcd
state, certificates) is restarted with the current tree's binary. The test
then asserts that:

1. the upgraded server becomes ready on the old data,
2. seeded workspaces, APIExports/APIBindings, bound resource instances
   (including their UIDs) and core objects survived the upgrade,
3. new workspaces and new bound resources can be created after the upgrade,
4. shards are registered and functional.

Optionally, regular e2e packages are run against the upgraded server.

## Running

```sh
make test-upgrade
```

This builds the current tree, downloads the latest released kcp binary via
`hack/download-kcp-release.sh` and runs the suite. Variants:

```sh
# upgrade from a specific release
make test-upgrade UPGRADE_FROM_VERSION=v0.32.3

# additionally run e2e packages against the upgraded server
make test-upgrade KCP_UPGRADE_E2E_PACKAGES="./test/e2e/apibinding/..."
```

The test itself is driven by environment variables and skips without them:

- `KCP_UPGRADE_FROM_BINARY`: kcp binary of the version to upgrade from.
- `KCP_UPGRADE_TO_BINARY`: kcp binary of the version to upgrade to.
- `KCP_UPGRADE_E2E_PACKAGES` (optional): space-separated go packages of e2e
  tests to run against the upgraded server. Only packages that support the
  shared external server mode (`--kcp-kubeconfig`) are eligible.

Server logs land in the artifact directory (`ARTIFACT_DIR` if set) as
`kcp-old.log` and `kcp-new.log`.

## Scope

Today the suite covers a single-shard server. Extending it to the sharded
topology (multiple shards, front-proxy, cache server via
`sharded-test-server`) is a planned follow-up, as is growing the seeded
fixture whenever a change moves where state lives (for example shard
registration or APIExport identities).
