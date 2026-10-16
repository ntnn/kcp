# APIExport Identity Rotation

!!! warning
    APIExport identity rotation is an **alpha** feature. The API and the
    operational behavior described here may change in incompatible ways in
    future releases.

An `APIExport`'s identity acts like a private key: whoever holds the identity
secret can serve the export's APIs and access resources claimed with it. If
the secret leaks — or organizational policy demands periodic credential
rotation — the secret must be replaceable without replacing the public
identity. The stable `status.identityHash` is load-bearing in two places:

1. **Storage**: bound resource instances are stored in etcd under a prefix
   that contains the identity hash
   (`/registry/<group>/<resource>/<identityHash>/<cluster>/...`).
2. **Permission claims**: `APIBinding` acceptance of permission claims is
   keyed by `(group, resource, identityHash)`, both in claim labels on
   claimed objects and in the APIExport virtual workspace views.

The `APIExportIdentityRotation` API (group `migration.kcp.io`) rotates the
accepted key fingerprints in `status.identityKeyHashes` while preserving
`status.identityHash`. Existing storage and permission claims do not move.

## Enabling the feature

Identity rotation is behind the `APIExportIdentityRotation` feature gate,
which is off by default. Enable it on every shard:

```sh
kcp start --feature-gates=APIExportIdentityRotation=true
```

With the gate disabled the rotation and migrator controllers do not run, and
creating an `APIExportIdentityRotation` is rejected by admission.

## Requesting a rotation

Create a rotation referencing the `APIExport` and a new identity secret that
has been pre-created **in the export's workspace** (that is where kcp
resolves export identities):

```yaml
apiVersion: migration.kcp.io/v1alpha1
kind: APIExportIdentityRotation
metadata:
  name: rotate-widgets-2026-09
spec:
  export:
    path: root:acme:widgets   # optional: defaults to the rotation's workspace
    name: widgets.example.kcp.io
  newIdentity:
    secretRef:
      namespace: kcp-system
      name: widgets-identity-2026-09
  aliasRetirement:
    policy: After
    after: 168h
```

The `migration.kcp.io` APIExport is owned by the platform and bound from
the `root` workspace, so binding it (and therefore requesting rotations) can
be restricted by the platform operator. Because `spec.export.path` may point
at another workspace (even on another shard), rotations are typically
requested from a platform-operated workspace: the workspaces owning the
rotated exports never need the rotation capability bound at all.

`spec.export` and `spec.newIdentity` are immutable. `spec.aliasRetirement`
selects when the old key fingerprint stops being accepted:

- `Manual` (default): the old key stays accepted until the retirement policy is changed.
- `After`: the old key retires after the configured grace duration.
  `after` must be a positive duration.
- `Immediate`: the old key retires without a grace period.

The policy may only be changed toward *earlier* retirement
(`Manual` → `After` → `Immediate`), and an `After` duration may only shrink.

## What happens during a rotation

The rotation controller drives `status.phase` through
`Pending → Migrating → AliasActive → Completed`:

1. **Pending**: the currently referenced key is verified against the export's
   accepted fingerprints, the new secret is validated, exclusive rotation
   ownership is acquired, its fingerprint is
   published, and the export's secret reference is switched.
2. **Migrating**: retained for API compatibility. No storage drain is needed
   because the public identity is unchanged; this phase is short-circuited.
3. **AliasActive**: the old key remains accepted per `spec.aliasRetirement`.
   The phase name is retained for API compatibility; no new storage alias is
   created.
4. **Completed**: the old fingerprint has been retired on the rotated export.

New key-only rotations do not fence consumer workspaces, rewrite resources, or
wait for per-shard storage drain reports. The legacy alias and migrator
subsystems remain available for existing storage migrations.

## Safety invariants

Admission enforces:

- at most one active rotation per export,
- a cooldown (1h, counted from when the previous rotation retired its alias)
  between completed rotations of the same export,
- spec immutability except the retirement policy, which only moves earlier,
- no deletion of a rotation while it is `Migrating` or `AliasActive`: the
  old key and the export's active-rotation marker are retired through the
  rotation object. To get rid of an in-flight rotation, move its policy to
  `Immediate`, let it complete, then delete it.

## Alpha limitations

- Accepted fingerprints must be coordinated across exports sharing an identity;
  retiring a key on one export does not revoke it on another export.
- Informer replication is eventually consistent. Key retirement is not a
  globally immediate revocation barrier, and adopting an identity immediately
  after rotation can observe stale fingerprints.
- Upgrade all identity controllers before enabling stable-identity rotation.
  See the [design note](../../developers/identity-decoupling.md) for migration
  and cross-shard consistency considerations.
- The admission invariants (one active rotation per export, rotation
  cooldown) are enforced among the rotations of one workspace. Rotations for
  the same export requested from different workspaces are not deduplicated;
  keep the rotation capability scoped to a single ops workspace per export.
