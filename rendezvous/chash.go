// Copyright 2023 Google LLC
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

package rendezvous

import (
	"fmt"

	hrw "github.com/google/abfs/third_party/rendezvous"
)

// Chasher finds which of the provided input values should be handled by the
// current workerIndex based on the provided workerCount.
type Chasher struct {
	hash       *hrw.Hasher
	currentKey string
}

func NewChasher(workerCount, workerIndex int) *Chasher {
	c := &Chasher{
		hash:       hrw.New(),
		currentKey: keyFromIndex(workerIndex),
	}
	for i := 0; i < workerCount; i++ {
		c.hash.Add(keyFromIndex(i))
	}
	return c
}

func (c *Chasher) IsAssignedToCurrentWorker(testValue string) bool {
	return c.hash.Get(testValue) == c.currentKey
}

func keyFromIndex(i int) string {
	return fmt.Sprintf("worker-%d", i)
}
