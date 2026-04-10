/*
Copyright 2025 The KCP Authors.

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

package garbagecollector

import (
	"bytes"
	"fmt"
)

// ToDOT returns a DOT-format representation of the ownership graph for
// debugging and introspection.
func (g *Graph) ToDOT() string {
	var buf bytes.Buffer
	buf.WriteString("strict digraph ownership {\n")

	g.ownerToDependents.Range(func(ownerID ID, deps *[]ObjectReference) bool {
		if deps == nil {
			return true
		}
		for _, dep := range *deps {
			fmt.Fprintf(&buf, "  %q -> %q;\n",
				fmt.Sprintf("%s/%s", ownerID.ClusterName, ownerID.ID),
				fmt.Sprintf("%s/%s", dep.ClusterName, dep.UID),
			)
		}
		return true
	})

	buf.WriteString("}\n")
	return buf.String()
}
