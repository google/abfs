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

package dagsync

import "github.com/google/abfs/abfslog"

// processFailureLog limits the failed push lines over all DAGSyncs: in a server outage every queued push
// fails, and the git-pusher creates a DAGSync per push.
var processFailureLog = abfslog.NewLimiter("failed push")

// FlushFailureLog writes the count of failed push lines left out so far. Daemons call it when they stop.
func FlushFailureLog() {
	processFailureLog.Flush()
}
