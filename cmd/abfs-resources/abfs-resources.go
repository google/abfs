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
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

type DisplayMode int

const (
	ModeAbsolute DisplayMode = iota // Default
	ModeDelta                       // 'd' -> Change since last poll
	ModeBaseline                    // 'D' -> Change since app started
)

type LayoutStyle int

const (
	LayoutTable LayoutStyle = iota // Force wide multi-column row charts
	LayoutCard                     // Force compact vertical text card layouts
)

type GlobalParams struct {
	MaxMounts     int64 `json:"casfs_max_mounts"`
	MaxGitPools   int64 `json:"casfs_max_git_pools"`
	MaxTapEntries int64 `json:"casfs_max_tap_entries"`
}
type SlabMetrics struct {
	Active int64 `json:"active_objects"`
	Total  int64 `json:"total_objects"`
	Size   int64 `json:"object_size_bytes"`
}

type CacheStorageProfile struct {
	ID            int           `json:"cache_id"`
	Path          string        `json:"cache_path"`
	Version       string        `json:"cache_version"`
	ObjectCount   int64         `json:"total_objects_count"`
	ObjectBytes   int64         `json:"total_objects_bytes"`
	BlobCount     int64         `json:"total_blobs_count"`
	BlobBytes     int64         `json:"total_blobs_bytes"`
	IsScanning    bool          `json:"is_scanning"`
	LastUpdated   time.Time     `json:"last_updated"`
	ScratchCounts map[int]int64 `json:"instance_scratch_counts"`
}

type MountProfile struct {
	InstanceID   int         `json:"instance_id"`
	MountPoint   string      `json:"mount_point"`
	CacheDir     string      `json:"cache_directory"`
	CacheID      int         `json:"cache_id"`
	ScratchDir   string      `json:"scratch_directory"`
	ScratchFiles int64       `json:"scratch_file_count"`
	NodeMetrics  SlabMetrics `json:"node_slab_metrics"`
	TapMetrics   SlabMetrics `json:"tap_slab_metrics"`
	NotifySlab   SlabMetrics `json:"notify_slab_metrics"`
}

func (p *MountProfile) TotalMemory() int64 {
	baseNodeMem := p.NodeMetrics.Total * p.NodeMetrics.Size
	baseTapMem := p.TapMetrics.Total * p.TapMetrics.Size
	baseNotifyMem := p.NotifySlab.Total * p.NotifySlab.Size
	primaryPathOverhead := p.TapMetrics.Active * 512
	estimatedRenameCount := int64(float64(p.TapMetrics.Active) * 0.05)
	auxPathOverhead := estimatedRenameCount * 512
	return baseNodeMem + baseTapMem + primaryPathOverhead + auxPathOverhead + baseNotifyMem
}

type SystemSnapshot struct {
	Timestamp    time.Time                       `json:"timestamp"`
	Mounts       []*MountProfile                 `json:"active_mounts"`
	SharedCaches map[string]*CacheStorageProfile `json:"shared_caches"`
	KernelLogs   []string                        `json:"kernel_logs"`
	Params       GlobalParams                    `json:"global_parameters"`
}

type HostMemoryInfo struct {
	Total     int64
	Available int64
}

type DiskSpaceInfo struct {
	TotalSizeBytes int64
	FreeSizeBytes  int64
	UsedSizeBytes  int64
	PercentFull    float64
}

func readUnderlyingDiskSpace(path string) (*DiskSpaceInfo, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return nil, err
	}
	totalBytes := int64(stat.Blocks) * int64(stat.Bsize)
	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)
	usedBytes := totalBytes - freeBytes

	var percentFull float64
	if totalBytes > 0 {
		percentFull = (float64(usedBytes) / float64(totalBytes)) * 100.0
	}
	return &DiskSpaceInfo{
		TotalSizeBytes: totalBytes,
		FreeSizeBytes:  freeBytes,
		UsedSizeBytes:  usedBytes,
		PercentFull:    percentFull,
	}, nil
}

var (
	storageRegistry       = make(map[string]*CacheStorageProfile)
	nextCacheID           = 0
	registryMutex         sync.Mutex
	currentMode           = ModeAbsolute
	currentLayout         = LayoutCard
	firstSnap             *SystemSnapshot
	lastSnap              *SystemSnapshot
	originalTerminalState *term.State
	isPaused              = false
	isVerbose             = false
)

func main() {
	disableInputBuffering()
	defer enableInputBuffering()

	keyChan := make(chan string, 1)
	go keyboardListener(keyChan)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	go StartKernelLogWorker()
	var runtimeErr error
	if err := updateUI(); err != nil {
		runtimeErr = err
	}

RuntimeLoop:
	for runtimeErr == nil {
		select {
		case <-sigChan:
			break RuntimeLoop

		case char := <-keyChan:
			if char == "quit" {
				break RuntimeLoop
			}

			switch char {
			case " ":
				isPaused = !isPaused
				runtimeErr = updateUI()
				continue
			case "a", "A":
				if !isPaused {
					currentMode = ModeAbsolute
				}
			case "d":
				if !isPaused {
					currentMode = ModeDelta
				}
			case "D":
				if !isPaused {
					currentMode = ModeBaseline
				}
			case "l", "L":
				if !isPaused {
					currentLayout = LayoutStyle((int(currentLayout) + 1) % 2)
				}

			case "v", "V":
				if !isPaused {
					isVerbose = !isVerbose
				}
			}

			if !isPaused {
				runtimeErr = updateUI()
			}

		case <-ticker.C:
			if !isPaused {
				runtimeErr = updateUI()
			}
		}
	}

	clearTerminal()
	if runtimeErr != nil {
		fmt.Fprintf(os.Stderr, "Fatal Runtime Error: %v\n", runtimeErr)
		os.Exit(1)
	}
}

