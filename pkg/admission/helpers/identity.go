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

package helpers

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// ValidateIdentityStatus reserves identity assignment and key publication for
// trusted system controllers. Assignment requires privileged loopback credentials;
// cross-shard rotation credentials may update an existing APIExport's keys.
// Once assigned, the identity cannot change, even for privileged callers.
func ValidateIdentityStatus(a admission.Attributes) error {
	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	identity, keys, err := identityStatus(a.GetObject())
	if err != nil {
		return admission.NewForbidden(a, err)
	}
	var oldIdentity string
	var oldKeys []string
	if a.GetOperation() == admission.Update {
		oldIdentity, oldKeys, err = identityStatus(a.GetOldObject())
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("invalid old object: %w", err))
		}
	}

	if oldIdentity != "" && identity != oldIdentity {
		return admission.NewForbidden(a, field.Forbidden(field.NewPath("status", "identityHash"), "is immutable once set"))
	}
	privileged := a.GetUserInfo() != nil && slices.Contains(a.GetUserInfo().GetGroups(), user.SystemPrivilegedGroup)
	if !privileged {
		if identity != oldIdentity {
			return admission.NewForbidden(a, field.Forbidden(field.NewPath("status", "identityHash"), "may only be assigned by a trusted system controller"))
		}
		rotationController := oldIdentity != "" &&
			a.GetResource().Group == "apis.kcp.io" && a.GetResource().Resource == "apiexports" &&
			a.GetUserInfo() != nil && slices.Contains(a.GetUserInfo().GetGroups(), bootstrap.SystemExternalLogicalClusterAdmin)
		if !slices.Equal(keys, oldKeys) && !rotationController {
			return admission.NewForbidden(a, field.Forbidden(field.NewPath("status", "identityKeyHashes"), "may only be changed by a trusted system controller"))
		}
	}
	return nil
}

func identityStatus(obj interface{}) (string, []string, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u == nil {
		return "", nil, fmt.Errorf("expected unstructured object, got %T", obj)
	}
	identity, _, err := unstructured.NestedString(u.Object, "status", "identityHash")
	if err != nil {
		return "", nil, err
	}
	keys, _, err := unstructured.NestedStringSlice(u.Object, "status", "identityKeyHashes")
	if err != nil {
		return "", nil, err
	}
	return identity, keys, nil
}
