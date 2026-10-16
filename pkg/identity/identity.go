/*
Copyright 2026 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package identity separates stable resource identities from key fingerprints.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// KeyHash returns the SHA-256 fingerprint of an identity secret's key material.
// Secret metadata is deliberately not an authority for the stable identity.
func KeyHash(secret *corev1.Secret) (string, error) {
	key := secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity]
	if len(key) == 0 {
		return "", fmt.Errorf("secret is missing data.%s", apisv1alpha1.SecretKeyAPIExportIdentity)
	}
	hash := sha256.Sum256(key)
	return hex.EncodeToString(hash[:]), nil
}

// NewIdentityID generates an independent, random 256-bit stable identity.
func NewIdentityID() (string, error) {
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// Verify verifies membership without ever replacing an established identity.
// lookup returns a unique existing identity for the fingerprint, or an empty ID.
func Verify(id string, hashes []string, hash string, lookup func(string) (string, error)) (string, []string, error) {
	if id != "" {
		if len(hashes) == 0 {
			// Legacy identities were the key fingerprint. Verify before migrating.
			if id != hash {
				return "", nil, fmt.Errorf("hash mismatch: identity secret hash %q must match status.identityHash %q", hash, id)
			}
			return id, []string{hash}, nil
		}
		if !slices.Contains(hashes, hash) {
			return "", nil, fmt.Errorf("identity secret hash %q is not in status.identityKeyHashes", hash)
		}
		return id, hashes, nil
	}
	if len(hashes) != 0 {
		return "", nil, fmt.Errorf("status.identityKeyHashes is set without status.identityHash")
	}
	if lookup != nil {
		var err error
		id, err = lookup(hash)
		if err != nil {
			return "", nil, err
		}
	}
	if id == "" {
		// Deterministic initialization also converges while informer updates lag.
		id = hash
	}
	return id, []string{hash}, nil
}
