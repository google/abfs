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

package ninjalog

import (
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/google/abfs/shared/entities"
)

const (
	DefaultLogFileName = ".ninja_log"
	ninjaLogHeader     = "# ninjago log v1"
	// lining up columns
	timestampWidth = 13 // Unix milliseconds.
	durationWidth  = 8  // Seconds with millisecond precision.
	hashWidth      = entities.HashHexSize
	mismatchWidth  = len("Mismatch")
)

var (
	// runTypeWidth fits the "Cached" heading and the longest RunType.
	runTypeWidth = func() int {
		w := len("Cached")
		for _, rt := range allRunTypes {
			w = max(w, len(rt))
		}
		return w
	}()

	columnsHeader = fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s %*s %s\n",
		timestampWidth, "#Timestamp",
		durationWidth, "Duration",
		runTypeWidth, "Cached",
		hashWidth, "Request-Hash",
		hashWidth, "Response-Hash",
		mismatchWidth, "Mismatch",
		"Target",
	)
	abfsSignature = ninjaLogHeader + "\n" + columnsHeader
)

type NinjaLog struct {
	fileName  string
	writeLock sync.Mutex
	writeFile *os.File
}

func (n *NinjaLog) Name() string {
	return n.fileName
}

func New(fileName string) *NinjaLog {
	return &NinjaLog{fileName: fileName}
}

func (n *NinjaLog) OpenForWrite() error {
	n.writeLock.Lock()
	defer n.writeLock.Unlock()

	if n.writeFile != nil {
		return fmt.Errorf("ninja log (%q) already open for write", n.fileName)
	}

	f, err := os.OpenFile(n.fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(f, abfsSignature)
	if err != nil {
		if err = f.Close(); err != nil {
			log.Printf("failed to close ninja log file %q: %v", n.fileName, err)
		}
		return err
	}

	n.writeFile = f
	return nil
}

func (n *NinjaLog) WriteEntry(entry *LogEntry) error {
	n.writeLock.Lock()
	defer n.writeLock.Unlock()

	if n.writeFile == nil {
		return fmt.Errorf("ninja log (%q) not open for write", n.fileName)
	}

	_, err := fmt.Fprintf(n.writeFile, "%-*d %*.3f %-*s %-*s %-*s %*d %s\n",
		timestampWidth, entry.Time.UnixMilli(),
		durationWidth, entry.Duration.Seconds(),
		runTypeWidth, entry.RunType,
		hashWidth, entry.ReqHash,
		hashWidth, entry.RespHash,
		mismatchWidth, entry.Mismatches,
		entry.Target,
	)
	return err
}

func (n *NinjaLog) Close() error {
	n.writeLock.Lock()
	defer n.writeLock.Unlock()
	if n.writeFile == nil {
		return nil
	}
	err := n.writeFile.Close()
	n.writeFile = nil
	return err
}
