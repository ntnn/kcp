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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kcpcrypto "github.com/kcp-dev/apimachinery/v2/pkg/util/crypto"
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
		// A configured lookup must reject known reserved IDs, including ones
		// whose initial key was retired.
		id = hash
	}
	return id, []string{hash}, nil
}

// GenerateIdentitySecret creates a Kubernetes Secret containing a randomly generated
// identity key used for APIExport identity.
//
// The secret will be created in the given namespace with the provided name.
// The identity key is stored in StringData under apisv1alpha1.SecretKeyAPIExportIdentity.
//
// A warning is logged if key generation takes longer than 100ms.
func GenerateIdentitySecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	start := time.Now()
	key := kcpcrypto.Random256BitsString()
	if dur := time.Since(start); dur > time.Millisecond*100 {
		logger.Info("identity key generation took a long time", "duration", dur)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: map[string]string{},
		},
		StringData: map[string]string{
			apisv1alpha1.SecretKeyAPIExportIdentity: key,
		},
	}, nil
}

// IdentityHash computes a SHA-256 hash of the identity key stored in the given Secret.
//
// The identity key is expected to be present in secret.Data under
// apisv1alpha1.SecretKeyAPIExportIdentity.
func IdentityHash(secret *corev1.Secret) (string, error) {
	return KeyHash(secret)
}
