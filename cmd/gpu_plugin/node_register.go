// Copyright 2024 Intel Corporation. All Rights Reserved.
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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// nodeIntelRegisterAnnotation is written on the node this plugin runs on.
	// Its value is a ':'-separated list of per-card entries, each of which is
	// a ','-separated tuple
	// "<igpu|dgpu>,<cardN>,<i915|xe>,<name>,<architecture>,<codename>,<mem>".
	// mem is the discrete VRAM size in bytes (0 for integrated GPUs). It lets
	// app-service pick the correct extended resource (gpu.intel.com/i915 vs
	// gpu.intel.com/xe) from the real bound driver instead of guessing from
	// the integrated/discrete distinction, and exposes product metadata.
	nodeIntelRegisterAnnotation = "bytetrade.io/node-intel-register"

	// nodeNameEnvVar carries the name of the node the pod runs on. It must be
	// injected via the downward API (spec.nodeName) in the DaemonSet.
	nodeNameEnvVar = "NODE_NAME"

	// nodeRegisterResyncPeriod bounds how often we re-check the node
	// annotation even without a device-count change, so an externally wiped
	// annotation is eventually restored. No-op writes are still skipped.
	nodeRegisterResyncPeriod = 60 * time.Second

	gpuKindIntegrated = "igpu"
	gpuKindDiscrete   = "dgpu"
)

// intelRegisterEntry describes a single Intel GPU card discovered during scan.
type intelRegisterEntry struct {
	kind         string // igpu | dgpu
	card         string // e.g. card0
	driver       string // i915 | xe
	name         string // product name, e.g. "Intel® Arc™ A770 Graphics"
	architecture string // e.g. Xe-HPG
	codename     string // e.g. Alchemist
	mem          uint64 // discrete VRAM in bytes; 0 for integrated
}

// gpuKindForCard classifies a DRM card as an integrated (igpu) or discrete
// (dgpu) GPU by inspecting its PCI topology, mirroring the reference detection:
// an integrated GPU hangs directly off the root complex (its parent in sysfs
// is the "pci0000:00" host bridge), whereas a discrete GPU sits behind a PCIe
// root port / bridge (any other parent). It returns an error when the card's
// PCI path can't be resolved.
func gpuKindForCard(cardPath string) (string, error) {
	real, err := filepath.EvalSymlinks(filepath.Join(cardPath, "device"))
	if err != nil {
		return "", errors.Wrapf(err, "can't resolve PCI path for %s", cardPath)
	}

	if filepath.Base(filepath.Dir(real)) == "pci0000:00" {
		return gpuKindIntegrated, nil
	}

	return gpuKindDiscrete, nil
}

// vramBytesForCard returns the discrete VRAM of a card in bytes, preferring the
// Level-Zero sidecar (GetDeviceMemoryAmount) as the source of truth. Integrated
// GPUs report 0. When the Level-Zero service is unavailable, its client is not
// yet ready, or it reports 0, it falls back to the sysfs-based cardMemoryBytes.
func (dp *devicePlugin) vramBytesForCard(cardPath, kind string) uint64 {
	if kind != gpuKindDiscrete {
		return 0
	}
	klog.V(0).Infof("dp.levelzeroservice: %v", dp.levelzeroService)
	if dp.levelzeroService != nil {
		klog.V(0).Infof("vramBytesForCard: not nil levelzeroService")
		if bdf, ok := bdfForCard(cardPath); ok {
			mem, err := dp.levelzeroService.GetDeviceMemoryAmount(bdf)
			if err != nil {
				klog.V(4).Infof("node-register: Level-Zero memory query for %s (%s) failed, falling back to sysfs: %v", cardPath, bdf, err)
			} else if mem > 0 {
				klog.V(0).Infof("vramBytesForCard: mem: %v", mem)
				return mem
			}
		}
	}

	return cardMemoryBytes(cardPath, kind)
}

