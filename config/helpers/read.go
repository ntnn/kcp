/*
Copyright 2021 The kcp Authors.

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
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
)

// DirEntryFilterFunc is used to filter which resources from a filesystem to read.
type DirEntryFilterFunc func(absPath string, dirEntry fs.DirEntry) (bool, error)

// ReadFromFS reads files from the provided filesystem, templates them
// and returns them as a single byte slice.
func ReadFromFS(
	ctx context.Context,
	embedFS embed.FS,
	ti *templateInput,
	transformers []TransformFileFunc,
	filter DirEntryFilterFunc,
) ([]byte, error) {
	buf := &bytes.Buffer{}

	if err := fs.WalkDir(embedFS, "/", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		if filter != nil {
			parse, err := filter(path, d)
			if err != nil {
				return fmt.Errorf("could not filter %s: %w", path, err)
			}
			if !parse {
				return nil
			}
		}

		raw, err := embedFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("could not read %s: %w", path, err)
		}

		raw, err = applyTransformers(raw, transformers...)
		if err != nil {
			return fmt.Errorf("error applying transformers to %q: %w", path, err)
		}

		out, err := tmpl(ctx, string(raw), ti)
		if err != nil {
			return fmt.Errorf("error templating manifest: %w", err)
		}

		buf.WriteString(out)
		buf.WriteString("\n---\n")
		return nil
	}); err != nil {
		return nil, fmt.Errorf("error walking fs: %w", err)
	}

	return buf.Bytes(), nil
}

// ReadResourcesFromFS reads all resources from a filesystem and returns
// them as unstructured.Unstructured objects.
// If the filter function is not nil if is called for each file. If the
// filter function returns true the file is parsed, otherwise it is ignored.
func ReadResourcesFromFS(
	ctx context.Context,
	embedFS embed.FS,
	filter DirEntryFilterFunc,
	batteriesIncluded sets.Set[string],
	transformers ...TransformFileFunc,
) ([]*unstructured.Unstructured, error) {
	ret := []*unstructured.Unstructured{}

	ti := newTemplateInput(batteriesIncluded)

	if err := fs.WalkDir(embedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		if filter != nil {
			parse, err := filter(path, d)
			if err != nil {
				return fmt.Errorf("could not filter %s: %w", path, err)
			}
			if !parse {
				return nil
			}
		}

		raw, err := embedFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("could not read %s: %w", path, err)
		}
		for _, transformer := range transformers {
			if raw, err = transformer(raw); err != nil {
				return fmt.Errorf("could not transform %s: %w", path, err)
			}
		}

		// some yaml docs are templates
		out, err := tmpl(ctx, string(raw), ti)
		if err != nil {
			return fmt.Errorf("error templating manifest: %w", err)
		}

		resources, err := ParseYAML([]byte(out))
		if err != nil {
			return fmt.Errorf("could not parse %s: %w", path, err)
		}

		for _, resource := range resources {
			v, found := resource.GetAnnotations()[annotationBattery]
			if !found {
				// resource is not relevant to batteries-included, just
				// add it to be installed
				ret = append(ret, resource)
				continue
			}

			partOf := strings.Split(v, ",")
			included := false
			for _, p := range partOf {
				if batteriesIncluded.Has(strings.TrimSpace(p)) {
					included = true
					break
				}
			}
			if !included {
				continue
			}
			ret = append(ret, resource)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("could not walk embed FS: %w", err)
	}

	return ret, nil
}