func updateUI() error {
	snapshot, err := captureSnapshot()
	if err != nil {
		return fmt.Errorf("failed to capture system snapshot: %w", err)
	}

	var globalWorkerActive bool
	for _, c := range snapshot.SharedCaches {
		if c.IsScanning {
			globalWorkerActive = true
			break
		}
	}

	clearTerminal()
	printHeader(globalWorkerActive)
	printGlobalParametersBlock(&snapshot.Params)

	if firstSnap == nil {
		firstSnap = snapshot
	} else {
		for path, currCache := range snapshot.SharedCaches {
			if firstCache, exists := firstSnap.SharedCaches[path]; exists {
				if firstCache.ObjectCount == 0 && currCache.ObjectCount > 0 {
					firstSnap.SharedCaches[path] = currCache
				}
			}
		}
		for _, currMount := range snapshot.Mounts {
			for _, firstMount := range firstSnap.Mounts {
				if currMount.InstanceID == firstMount.InstanceID {
					if firstMount.ScratchFiles == 0 && currMount.ScratchFiles > 0 {
						firstMount.ScratchFiles = currMount.ScratchFiles
					}
				}
			}
		}
	}

	if lastSnap == nil {
		lastSnap = snapshot
	}

	if len(snapshot.Mounts) == 0 {
		fmt.Println("No active casfs mount points or slab caches detected.\r")
	} else {
		renderLayout(snapshot, firstSnap, lastSnap, currentMode)
	}

	lastSnap = snapshot
	return nil
}

func printHeader(isWorkerActive bool) {
	fmt.Printf("=== ABFS Resource Profiler ===\r\n\n")
	fmt.Printf("Time: %s\r\n", time.Now().Format(time.RFC1123))

	if isWorkerActive {
		fmt.Printf("Profiler State: \033[1;33mRUNNING (indexing...)\033[0m\r\n")
	} else {
		fmt.Printf("Profiler State: \033[1;32mIDLE (synced)\033[0m\r\n")
	}

	if isPaused {
		fmt.Printf("Display Status: \033[1;31mPAUSED (Press [SPACE] to resume data updates)\033[0m\r\n")
	} else {
		fmt.Printf("Display Status: \033[32mACTIVE (Press [SPACE] to freeze layout)\033[0m\r\n")
	}

	if isVerbose {
		fmt.Printf("Telemetry Log : \033[1;33m[VERBOSE DEBUGFS ON]\033[0m (Press 'v' to collapse details)\r\n")
	} else {
		fmt.Printf("Telemetry Log : [STANDARD] (Press 'v' for deep DebugFS metrics)\r\n")
	}

	switch currentLayout {
	case LayoutTable:
		fmt.Printf("Layout Profile: \033[1;36m[FORCE WIDE SPREADSHEET]\033[0m (Press 'l' to force change modes)\r\n")
	case LayoutCard:
		fmt.Printf("Layout Profile: \033[1;35m[FORCE COMPACT CARDS]\033[0m (Press 'l' to force change modes)\r\n")
	}

	switch currentMode {
	case ModeAbsolute:
		fmt.Printf("View Mode:      [ABSOLUTE] (Press 'd' for Last Delta, 'D' for Baseline Delta, 'a' for Absolute)\r\n\r\n")
	case ModeDelta:
		fmt.Printf("View Mode:      [LAST REFRESH DELTA] (Press 'a' for Absolute, 'D' for Baseline Delta)\r\n\r\n")
	case ModeBaseline:
		fmt.Printf("View Mode:      [INITIAL LAUNCH BASELINE DELTA] (Press 'a' for Absolute, 'd' for Last Delta)\r\n\r\n")
	}
}

func renderLayout(current, first, last *SystemSnapshot, mode DisplayMode) {

	if currentLayout == LayoutCard {
		printCardView(current, first, last, mode)
	} else {
		printWideTableView(current, first, last, mode)
	}
}

