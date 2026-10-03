// Copyright 2025 Google LLC
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

package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

/*
 * test control interface
 */

func TestCtrl_Control(t *testing.T) {
	path := env.CtrlTestNextPath()

	err := env.ctrl.Control(path, "control")
	require.NoError(t, err)
}
func TestCtrl_Remove(t *testing.T) {
	path := env.CtrlTestNextPath()

	err := env.ctrl.ControlRemove(path, "control")
	require.NoError(t, err)
}
func TestCtrl_Status(t *testing.T) {
	err := env.ctrl.StatusTest(true)
	require.NoError(t, err)
}
