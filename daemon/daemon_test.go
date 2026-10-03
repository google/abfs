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

package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZeroExitCode_NoError(t *testing.T) {
	require := require.New(t)

	cmd := exec.Command("true")
	err := ForkDaemon(cmd)

	require.NoError(err, "should have no error after zero exit code")
}

func TestSetupDaemon_LogsPreservedOnClose(t *testing.T) {
	require := require.New(t)

	// Save original stdout/stderr to restore after the test
	origStdout := os.Stdout
	origStderr := os.Stderr
	origLogWriter := log.Writer()
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		log.SetOutput(origLogWriter)
	}()

	tmpFile, err := os.CreateTemp("", "daemon_test_log")
	require.NoError(err)
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	d, err := SetupDaemon(tmpPath)
	require.NoError(err)

	testMessage := "hello daemon logs!"
	_, _ = fmt.Fprintln(os.Stdout, testMessage)
	_, _ = fmt.Fprintln(os.Stderr, testMessage)
	log.Println(testMessage)

	err = d.Close()
	require.NoError(err)

	content, err := os.ReadFile(tmpPath)
	require.NoError(err)

	require.Contains(string(content), "hello daemon logs!")
}

func TestSetupDaemon_StdoutCloseDoesNotHaltLogging(t *testing.T) {
	require := require.New(t)

	origStdout := os.Stdout
	origStderr := os.Stderr
	origLogWriter := log.Writer()
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		log.SetOutput(origLogWriter)
	}()

	tmpFile, err := os.CreateTemp("", "daemon_test_log")
	require.NoError(err)
	tmpPath := tmpFile.Name()
	require.NoError(tmpFile.Close())
	defer func() { _ = os.Remove(tmpPath) }()

	d, err := SetupDaemon(tmpPath)
	require.NoError(err)

	// Force-close the target console stdout to cause write errors on w.out.Write
	require.NoError(d.stdout.Close())

	testMessage := "log message even with closed console stdout"
	fmt.Println(testMessage)

	err = d.Close()
	require.NoError(err)

	content, err := os.ReadFile(tmpPath)
	require.NoError(err)

	require.Contains(string(content), "log message even with closed console stdout")
}