func printWideTableView(current, first, last *SystemSnapshot, mode DisplayMode) {
	const sep = "   |   "

	mountHeaderFmt := "%-24s" + sep + "%-12s" + sep + "%-24s" + sep + "%-24s" + sep + "%-24s" + sep + "%-14s" + sep + "%-18s\r\n"
	mountRowFmt := "%-24s" + sep + "%-12s" + sep + "%-24s" + sep + "%-24s" + sep + "%-24s" + sep + "%-14s" + sep + "%-18s\r\n"

	fmt.Printf(mountHeaderFmt, "ACTIVE VFS MOUNT", "MAPPED CACHE", "NODE METRICS (ACT/TOT)", "TAP METRICS (ACT/TOT)", "NOTIFY METRICS (ACT/TOT)", "SCRATCH ACTIVE", "EST KMEM OVERHEAD")
	fmt.Printf(mountHeaderFmt, "------------------------", "------------", "------------------------", "------------------------", "------------------------", "--------------", "------------------")

	var totalClusterMem int64
	lastMountMap := mapMounts(last)
	firstMountMap := mapMounts(first)

	sortedMounts := make([]*MountProfile, len(current.Mounts))
	copy(sortedMounts, current.Mounts)
	sort.Slice(sortedMounts, func(i, j int) bool {
		return sortedMounts[i].InstanceID < sortedMounts[j].InstanceID
	})

	for _, p := range sortedMounts {
		mountLocation := p.MountPoint
		if mountLocation == "" {
			mountLocation = "unmapped"
		}
		mountStr := fmt.Sprintf("%s (id: %d)", mountLocation, p.InstanceID)
		cachePointer := fmt.Sprintf("Cache #%d", p.CacheID)
		if p.CacheDir == "" {
			cachePointer = "None"
		}

		totalClusterMem += p.TotalMemory()
		var nodeStat, tapStat, notifyStat, scratchStat, memStr string

		if mode == ModeAbsolute {
			nodeStat = fmt.Sprintf("%d / %d", p.NodeMetrics.Active, p.NodeMetrics.Total)
			tapStat = fmt.Sprintf("%d / %d", p.TapMetrics.Active, p.TapMetrics.Total)
			notifyStat = fmt.Sprintf("%d / %d", p.NotifySlab.Active, p.NotifySlab.Total)
			scratchStat = fmt.Sprintf("%d files", p.ScratchFiles)
			memStr = formatBytes(p.TotalMemory())
		} else {
			var ref *MountProfile
			if mode == ModeDelta {
				ref = lastMountMap[p.InstanceID]
			} else {
				ref = firstMountMap[p.InstanceID]
			}

			if ref != nil {
				nodeStat = fmt.Sprintf("%d (%s) / %d", p.NodeMetrics.Active, formatDiff(p.NodeMetrics.Active, ref.NodeMetrics.Active), p.NodeMetrics.Total)
				tapStat = fmt.Sprintf("%d (%s) / %d", p.TapMetrics.Active, formatDiff(p.TapMetrics.Active, ref.TapMetrics.Active), p.TapMetrics.Total)
				notifyStat = fmt.Sprintf("%d (%s) / %d", p.NotifySlab.Active, formatDiff(p.NotifySlab.Active, ref.NotifySlab.Active), p.NotifySlab.Total)
				scratchStat = fmt.Sprintf("%d (%s)", p.ScratchFiles, formatDiff(p.ScratchFiles, ref.ScratchFiles))
				memStr = fmt.Sprintf("%s (%s)", formatBytes(p.TotalMemory()), formatDiffBytes(p.TotalMemory()-ref.TotalMemory()))
			} else {
				nodeStat = fmt.Sprintf("%d / %d", p.NodeMetrics.Active, p.NodeMetrics.Total)
				tapStat = fmt.Sprintf("%d / %d", p.TapMetrics.Active, p.TapMetrics.Total)
				notifyStat = fmt.Sprintf("%d / %d", p.NotifySlab.Active, p.NotifySlab.Total)
				scratchStat = fmt.Sprintf("%d files", p.ScratchFiles)
				memStr = formatBytes(p.TotalMemory())
			}
		}
		fmt.Printf(mountRowFmt, mountStr, cachePointer, nodeStat, tapStat, notifyStat, scratchStat, memStr)
	}
	printSystemGlobalMetrics(totalClusterMem)
	_ = printCacheStorageBlock(current)
	printKernelLogsBlock(current)
}

