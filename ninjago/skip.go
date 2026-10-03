// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ninjago

import "github.com/google/ninja-go/graph"

// Following tests are not supported by ABFS
var skipTargets = map[string]struct{}{
	"$OUT_DIR/host/linux-x86/bin/go/blueprint-microfactory/test/test.passed": {},
	"$OUT_DIR/host/linux-x86/bin/go/soong-ui-build/test/test.passed":         {},
}

// Skip building these nodes
func SkipNode(n *graph.Node) bool {
	for _, t := range n.Targets {
		if _, found := skipTargets[t]; found {
			return true
		}
	}
	return false
}
