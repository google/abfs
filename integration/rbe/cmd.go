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

package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/rbe/model"
)

type TestFileMetadata struct {
	FullPath string
	ARI      string
	SHA256   string
	Size     int64
}

type App struct {
	*root.RootCmd
	*cmd.ConfigProvider
	AllowedFlags map[string]bool
	MountPoint   string
	client       Client
}

func newApp() *App {
	return &App{
		RootCmd: root.NewRootCommand(),
		AllowedFlags: map[string]bool{
			"cache-dir":   true,
			"instance":    true,
			"mount-point": true,
		},
	}
}
func (a *App) RegisterFlags(fl *pflag.FlagSet) {
	a.RootCmd.RegisterFlags(fl)

	fl.StringVar(&a.MountPoint, "mount-point", ".", "Path to the ABFS mount point")

	fl.VisitAll(func(f *pflag.Flag) {
		if !a.AllowedFlags[f.Name] {
			f.Hidden = true
		}
	})
}

// Write data to our backend aris directory
func (a *App) AddARIData(ari string, payload []byte) error {
	cacheDir := a.Config.CacheDir
	if cacheDir == "" || ari == "" {
		return fmt.Errorf("cache dir or ari not specified")
	}

	h := sha1.Sum([]byte(ari))
	hashHex := hex.EncodeToString(h[:])

	shardDir := path.Join(cacheDir, "aris", hashHex[:3])
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return err
	}
	filePath := path.Join(shardDir, hashHex[3:])
	return os.WriteFile(filePath, payload, 0644)
}

// Write data to backend aris and notify mount of new data
func (a *App) CreateFileWithARI(relPath string, mode uint32, payload []byte) (*TestFileMetadata, error) {
	hashSum := sha256.Sum256(payload)

	hashHex := hex.EncodeToString(hashSum[:])
	payloadSize := int64(len(payload))

	ari := fmt.Sprintf("rbe:%s:%d", hashHex, payloadSize)

	digest := model.Digest{
		SHA256: hashHex,
		Size:   payloadSize,
	}

	if err := a.AddARIData(ari, payload); err != nil {
		return nil, fmt.Errorf("failed to seed ARI cache data: %w", err)
	}
	if err := a.Client().SetDigest(relPath, digest, mode); err != nil {
		return nil, err
	}

	return &TestFileMetadata{
		FullPath: filepath.Join(a.MountPoint, relPath),
		ARI:      ari,
		SHA256:   hashHex,
		Size:     payloadSize,
	}, nil

}

// Dynamically create client fail out if error
func (a *App) Client() Client {
	if a.client != nil {
		return a.client
	}

	c, err := NewClient(a.MountPoint)
	if err != nil {
		log.Fatalf("failed to create client for mount %s: %v", a.MountPoint, err)
	}
	a.client = c
	return a.client
}
func main() {
	app := newApp()
	app.Commands = []cmd.ABFSCmd{
		newTapCmd(app),
		newTapTestCmd(app),
		newSetDigestCmd(app),
		newGetDigestCmd(app),
	}

	clis := cmd.ABFSCmdToCli(app)
	cli.RunRoot(clis)
}