func printCardView(current, first, last *SystemSnapshot, mode DisplayMode) {
	lastMountMap := mapMounts(last)
	firstMountMap := mapMounts(first)

	sortedMounts := make([]*MountProfile, len(current.Mounts))
	copy(sortedMounts, current.Mounts)
	sort.Slice(sortedMounts, func(i, j int) bool {
		return sortedMounts[i].InstanceID < sortedMounts[j].InstanceID
	})

	var totalClusterMem int64

	for _, p := range sortedMounts {
		mountLocation := p.MountPoint
		if mountLocation == "" {
			mountLocation = "unmapped"
		}
		totalClusterMem += p.TotalMemory()

		fmt.Printf("--- Active Mount Info [ID: %d] ------------------------------------\r\n", p.InstanceID)
		fmt.Printf("  VFS Mount Point : %s\r\n", mountLocation)
		fmt.Printf("  Mapped Backend  : Cache #%d\r\n", p.CacheID)

		var nodeStat, tapStat, notifyStat, scratchStat, memStr string

		if mode == ModeAbsolute {
			nodeStat = fmt.Sprintf("%d / %d", p.NodeMetrics.Active, p.NodeMetrics.Total)
			tapStat = fmt.Sprintf("%d / %d", p.TapMetrics.Active, p.TapMetrics.Total)
			notifyStat = fmt.Sprintf("%d / %d", p.NotifySlab.Active, p.NotifySlab.Total)
			scratchStat = fmt.Sprintf("%d files", p.ScratchFiles)
			memStr = formatBytes(p.TotalMemory())
		} else {
			var ref *MountProfile
			if mode == ModeDelta {
				ref = lastMountMap[p.InstanceID]
			} else {
				ref = firstMountMap[p.InstanceID]
			}

			if ref != nil {
				nodeStat = fmt.Sprintf("%d (%s) / %d", p.NodeMetrics.Active, formatDiff(p.NodeMetrics.Active, ref.NodeMetrics.Active), p.NodeMetrics.Total)
				tapStat = fmt.Sprintf("%d (%s) / %d", p.TapMetrics.Active, formatDiff(p.TapMetrics.Active, ref.TapMetrics.Active), p.TapMetrics.Total)
				notifyStat = fmt.Sprintf("%d (%s) / %d", p.NotifySlab.Active, formatDiff(p.NotifySlab.Active, ref.NotifySlab.Active), p.NotifySlab.Total)
				scratchStat = fmt.Sprintf("%d (%s)", p.ScratchFiles, formatDiff(p.ScratchFiles, ref.ScratchFiles))
				memStr = fmt.Sprintf("%s (%s)", formatBytes(p.TotalMemory()), formatDiffBytes(p.TotalMemory()-ref.TotalMemory()))
			} else {
				nodeStat = fmt.Sprintf("%d / %d", p.NodeMetrics.Active, p.NodeMetrics.Total)
				tapStat = fmt.Sprintf("%d / %d", p.TapMetrics.Active, p.TapMetrics.Total)
				notifyStat = fmt.Sprintf("%d / %d", p.NotifySlab.Active, p.NotifySlab.Total)
				scratchStat = fmt.Sprintf("%d files", p.ScratchFiles)
				memStr = formatBytes(p.TotalMemory())
			}
		}

		fmt.Printf("  Node Metrics    : %s (ACT/TOT)\r\n", nodeStat)
		fmt.Printf("  Tap Metrics     : %s (ACT/TOT)\r\n", tapStat)
		fmt.Printf("  Provider Notify : %s (ACT/TOT)\r\n", notifyStat)
		fmt.Printf("  Scratch Space   : %s active\r\n", scratchStat)
		fmt.Printf("  Slab Overhead   : %s\r\n", memStr)
		if isVerbose {
			printDebugFSMountDetails(p.InstanceID)
		}
	}
	fmt.Printf("------------------------------------------------------------------------\r\n")

	printSystemGlobalMetrics(totalClusterMem)
	printCompactCacheBlock(current)
	printKernelLogsBlock(current)
}

