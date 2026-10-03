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

package tap

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"syscall"

	"github.com/google/abfs/shared/entities"
)

const TapSeqVersion = 0x03

func OSModeToFSMode(mode int32) fs.FileMode {
	out := fs.FileMode(mode & 0777)
	switch mode & syscall.S_IFMT {
	case syscall.S_IFBLK:
		out |= fs.ModeDevice
	case syscall.S_IFCHR:
		out |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFDIR:
		out |= fs.ModeDir
	case syscall.S_IFIFO:
		out |= fs.ModeNamedPipe
	case syscall.S_IFLNK:
		out |= fs.ModeSymlink
	case syscall.S_IFREG:
	case syscall.S_IFSOCK:
		out |= fs.ModeSocket
	}
	if mode&syscall.S_ISGID != 0 {
		out |= fs.ModeSetgid
	}
	if mode&syscall.S_ISUID != 0 {
		out |= fs.ModeSetuid
	}
	if mode&syscall.S_ISVTX != 0 {
		out |= fs.ModeSticky
	}
	return out
}

func TapDecode(reader io.Reader, taps chan *entities.TapFSEntry) error {
	buf := bufio.NewReaderSize(reader, MaxChunkSize)
	v, err := buf.ReadByte()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	if v != TapSeqVersion {
		return fmt.Errorf("unsupported tap version %d", v)
	}
	for {
		t, err := buf.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		dirty, err := buf.ReadByte()
		if err != nil {
			return err
		}
		isARI, err := buf.ReadByte()
		if err != nil {
			return err
		}

		tap := new(entities.TapFSEntry)
		tap.Type = entities.TapType(t)
		tap.Dirty = (dirty != 0)
		tap.IsARI = (isARI != 0)

		if tap.IsARI {
			tap.ARI, err = buf.ReadString('\x00')
			if err != nil {
				return err
			}
			tap.ARI = tap.ARI[:len(tap.ARI)-1]
		} else {
			if _, err := io.ReadFull(buf, tap.Hash[:]); err != nil {
				return err
			}
		}

		if err := binary.Read(buf, binary.NativeEndian, &tap.Size); err != nil {
			return err
		}

		var mode int32
		if err := binary.Read(buf, binary.NativeEndian, &mode); err != nil {
			return err
		}
		// Convert mode
		tap.Mode = OSModeToFSMode(mode)
		tap.Path, err = buf.ReadString('\x00')
		if err != nil {
			return err
		}
		tap.Path = tap.Path[:len(tap.Path)-1]

		tap.AuxPath, err = buf.ReadString('\x00')
		if err != nil {
			return err
		}
		tap.AuxPath = tap.AuxPath[:len(tap.AuxPath)-1]
		taps <- tap
	}
}
