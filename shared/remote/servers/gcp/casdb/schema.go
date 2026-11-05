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

package casdb

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

var sqlCommentRe = regexp.MustCompile(`(?m)^\s*--.*$`)

// SchemaStatements returns the DDL statements of the Spanner schema in schema.sql.
func SchemaStatements() []string {
	var stmts []string
	for _, s := range strings.Split(sqlCommentRe.ReplaceAllString(schemaSQL, ""), ";") {
		if s = strings.TrimSpace(s); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}
