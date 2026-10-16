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

package identity

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVerify(t *testing.T) {
	for _, tt := range []struct {
		name, id, hash, found, want string
		hashes                      []string
		lookupErr                   error
		wantErr                     bool
	}{
		{name: "initialize deterministically", hash: "key", want: "key"},
		{name: "migrate legacy", id: "key", hash: "key", want: "key"},
		{name: "reject foreign legacy key", id: "old", hash: "foreign", wantErr: true},
		{name: "adopt rotated identity", hash: "new-key", found: "stable", want: "stable"},
		{name: "accepted changed key preserves ID", id: "stable", hashes: []string{"old-key", "new-key"}, hash: "new-key", want: "stable"},
		{name: "reject unaccepted key", id: "stable", hashes: []string{"old-key"}, hash: "foreign", wantErr: true},
		{name: "lookup failure", hash: "key", lookupErr: errors.New("conflict"), wantErr: true},
		{name: "incomplete status", hashes: []string{"key"}, hash: "key", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, hashes, err := Verify(tt.id, tt.hashes, tt.hash, func(string) (string, error) {
				return tt.found, tt.lookupErr
			})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, id)
			require.Contains(t, hashes, tt.hash)
		})
	}
}

func TestKeyHashAndNewIdentityID(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"identity": "claimed"}},
		Data:       map[string][]byte{"key": []byte("abc")},
	}
	hash, err := KeyHash(secret)
	require.NoError(t, err)
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", hash)
	_, err = KeyHash(&corev1.Secret{})
	require.Error(t, err)
	id, err := NewIdentityID()
	require.NoError(t, err)
	data, err := hex.DecodeString(id)
	require.NoError(t, err)
	require.Len(t, data, 32)
	other, err := NewIdentityID()
	require.NoError(t, err)
	require.NotEqual(t, id, other)
}
