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

package rbe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	re "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type mockStore struct {
	entities.CASStore
	openBlobFunc func(ctx context.Context, key entities.Hash) (io.ReadCloser, error)
}

func (m *mockStore) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	if m.openBlobFunc != nil {
		return m.openBlobFunc(ctx, key)
	}
	return nil, io.EOF
}

func TestRBEInspect_Action(t *testing.T) {
	action := &re.Action{
		CommandDigest: &re.Digest{
			Hash:      "1234567890abcdef1234567890abcdef12345678",
			SizeBytes: 100,
		},
		InputRootDigest: &re.Digest{
			Hash:      "fedcba0987654321fedcba0987654321fedcba09",
			SizeBytes: 200,
		},
		DoNotCache: true,
	}
	data, err := proto.Marshal(action)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000001")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			assert.Equal(t, hash, key)
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "action", hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	assert.Regexp(t, `"commandDigest":\s+{`, output)
	assert.Contains(t, output, `"1234567890abcdef1234567890abcdef12345678"`)
	assert.Regexp(t, `"doNotCache":\s+true`, output)
}

func TestRBEInspect_ActionResult(t *testing.T) {
	ar := &re.ActionResult{
		ExitCode:  42,
		StdoutRaw: []byte("hello stdout"),
	}
	data, err := proto.Marshal(ar)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000002")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			assert.Equal(t, hash, key)
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "action-result", hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	assert.Regexp(t, `"exitCode":\s+42`, output)
	assert.Regexp(t, `"stdoutRaw":\s+"aGVsbG8gc3Rkb3V0"`, output) // base64 encoded stdout
}

func TestRBEInspect_Directory(t *testing.T) {
	dir := &re.Directory{
		Files: []*re.FileNode{
			{
				Name: "foo.txt",
				Digest: &re.Digest{
					Hash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					SizeBytes: 10,
				},
				IsExecutable: true,
			},
		},
	}
	data, err := proto.Marshal(dir)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000003")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			assert.Equal(t, hash, key)
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "directory", hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	assert.Regexp(t, `"name":\s+"foo.txt"`, output)
	assert.Regexp(t, `"isExecutable":\s+true`, output)
}

func TestRBEInspect_Directory_Recursive(t *testing.T) {
	subDir := &re.Directory{
		Files: []*re.FileNode{
			{
				Name: "bar.txt",
				Digest: &re.Digest{
					Hash:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					SizeBytes: 20,
				},
			},
		},
	}
	subData, err := proto.Marshal(subDir)
	assert.NoError(t, err)

	subHash := entities.NewHash("0000000000000000000000000000000000000005")

	rootDir := &re.Directory{
		Files: []*re.FileNode{
			{
				Name: "foo.txt",
				Digest: &re.Digest{
					Hash:      "cccccccccccccccccccccccccccccccccccccccc",
					SizeBytes: 10,
				},
			},
		},
		Directories: []*re.DirectoryNode{
			{
				Name: "subdir",
				Digest: &re.Digest{
					Hash:      subHash.String(),
					SizeBytes: int64(len(subData)),
				},
			},
		},
	}
	rootData, err := proto.Marshal(rootDir)
	assert.NoError(t, err)

	rootHash := entities.NewHash("0000000000000000000000000000000000000003")

	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			if key == rootHash {
				return io.NopCloser(bytes.NewReader(rootData)), nil
			}
			if key == subHash {
				return io.NopCloser(bytes.NewReader(subData)), nil
			}
			return nil, fmt.Errorf("not found")
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "directory", "--recursive", rootHash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "HASH                                                                             TYPE       SIZE       PATH")
	assert.NotContains(t, output, "Directory: .")
	assert.NotContains(t, output, "Directory: subdir")

	expectedOrderRegex := `(?s)cccccccccccccccccccccccccccccccccccccccc\s+FILE\s+10\s+foo\.txt` +
		`.*` + subHash.String() + `\s+DIR\s+\d+\s+subdir` +
		`.*bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\s+FILE\s+20\s+subdir/bar\.txt`
	assert.Regexp(t, expectedOrderRegex, output)
}

func TestRBEInspect_Recursive_NonDirectory_Error(t *testing.T) {
	action := &re.Action{
		DoNotCache: true,
	}
	data, err := proto.Marshal(action)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000001")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "action", "--recursive", hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "recursive flag can only be used with directory objects")
}

func TestRBEInspect_Command(t *testing.T) {
	command := &re.Command{
		Arguments: []string{"echo", "hello"},
		EnvironmentVariables: []*re.Command_EnvironmentVariable{
			{
				Name:  "PATH",
				Value: "/bin",
			},
		},
	}
	data, err := proto.Marshal(command)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000004")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			assert.Equal(t, hash, key)
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{"--type", "command", hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	assert.Regexp(t, `"arguments":\s+\[`, output)
	assert.Contains(t, output, `"echo"`)
	assert.Contains(t, output, `"hello"`)
	assert.Regexp(t, `"name":\s+"PATH"`, output)
	assert.Regexp(t, `"value":\s+"/bin"`, output)
}

func TestRBEInspect_AutoDetect_Action(t *testing.T) {
	// Test Action auto-detect
	action := &re.Action{
		CommandDigest: &re.Digest{
			Hash:      "1234567890abcdef1234567890abcdef12345678",
			SizeBytes: 100,
		},
	}
	data, err := proto.Marshal(action)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000001")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	errOutput := errBuf.String()
	assert.Equal(t, "Auto-detected type: action\n", errOutput)
	assert.Regexp(t, `"commandDigest":\s+{`, output)
}

func TestRBEInspect_AutoDetect_Command(t *testing.T) {
	command := &re.Command{
		Arguments: []string{"ls", "-l"},
	}
	data, err := proto.Marshal(command)
	assert.NoError(t, err)

	hash := entities.NewHash("0000000000000000000000000000000000000004")
	ms := &mockStore{
		openBlobFunc: func(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}

	buf := bytes.NewBuffer(nil)
	errBuf := bytes.NewBuffer(nil)
	c := &rbeCmd{
		out:    buf,
		errOut: errBuf,
		StoreProvider: &cmd.StoreProvider{
			Store: ms,
		},
	}

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	err = fl.Parse([]string{hash.String()})
	assert.NoError(t, err)

	err = c.Exec(context.Background(), fl)
	assert.NoError(t, err)

	output := buf.String()
	errOutput := errBuf.String()
	assert.Equal(t, "Auto-detected type: command\n", errOutput)
	assert.Regexp(t, `"arguments":\s+\[`, output)
}
