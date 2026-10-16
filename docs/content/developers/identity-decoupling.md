# Stable API identities and key fingerprints

## Scope

`APIExport.status.identityHash` and `ClusterCachedResource.status.identityHash` are
stable public storage identities, not fingerprints of the currently referenced
secret. Consumers of these fields, including storage, permission claims,
authorizers, virtual workspaces, and cache lookups, retain their existing format
and behavior.

The SDK is replaced by the local staging module; both APIExport versions and
ClusterCachedResource carry the new status field there. The identity rotation
API and its existing phases are retained, but key-only rotations no longer
change the storage identity or require a storage drain.

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

An initial key fingerprint is not an unused identity merely because it no
longer appears in the accepted-key index. Controllers also check existing
stable identities and reject initialization when that deterministic ID is
already reserved without an accepted-key match. Otherwise a retired initial
key could recreate the original identity even with fully synchronized caches.

No identity identifier or fingerprint asserted in secret metadata is trusted.
The secret key itself is the proof of possession.

## Status security

The CRDs enable status subresources, separating ordinary spec writes from
controller-owned status. `SystemCRDAuthorizer` already denies tenant APIExport
status requests before delegating to workspace RBAC; ClusterCachedResource has
the same boundary. RBAC alone is insufficient: workspace administrators can
potentially grant status permissions. Admission therefore protects both the identity and
the accepted fingerprints, including status-subresource requests. Ordinary
users cannot initialize, replace, clear, or add accepted fingerprints through
status writes. The public identity is immutable once assigned.

Only privileged `system:masters` controller credentials may initialize identity
status. Those credentials may also update accepted fingerprints. For an
already-initialized APIExport, the trusted
`system:kcp:external-logical-cluster-admin` group may update fingerprints to
support cross-shard rotation; it cannot initialize or replace the public
identity. This group still requires status RBAC permissions and must never be
granted to tenant service accounts. The front-proxy filters tenant assertions of
these system groups. As with the rest of the control plane, compromising
privileged controller credentials defeats this boundary.

## Rotation and remaining work

The rotation controller verifies the currently accepted key, reads the new
secret, acquires exclusive rotation ownership using a resource-version-guarded
annotation update, publishes its fingerprint, switches `spec.identity.secretRef`, and
retires the old fingerprint according to the requested retirement policy.
Merely changing the secret reference does not authorize a new key. Throughout
this process the public identity remains unchanged, so no storage alias or
drain is needed. The APIExportIdentityRotation API remains the user-facing
rotation workflow.

TODO: retain the legacy alias and drain subsystems for existing migrations
until a separate removal and upgrade plan establishes that they are no longer
needed. New key-only rotations must not invoke storage migration.
Existing legacy rotations already in `Migrating` continue to wait for their
storage drain reports. A legacy `Pending` rotation whose export has already
published a storage alias cannot safely be reinterpreted as a key-only
rotation; it fails without changing that identity or alias and requires
operator recovery.

TODO: define an authoritative cross-shard fingerprint registry or coordinated
rotation protocol. Informer caches can lag: after rotation, a new object may
miss an adoption candidate or see a retired fingerprint. Deterministic initial
assignment handles simultaneous first use of the same fresh key, but does not
solve adoption after rotation. Cached indexes are not a revocation barrier;
globally immediate retirement requires stronger consistency. During a rollout,
upgrade all identity controllers before enabling rotation, so old controllers
do not reject a new key for an unchanged public identity.

Stable-ID collision checks currently cover identities visible in the
informers, not permanent reservations after every exporting object is deleted.
A durable identity reservation and revocation registry is needed before
claiming globally immediate or deletion-independent key revocation.

TODO: coordinate fingerprint publication and retirement across every export
sharing an identity. Updating one export does not update its peers; retaining
an old fingerprint on any peer can continue to allow adoption through that
peer. Rotation must not report global retirement until all authoritative
participants have acknowledged it.