func printDebugFSMountDetails(instanceID int) {
	debugfsPath := fmt.Sprintf("/sys/kernel/debug/casfs_%d", instanceID)

	// Helper closure to read simple u64 single-line text stats
	readU64File := func(subPath string) string {
		b, err := os.ReadFile(filepath.Join(debugfsPath, subPath))
		if err != nil {
			return "0"
		}
		return strings.TrimSpace(string(b))
	}

	// Shifting to 1;36m (Bold Cyan) + 2m (Dim text) for perfect visibility on dark screens
	fmt.Printf("    \033[1;36m└─\033[0m \033[1m[DebugFS Tap Core Engine]\033[0m\r\n")
	fmt.Printf("       Total Path Lookups : %-10s | Fast Stack Hits  : %s\r\n",
		readU64File("tap/paths_total"), readU64File("tap/paths_fast_stack"))
	fmt.Printf("       Slow Heap Fallbacks: %-10s | Retries On Wire  : %s\r\n",
		readU64File("tap/paths_slow_heap"), readU64File("provider/socket_retries"))

	// Extract and read the new session_overflows tracking file
	overflowCount := readU64File("session_overflows")
	if overflowCount != "0" {
		fmt.Printf("       \033[1;31mGuardrail Drops    : %-10s (ENOSPC Memory ceiling alerts)\033[0m\r\n", overflowCount)
	} else {
		fmt.Printf("       Guardrail Drops    : %-10s (Healthy slots)\r\n", overflowCount)
	}

	// 1. Parse and extract the Tap Queue HWM Depth Distribution Metrics
	if b, err := os.ReadFile(filepath.Join(debugfsPath, "tap/queue_depth_histogram")); err == nil {
		fmt.Printf("    \033[1;36m└─\033[0m \033[1m[DebugFS Tap Queue Distribution HWM]\033[0m\r\n")
		lines := strings.Split(string(b), "\n")
		var activeBuckets []string
		var absolutePeak string
		var mountWideHWM string

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "Absolute Peak Single Session HWM:") {
				parts := strings.Split(line, ":")
				if len(parts) == 2 {
					absolutePeak = strings.TrimSpace(parts[1])
				}
				continue
			}
			// Parse out the new lockless mount-wide metric
			if strings.Contains(line, "Mount-Wide HWM:") {
				parts := strings.Split(line, ":")
				if len(parts) == 2 {
					mountWideHWM = strings.TrimSpace(parts[1])
				}
				continue
			}

			if strings.Contains(line, ":") {
				parts := strings.Split(line, ":")
				val, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
				if val > 0 { // High-density filter: only list ranges that actually experienced traffic bursts
					rawRange := strings.TrimSpace(parts[0])
					// Clean up internal spacer formatting from "0 - 64      items" down to "0-64"
					cleanRange := strings.ReplaceAll(rawRange, " ", "")
					cleanRange = strings.ReplaceAll(cleanRange, "items", "")
					activeBuckets = append(activeBuckets, fmt.Sprintf("%s: %d", cleanRange, val))
				}
			}
		}

		if len(activeBuckets) > 0 {
			fmt.Printf("       Burst Ranges -> %s\r\n", strings.Join(activeBuckets, " | "))
		}
		if absolutePeak != "" && absolutePeak != "0" {
			fmt.Printf("       Absolute Single-Session Peak Queue HWM: %s items\r\n", absolutePeak)
		}
		if mountWideHWM != "" && mountWideHWM != "0" {
			fmt.Printf("       \033[1;33mMount-Wide Global Allocated HWM:\033[0m   %s items concurrent\r\n", mountWideHWM)
		}
	}

	if b, err := os.ReadFile(filepath.Join(debugfsPath, "provider/message_stats")); err == nil {
		fmt.Printf("    \033[1;36m└─\033[0m \033[1m[DebugFS Provider Protocol Audit]\033[0m\r\n")
		lines := strings.Split(string(b), "\n")
		var activeStats []string
		for _, line := range lines {
			if strings.Contains(line, ":") {
				parts := strings.SplitN(line, ":", 2)
				val, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
				if val > 0 {
					cleanName := strings.TrimSpace(parts[0])
					if idx := strings.Index(cleanName, "("); idx != -1 {
						cleanName = strings.TrimSpace(cleanName[:idx])
					}
					activeStats = append(activeStats, fmt.Sprintf("%s: %d", cleanName, val))
				}
			}
		}
		if len(activeStats) > 0 {
			fmt.Printf("       Active Frames -> %s\r\n", strings.Join(activeStats, " | "))
		} else {
			fmt.Printf("       Active Frames -> None (No provider traffic transferred yet)\r\n")
		}
	}
}
func printSystemGlobalMetrics(totalClusterMem int64) {
	fmt.Printf("\r\n>> Cumulative Subsystem Kernel Slab Overhead: %s\r\n", formatBytes(totalClusterMem))
	sysMem, sysErr := readHostMemoryInfo()
	sysTotalStr := "unknown"
	sysAvailStr := "unknown"
	if sysErr == nil {
		sysTotalStr = formatBytes(sysMem.Total)
		sysAvailStr = formatBytes(sysMem.Available)
	}
	fmt.Printf(">> Host Physical Machine Total Memory:        %s\r\n", sysTotalStr)
	fmt.Printf(">> Host Physical Machine Available Headroom:  %s\r\n\r\n", sysAvailStr)
}

func printCompactCacheBlock(current *SystemSnapshot) {
	// Compact view condenses backing target paths utilizing shorthand homedir tildes
	for _, c := range current.SharedCaches {
		fmt.Printf("--- Cache Storage Properties [Cache #%d] ---------------------------\r\n", c.ID)
		fmt.Printf("  Version Anchor : %s\r\n", c.Version)

		displayPath := c.Path
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if rel, ok := strings.CutPrefix(displayPath, home+"/"); ok {
				displayPath = "~/" + rel
			}
		}
		fmt.Printf("  Backing Path   : %s\r\n", displayPath)
		fmt.Printf("  Stored Objects : %d (%s)\r\n", c.ObjectCount, formatBytes(c.ObjectBytes))
		fmt.Printf("  Stored Blobs   : %d (%s)\r\n", c.BlobCount, formatBytes(c.BlobBytes))

		diskInfo, err := readUnderlyingDiskSpace(c.Path)
		if err == nil {
			fmt.Printf("  Disk Occupancy : %s / %s Full (%.1f%%)\r\n",
				formatBytes(diskInfo.UsedSizeBytes),
				formatBytes(diskInfo.TotalSizeBytes),
				diskInfo.PercentFull)

			cachePercentage := (float64(c.ObjectBytes+c.BlobBytes) / float64(diskInfo.TotalSizeBytes)) * 100.0
			fmt.Printf("  Cache Footprint: %s (%.1f%% of underlying device)\r\n",
				formatBytes(c.ObjectBytes+c.BlobBytes),
				cachePercentage)
		}
	}
	fmt.Printf("------------------------------------------------------------------------\r\n")
}

