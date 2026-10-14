/*
Copyright 2026 The Appshell Authors.

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

package main

import (
	"slices"
	"strings"
)

// namespacesOf reads --watch-namespaces: names separated by commas, blanks and repeats dropped,
// in a stable order. Empty means every namespace.
func namespacesOf(flag string) []string {
	var namespaces []string
	for name := range strings.SplitSeq(flag, ",") {
		if name = strings.TrimSpace(name); name != "" {
			namespaces = append(namespaces, name)
		}
	}
	slices.Sort(namespaces)

	return slices.Compact(namespaces)
}
