/*
Copyright 2026 The kcp Authors.

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
	"bytes"
	"context"
	"fmt"
	"text/template"

	"k8s.io/apimachinery/pkg/util/sets"
)

type templateInput struct {
	Batteries map[string]bool
}

func newTemplateInput(batteries sets.Set[string]) *templateInput {
	ti := &templateInput{
		Batteries: map[string]bool{},
	}

	for _, b := range sets.List[string](batteries) {
		ti.Batteries[b] = true
	}

	return ti
}

func tmpl(ctx context.Context, raw string, input *templateInput) (string, error) {
	tmpl, err := template.New("manifest").Parse(raw)
	if err != nil {
		return "", fmt.Errorf("error parsing manifest: %w", err)
	}

	buf := &bytes.Buffer{}
	if err := tmpl.Execute(buf, input); err != nil {
		return "", fmt.Errorf("error executing manifest template: %w", err)
	}

	return buf.String(), nil
}