// cardMemoryBytes returns the discrete VRAM of a card in bytes. Integrated GPUs
// report 0. The sysfs layout differs per driver / kernel version:
//   - xe: per-tile "physical_vram_size_bytes" (under device/tile*/ or
//     device/tile*/memory/); summed across tiles.
//   - i915: a single per-tile "lmem_total_bytes" at the card level, multiplied
//     by the tile count (mirrors the labeler's legacy path).
//
// When no VRAM attribute exists (common: the value moved to the DRM query
// IOCTL, or the driver simply doesn't expose it) it returns 0 without a noisy
// log, since this runs on every scan.
func cardMemoryBytes(cardPath, kind string) uint64 {
	if kind != gpuKindDiscrete {
		return 0
	}
	reserved := getEnvVarNumber("GPU_MEMORY_RESERVED")

	if dat, err := os.ReadFile(filepath.Join(cardPath, "lmem_total_bytes")); err == nil {
		if perTile, perr := strconv.ParseUint(strings.TrimSpace(string(dat)), 0, 64); perr == nil {
			return perTile*cardTileCount(cardPath) - reserved
		}
	}

	klog.V(4).Infof("node-register: no VRAM sysfs attribute for %s; reporting mem=0", cardPath)

	return 0
}
func getEnvVarNumber(envVarName string) uint64 {
	envValue := os.Getenv(envVarName)
	if envValue != "" {
		val, err := strconv.ParseUint(envValue, 10, 64)
		if err == nil {
			return val
		}
	}

	return 0
}

// cardTileCount counts GPU tiles: gt/gt* (i915) plus device/tile? (Xe). At
// least 1.
func cardTileCount(cardPath string) uint64 {
	files, _ := filepath.Glob(filepath.Join(cardPath, "gt", "gt*"))
	tiles, _ := filepath.Glob(filepath.Join(cardPath, "device", "tile?"))
	count := len(files) + len(tiles)

	if count == 0 {
		return 1
	}

	return uint64(count)
}

// formatIntelRegister renders entries into the annotation value:
// "<kind>,<card>,<driver>,<name>,<architecture>,<codename>,<mem>" joined by ':'.
func formatIntelRegister(entries []intelRegisterEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, strings.Join([]string{
			e.kind,
			e.card,
			e.driver,
			e.name,
			e.architecture,
			e.codename,
			strconv.FormatUint(e.mem, 10),
		}, ","))
	}

	return strings.Join(parts, ":")
}

// runNodeRegister publishes the latest scan() register snapshot to the node
// annotation. It is intentionally decoupled from scan() (which runs every few
// seconds and is exercised directly by unit tests): the annotation is patched
// only when the device set changes (via scanResources) or on a periodic
// resync, and no-op writes are skipped. Failure to build a client only
// disables publishing; it never blocks device discovery.
func (dp *devicePlugin) runNodeRegister(ctx context.Context) {
	nodeName := os.Getenv(nodeNameEnvVar)
	if nodeName == "" {
		klog.Warningf("node-register: %s env is empty, skipping node annotation publishing", nodeNameEnvVar)

		return
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		klog.Warningf("node-register: can't build in-cluster config, skipping: %v", err)

		return
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Warningf("node-register: can't build clientset, skipping: %v", err)

		return
	}

	ticker := time.NewTicker(nodeRegisterResyncPeriod)
	defer ticker.Stop()

	var (
		lastWritten string
		haveWritten bool
	)

	reconcile := func() {
		dp.allocMutex.Lock()
		entries := make([]intelRegisterEntry, len(dp.intelRegister))
		copy(entries, dp.intelRegister)
		dp.allocMutex.Unlock()

		value := formatIntelRegister(entries)
		if haveWritten && value == lastWritten {
			return
		}

		if err := patchNodeIntelRegister(ctx, clientset, nodeName, value); err != nil {
			klog.Warningf("node-register: failed to patch node %s annotation: %v", nodeName, err)

			return
		}

		klog.V(1).Infof("node-register: updated %s on node %s = %q", nodeIntelRegisterAnnotation, nodeName, value)

		lastWritten = value
		haveWritten = true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-dp.scanDone:
			return
		case <-dp.scanResources:
			reconcile()
		case <-ticker.C:
			reconcile()
		}
	}
}

// patchNodeIntelRegister sets (or, when value is empty, removes) the register
// annotation on the given node using a JSON merge patch.
func patchNodeIntelRegister(ctx context.Context, clientset kubernetes.Interface, nodeName, value string) error {
	var annotationValue interface{}
	if value == "" {
		annotationValue = nil
	} else {
		annotationValue = value
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				nodeIntelRegisterAnnotation: annotationValue,
			},
		},
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	_, err = clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, data, metav1.PatchOptions{})

	return err
}
