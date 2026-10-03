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

package testcreds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

const genTestCredsScript = `#!/bin/bash

OUTPUT_DIR=${1:-/tmp}
CONFIG_PATH=${2}

openssl genpkey -algorithm RSA -out "${OUTPUT_DIR}/server.key" -pkeyopt rsa_keygen_bits:2048
openssl req -new -key "${OUTPUT_DIR}/server.key" -out "${OUTPUT_DIR}/server.csr" -config "${CONFIG_PATH}"
openssl x509 -req -days 365 -in "${OUTPUT_DIR}/server.csr" -signkey "${OUTPUT_DIR}/server.key" -out "${OUTPUT_DIR}/server.crt" \
  -extensions v3_req -extfile "${CONFIG_PATH}"
`

const opensslCnfContent = `[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = localhost

[v3_req]
keyUsage = keyEncipherment, dataEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
IP.2 = ::1
`

type testCredsCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *testCredsCmd {
	return &testCredsCmd{RootCmd: rootCmd}
}

func (c *testCredsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "test-creds",
		Hidden:  true,
		Usage:   "",
		Aliases: []string{},
		Desc: `Generates a test private key and certificate.

For the ABFS server, provide the key with --tls-key-file and the cert with --tls-cert-file.
For clients, provide the cert in the config with the root-ca-file field.`,
	}
}

func (c *testCredsCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *testCredsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	tmpDir, err := os.MkdirTemp("", "test-creds-*")
	if err != nil {
		return clierr.CLIErr("Error creating temp dir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(configPath, []byte(opensslCnfContent), 0644); err != nil {
		return clierr.CLIErr("Error writing openssl.cnf: %v", err)
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", genTestCredsScript, "bash", tmpDir, configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return clierr.CLIErr("Error generating test credentials: %v", err)
	}

	fmt.Printf("Generated key at: %s\n", filepath.Join(tmpDir, "server.key"))
	fmt.Printf("Generated cert at: %s\n", filepath.Join(tmpDir, "server.crt"))

	return nil
}
