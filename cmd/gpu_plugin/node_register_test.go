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
	"os"
	"path/filepath"
	"testing"
)

func TestFormatIntelRegister(t *testing.T) {
	cases := []struct {
		name    string
		entries []intelRegisterEntry
		want    string
	}{
		{
			name:    "empty",
			entries: nil,
			want:    "",
		},
		{
			name: "single integrated (mem 0)",
			entries: []intelRegisterEntry{{
				kind: gpuKindIntegrated, card: "card0", driver: "i915",
				name: "Intel® Iris® Xe Graphics", architecture: "Xe", codename: "Tiger Lake", mem: 0,
			}},
			want: "igpu,card0,i915,Intel® Iris® Xe Graphics,Xe,Tiger Lake,0",
		},
		{
			name: "multiple with discrete mem",
			entries: []intelRegisterEntry{
				{kind: gpuKindIntegrated, card: "card0", driver: "i915", name: "Intel® UHD Graphics", architecture: "Xe", codename: "Alder Lake-P", mem: 0},
				{kind: gpuKindDiscrete, card: "card1", driver: "xe", name: "Intel® Arc™ B580 Graphics", architecture: "Xe2", codename: "Battlemage", mem: 12884901888},
			},
			want: "igpu,card0,i915,Intel® UHD Graphics,Xe,Alder Lake-P,0:dgpu,card1,xe,Intel® Arc™ B580 Graphics,Xe2,Battlemage,12884901888",
		},
		{
			name:    "unknown model leaves metadata empty",
			entries: []intelRegisterEntry{{kind: gpuKindDiscrete, card: "card0", driver: "xe", mem: 0}},
			want:    "dgpu,card0,xe,,,,0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatIntelRegister(tc.entries); got != tc.want {
				t.Errorf("formatIntelRegister() = %q, want %q", got, tc.want)
			}
		})
	}
}

// linkCard creates a fake DRM card whose "device" symlink resolves to pciReal,
// which is created under root. Returns the card path.
func linkCard(t *testing.T, root, card, pciReal string) string {
	t.Helper()

	realPath := filepath.Join(root, pciReal)
	if err := os.MkdirAll(realPath, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", realPath, err)
	}

	cardPath := filepath.Join(root, "sys", "class", "drm", card)
	if err := os.MkdirAll(cardPath, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", cardPath, err)
	}

	if err := os.Symlink(realPath, filepath.Join(cardPath, "device")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	return cardPath
}

func TestLookupGPUModel(t *testing.T) {
	// sysfs form with 0x prefix and uppercase.
	if m := lookupGPUModel("0x56A0"); m.name != "Intel® Arc™ A770 Graphics" || m.architecture != "Xe-HPG" || m.codename != "Alchemist" {
		t.Errorf("lookupGPUModel(0x56A0) = %+v", m)
	}

	// lowercase without prefix, with whitespace.
	if m := lookupGPUModel(" 9a49 "); m.codename != "Tiger Lake" {
		t.Errorf("lookupGPUModel(9a49) = %+v", m)
	}

	// unknown -> empty.
	if m := lookupGPUModel("0xffff"); m != (gpuModel{}) {
		t.Errorf("lookupGPUModel(0xffff) = %+v, want empty", m)
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}

	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestCardMemoryBytes(t *testing.T) {
	// Integrated GPUs always report 0, even if a stray file exists.
	igpu := filepath.Join(t.TempDir(), "card0")
	writeFile(t, filepath.Join(igpu, "lmem_total_bytes"), "123\n")
	if got := cardMemoryBytes(igpu, gpuKindIntegrated); got != 0 {
		t.Errorf("integrated mem = %d, want 0", got)
	}

	// xe: sum of per-tile physical_vram_size_bytes.
	xe := filepath.Join(t.TempDir(), "card0")
	writeFile(t, filepath.Join(xe, "device", "tile0", "memory", "physical_vram_size_bytes"), "8000000000\n")
	writeFile(t, filepath.Join(xe, "device", "tile1", "memory", "physical_vram_size_bytes"), "8000000000\n")
	if got := cardMemoryBytes(xe, gpuKindDiscrete); got != 16000000000 {
		t.Errorf("xe mem = %d, want 16000000000", got)
	}

	// i915: lmem_total_bytes per tile * tile count.
	i915 := filepath.Join(t.TempDir(), "card0")
	writeFile(t, filepath.Join(i915, "lmem_total_bytes"), "17179869184\n")
	writeFile(t, filepath.Join(i915, "gt", "gt0", "id"), "0\n")
	if got := cardMemoryBytes(i915, gpuKindDiscrete); got != 17179869184 {
		t.Errorf("i915 mem = %d, want 17179869184", got)
	}

	// No VRAM attribute -> 0 (no panic, no error).
	none := filepath.Join(t.TempDir(), "card0")
	if err := os.MkdirAll(none, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := cardMemoryBytes(none, gpuKindDiscrete); got != 0 {
		t.Errorf("missing mem = %d, want 0", got)
	}
}

func TestGPUKindForCard(t *testing.T) {
	root := t.TempDir()

	// Integrated GPU hangs directly off the host bridge pci0000:00.
	igpuCard := linkCard(t, root, "card0", "devices/pci0000:00/0000:00:02.0")
	// Discrete GPU sits behind a PCIe root port / bridge.
	dgpuCard := linkCard(t, root, "card1", "devices/pci0000:00/0000:00:01.0/0000:01:00.0")

	if got, err := gpuKindForCard(igpuCard); err != nil || got != gpuKindIntegrated {
		t.Errorf("gpuKindForCard(igpu) = (%q, %v), want (%q, nil)", got, err, gpuKindIntegrated)
	}

	if got, err := gpuKindForCard(dgpuCard); err != nil || got != gpuKindDiscrete {
		t.Errorf("gpuKindForCard(dgpu) = (%q, %v), want (%q, nil)", got, err, gpuKindDiscrete)
	}

	// A card without a resolvable device link returns an error.
	missing := filepath.Join(root, "sys", "class", "drm", "card9")
	if err := os.MkdirAll(missing, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", missing, err)
	}

	if got, err := gpuKindForCard(missing); err == nil {
		t.Errorf("gpuKindForCard(missing) = (%q, nil), want error", got)
	}
}
