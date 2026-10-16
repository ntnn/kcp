# Stable API identities and key fingerprints

## Scope

`APIExport.status.identityHash` and `CachedResource.status.identityHash` are
stable public storage identities, not fingerprints of the currently referenced
secret. Consumers of these fields, including storage, permission claims,
authorizers, virtual workspaces, and cache lookups, retain their existing format
and behavior.

This checkout contains `CachedResource`, not `ClusterCachedResource`. It also
does not contain `APIExportIdentityRotation`, its phases, identity aliases, or
per-shard drain controllers. Consequently this change does not introduce a new
rotation API or remove a storage migrator. The SDK is replaced by the local
staging module; both APIExport versions and CachedResource carry the new status
field there.

## Assignment, migration, and verification

`status.identityKeyHashes` records the SHA256 hex fingerprints of accepted
secret keys. Reconciliation computes the referenced secret's fingerprint and
requires membership in that list; a foreign key fails identity verification.

For an existing identity with no fingerprints, the old identity is the legacy
fingerprint. Reconciliation verifies the secret against that fingerprint before
completing migration. The identity and stored resources do not move.

For a new object, controllers search informer indexes for an existing identity
accepting the same fingerprint. They adopt that identity only on a fingerprint
match. Otherwise the initial identity is the key fingerprint, preserving
deterministic assignment and compatibility with existing objects. Subsequent
reconciliations never recompute the public identity. A random 256-bit identity
generator is available for future coordinated assignment, but is deliberately
not used by this migration.

No identity identifier or fingerprint asserted in secret metadata is trusted.
The secret key itself is the proof of possession.

## Status security

The CRDs enable status subresources, separating ordinary spec writes from
controller-owned status. `SystemCRDAuthorizer` already denies tenant APIExport
status requests before delegating to workspace RBAC; CachedResource must have
the same boundary. RBAC alone is insufficient: workspace administrators can
potentially grant status permissions. Admission therefore protects both the identity and
the accepted fingerprints, including status-subresource requests. Ordinary
users cannot initialize, replace, clear, or add accepted fingerprints through
status writes. The public identity is immutable once assigned.

Only trusted system controller credentials may initialize identity status or
update accepted fingerprints. This privilege must not be granted to tenant
service accounts. As with the rest of the control plane, compromising privileged
controller credentials defeats this boundary.

## Rotation and remaining work

A future rotation controller must authorize rotation using the currently
accepted key and the right to change the export, read and validate the new
secret, publish its fingerprint, switch `spec.identity.secretRef`, and retire
the old fingerprint after the configured grace policy. Merely changing the
secret reference does not authorize a new key. Throughout this process the
public identity must remain unchanged, so no storage alias or drain is needed.

TODO: integrate these steps with the rotation API and retirement phases if that
subsystem is added to this branch. This change supplies fingerprint verification
and stable identity storage, not a user-facing rotation workflow.

TODO: define an authoritative cross-shard fingerprint registry or coordinated
rotation protocol. Informer caches can lag: after rotation, a new object may
miss an adoption candidate or see a retired fingerprint. Deterministic initial
assignment handles simultaneous first use of the same fresh key, but does not
solve adoption after rotation. Cached indexes are not a revocation barrier;
globally immediate retirement requires stronger consistency. During a rollout,
upgrade all identity controllers before enabling rotation, so old controllers
do not reject a new key for an unchanged public identity.

TODO: coordinate fingerprint publication and retirement across every export
sharing an identity. Updating one export does not update its peers; retaining
an old fingerprint on any peer can continue to allow adoption through that
peer. Rotation must not report global retirement until all authoritative
participants have acknowledged it.