func printCacheStorageBlock(current *SystemSnapshot) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)

	if _, err := fmt.Fprintln(w, "CACHE REF\tBACKING PATH\tVERSION\tTOTAL OBJECTS STORED\tTOTAL BLOBS STORED\tUNDERLYING DISK STATUS\r"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "---------\t------------\t-------\t--------------------\t------------------\t----------------------\r"); err != nil {
		return err
	}

	for _, c := range current.SharedCaches {
		cacheRef := fmt.Sprintf("Cache #%d", c.ID)
		objStr := fmt.Sprintf("%d (%s)", c.ObjectCount, formatBytes(c.ObjectBytes))
		blobStr := fmt.Sprintf("%d (%s)", c.BlobCount, formatBytes(c.BlobBytes))

		diskStr := "unknown"
		diskInfo, err := readUnderlyingDiskSpace(c.Path)
		if err == nil {
			cachePercentage := (float64(c.ObjectBytes+c.BlobBytes) / float64(diskInfo.TotalSizeBytes)) * 100.0
			diskStr = fmt.Sprintf("%s/%s Full (%.1f%%) | Cache: %s (%.1f%%)",
				formatBytes(diskInfo.UsedSizeBytes),
				formatBytes(diskInfo.TotalSizeBytes),
				diskInfo.PercentFull,
				formatBytes(c.ObjectBytes+c.BlobBytes),
				cachePercentage)
		}

		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\r\n", cacheRef, c.Path, c.Version, objStr, blobStr, diskStr); err != nil {
			return err
		}
	}

	return w.Flush()
}
func keyboardListener(ch chan string) {
	var buf [1]byte
	for {
		_, err := os.Stdin.Read(buf[:])
		if err != nil {
			return
		}
		// Catch Ctrl+C (3), 'q', or 'Q' and send a unified exit token
		if buf[0] == 3 || buf[0] == 'q' || buf[0] == 'Q' {
			ch <- "quit"
			return
		}
		ch <- string(buf[0])
	}
}

func disableInputBuffering() {
	fmt.Print("\033[?25l")
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err == nil {
			originalTerminalState = state
		}
	}
}

func enableInputBuffering() {
	fmt.Print("\033[?25h")
	fd := int(os.Stdin.Fd())
	if originalTerminalState != nil && term.IsTerminal(fd) {
		_ = term.Restore(fd, originalTerminalState)
	}
}

func mapMounts(snap *SystemSnapshot) map[int]*MountProfile {
	m := make(map[int]*MountProfile)
	if snap == nil {
		return m
	}
	for _, p := range snap.Mounts {
		m[p.InstanceID] = p
	}
	return m
}

func formatDiff(current, reference int64) string {
	diff := current - reference
	if diff > 0 {
		return fmt.Sprintf("+%d", diff)
	} else if diff < 0 {
		return fmt.Sprintf("%d", diff)
	}
	return "+0"
}

func formatDiffBytes(diff int64) string {
	if diff == 0 {
		return "+0 B"
	}
	prefix := ""
	if diff > 0 {
		prefix = "+"
	} else {
		prefix = "-"
		diff = -diff
	}
	return prefix + formatBytes(diff)
}

func captureSnapshot() (*SystemSnapshot, error) {
	snapshot := &SystemSnapshot{Timestamp: time.Now(), Mounts: make([]*MountProfile, 0), SharedCaches: make(map[string]*CacheStorageProfile)}
	mountMap, err := parseCasfsMounts()
	if err != nil {
		return nil, err
	}
	slabProfiles, err := profileSlabCaches()
	if err != nil {
		return nil, err
	}

	snapshot.Params.MaxMounts, _ = readSysfsInt("/sys/module/casfs/parameters/casfs_max_mounts")
	snapshot.Params.MaxGitPools, _ = readSysfsInt("/sys/module/casfs/parameters/casfs_max_git_pools")
	snapshot.Params.MaxTapEntries, _ = readSysfsInt("/sys/module/casfs/parameters/casfs_max_tap_entries")

	registryMutex.Lock()
	defer registryMutex.Unlock()

	for id, profile := range slabProfiles {
		if mountInfo, matched := mountMap[id]; matched {
			profile.MountPoint = mountInfo.MountPoint
			profile.CacheDir = mountInfo.CacheDir
			profile.ScratchDir = mountInfo.ScratchDir

			if profile.CacheDir != "" {
				if _, exists := storageRegistry[profile.CacheDir]; !exists {
					storageRegistry[profile.CacheDir] = &CacheStorageProfile{
						ID:            nextCacheID,
						Path:          profile.CacheDir,
						Version:       "",
						ScratchCounts: make(map[int]int64),
					}
					nextCacheID++
				}

				cProfile := storageRegistry[profile.CacheDir]
				profile.CacheID = cProfile.ID

				profile.ScratchFiles = cProfile.ScratchCounts[profile.InstanceID]

				if !cProfile.IsScanning && time.Since(cProfile.LastUpdated) > 30*time.Second {
					cProfile.IsScanning = true

					type scanTarget struct {
						instanceID int
						scratchDir string
					}
					var targets []scanTarget
					for instID, mInfo := range mountMap {
						if mInfo.CacheDir == profile.CacheDir && mInfo.ScratchDir != "" {
							targets = append(targets, scanTarget{instanceID: instID, scratchDir: mInfo.ScratchDir})
						}
					}

					go func(cPath string, tList []scanTarget) {
						version := "unknown"
						if vBytes, err := os.ReadFile(filepath.Join(cPath, "version")); err == nil {
							version = strings.TrimSpace(string(vBytes))
						}

						objCount, objBytes := walkShardedStore(filepath.Join(cPath, "objects"))
						blobCount, blobBytes := walkShardedStore(filepath.Join(cPath, "blobs"))

						localScratchMap := make(map[int]int64)
						for _, t := range tList {
							localScratchMap[t.instanceID] = countDirectoryFiles(t.scratchDir)
						}

						registryMutex.Lock()
						if p, exists := storageRegistry[cPath]; exists {
							p.Version = version
							p.ObjectCount = objCount
							p.ObjectBytes = objBytes
							p.BlobCount = blobCount
							p.BlobBytes = blobBytes
							p.ScratchCounts = localScratchMap
							p.IsScanning = false
							p.LastUpdated = time.Now()
						}
						registryMutex.Unlock()
					}(profile.CacheDir, targets)
				}

				snapshot.SharedCaches[profile.CacheDir] = &CacheStorageProfile{
					ID:            cProfile.ID,
					Path:          cProfile.Path,
					Version:       cProfile.Version,
					ObjectCount:   cProfile.ObjectCount,
					ObjectBytes:   cProfile.ObjectBytes,
					BlobCount:     cProfile.BlobCount,
					BlobBytes:     cProfile.BlobBytes,
					IsScanning:    cProfile.IsScanning,
					ScratchCounts: cProfile.ScratchCounts,
					LastUpdated:   cProfile.LastUpdated,
				}
			}
		}
		snapshot.Mounts = append(snapshot.Mounts, profile)
	}
	snapshot.KernelLogs = globalLogRing.Get()
	return snapshot, nil
}

func walkShardedStore(basePath string) (int64, int64) {
	subDirs, err := os.ReadDir(basePath)
	if err != nil {
		return 0, 0
	}
	var totalFiles, totalBytes int64
	for _, d := range subDirs {
		if !d.IsDir() || len(d.Name()) != 3 {
			continue
		}
		shardPath := filepath.Join(basePath, d.Name())
		files, err := os.ReadDir(shardPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() {
				totalFiles++
				info, err := f.Info()
				if err != nil {
					continue
				}
				totalBytes += info.Size()
			}
		}
	}
	return totalFiles, totalBytes
}

type vfsMountInfo struct{ MountPoint, CacheDir, ScratchDir string }

func parseCasfsMounts() (map[int]*vfsMountInfo, error) {
	mountsContent, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	mountMap := make(map[int]*vfsMountInfo)
	for _, line := range strings.Split(string(mountsContent), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "casfs" {
			continue
		}
		mountPoint := fields[1]
		optsStr := strings.TrimSuffix(strings.TrimPrefix(fields[3], "("), ")")
		var instanceID int
		var cacheDir, scratchVal string
		var foundInstance bool
		for _, opt := range strings.Split(optsStr, ",") {
			kv := strings.SplitN(opt, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "instance":
				if id, err := strconv.Atoi(kv[1]); err == nil {
					instanceID = id
					foundInstance = true
				}
			case "cache":
				cacheDir = kv[1]
				if strings.HasPrefix(cacheDir, "~") {
					home, _ := os.UserHomeDir()
					cacheDir = filepath.Join(home, cacheDir[1:])
				}
				if !filepath.IsAbs(cacheDir) && !strings.HasPrefix(cacheDir, "/") {
					cacheDir = "/" + cacheDir
				}
			case "scratch":
				scratchVal = kv[1]
			}
		}
		if foundInstance {
			info := &vfsMountInfo{MountPoint: mountPoint, CacheDir: cacheDir}
			if cacheDir != "" && scratchVal != "" {
				info.ScratchDir = filepath.Join(cacheDir, "scratch", scratchVal)
			}
			mountMap[instanceID] = info
		}
	}
	return mountMap, nil
}

func profileSlabCaches() (map[int]*MountProfile, error) {
	basePath := "/sys/kernel/slab"
	files, err := os.ReadDir(basePath)
	if err != nil {
		return nil, err
	}
	profiles := make(map[int]*MountProfile)
	for _, file := range files {
		name := file.Name()
		if !strings.HasPrefix(name, "casfs_") {
			continue
		}
		parts := strings.Split(name, "_")
		if len(parts) != 3 {
			continue
		}
		id, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		if _, exists := profiles[id]; !exists {
			profiles[id] = &MountProfile{InstanceID: id}
		}
		p := profiles[id]
		slabDir := filepath.Join(basePath, name)
		objsActive, _ := readSysfsInt(filepath.Join(slabDir, "objects"))
		objsTotal, _ := readSysfsInt(filepath.Join(slabDir, "total_objects"))
		objSize, _ := readSysfsInt(filepath.Join(slabDir, "object_size"))
		switch parts[1] {
		case "node":
			p.NodeMetrics = SlabMetrics{Active: objsActive, Total: objsTotal, Size: objSize}
		case "tap":
			p.TapMetrics = SlabMetrics{Active: objsActive, Total: objsTotal, Size: objSize}
		case "notify":
			p.NotifySlab = SlabMetrics{Active: objsActive, Total: objsTotal, Size: objSize}
		}

	}
	return profiles, nil
}
func countDirectoryFiles(dirPath string) int64 {
	var count int64
	err := filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		return 0
	}
	return count
}

func readSysfsInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty sysfs")
	}
	return strconv.ParseInt(fields[0], 10, 64)
}

func readHostMemoryInfo() (*HostMemoryInfo, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	info := &HostMemoryInfo{}
	lines := strings.Split(string(data), "\n")
	var foundTotal, foundAvail bool
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			kbVal, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				info.Total = kbVal * 1024
				foundTotal = true
			}
		case "MemAvailable:":
			kbVal, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				info.Available = kbVal * 1024
				foundAvail = true
			}
		}
		if foundTotal && foundAvail {
			return info, nil
		}
	}
	if foundTotal && !foundAvail {
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "MemFree:" {
				kbVal, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					info.Available = kbVal * 1024
					return info, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("could not resolve host memory metrics")
}

func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(1024), 0
	for n := b / 1024; n >= 1024; n /= 1024 {
		div *= 1024
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func clearTerminal() { fmt.Print("\033[H\033[2J") }

// Ring buffer to hold the last N logs safely
type CasfsLogRing struct {
	mu   sync.RWMutex
	logs []string
	max  int
}

func NewCasfsLogRing(max int) *CasfsLogRing {
	return &CasfsLogRing{logs: make([]string, 0, max), max: max}
}

func (r *CasfsLogRing) Push(log string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.logs) >= r.max {
		r.logs = r.logs[1:] // Drop oldest
	}
	r.logs = append(r.logs, log)
}

func (r *CasfsLogRing) Get() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Return a copy to avoid race conditions during UI render
	out := make([]string, len(r.logs))
	copy(out, r.logs)
	return out
}

var globalLogRing = NewCasfsLogRing(6) // Keep last 6 matching kernel lines

func getBootTimeBase() time.Time {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return time.Now() // Fallback if system call fails
	}
	// Subtract system uptime from current real time
	return time.Now().Add(-time.Duration(info.Uptime) * time.Second)
}
func StartKernelLogWorker() {
	file, err := os.Open("/dev/kmsg")
	if err != nil {
		globalLogRing.Push(fmt.Sprintf("[Profiler Warning] Cannot access /dev/kmsg: %v", err))
		return
	}
	defer func() { _ = file.Close() }()

	bootTimeBase := getBootTimeBase()
	reader := bufio.NewReader(file)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				continue
			}
			break
		}

		// Look for your specific filesystem log identifier
		if strings.Contains(line, "casfs:") {
			// Expected format structure: info_prefix,seq,timestamp_us,flags;message_body
			headerAndBody := strings.SplitN(line, ";", 2)
			if len(headerAndBody) != 2 {
				continue
			}

			headerFields := strings.Split(headerAndBody[0], ",")
			if len(headerFields) >= 3 {
				// Parse the microsecond field (index 2)
				if us, err := strconv.ParseInt(headerFields[2], 10, 64); err == nil {
					// Correlate microsecond value to concrete calendar time
					logTime := bootTimeBase.Add(time.Duration(us) * time.Microsecond)

					// time.ANSIC matches the exact style used by 'dmesg -T'
					// Output Example: "Fri Jun 12 08:14:19 2026"
					timeStr := logTime.Format(time.ANSIC)

					msgBody := strings.TrimSpace(headerAndBody[1])

					// Frame it inside square brackets to mirror classic dmesg output
					formattedLog := fmt.Sprintf("[%s] %s", timeStr, msgBody)
					globalLogRing.Push(formattedLog)
				}
			}
		}
	}
}

func printKernelLogsBlock(current *SystemSnapshot) {
	fmt.Printf("\r\n=== Recent Kernel Log Messages (casfs) =================================\r\n")
	if len(current.KernelLogs) == 0 {
		fmt.Println("  (No casfs messages detected in kernel ring buffer yet)\r")
		return
	}
	for _, log := range current.KernelLogs {
		// Highlight errors cleanly on dark screens using soft orange/red hues
		if strings.Contains(strings.ToLower(log), "failed") || strings.Contains(strings.ToLower(log), "cannot") {
			fmt.Printf("  \033[1;31m[!] %s\033[0m\r\n", log)
		} else {
			fmt.Printf("  * %s\r\n", log)
		}
	}
	fmt.Printf("========================================================================\r\n")
}

func printGlobalParametersBlock(p *GlobalParams) {
	fmt.Printf("=== Kernel Module Global Initialization Parameters =====================\r\n")
	fmt.Printf("  casfs_max_mounts     : %-8d | casfs_max_git_pools : %-8d\r\n", p.MaxMounts, p.MaxGitPools)
	fmt.Printf("  casfs_max_tap_entries: %-8d (Memory Guardrail Cap ceiling)\r\n", p.MaxTapEntries)
	fmt.Printf("========================================================================\r\n\r\n")
}
