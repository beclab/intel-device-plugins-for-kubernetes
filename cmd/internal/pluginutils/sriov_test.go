// Copyright 2021-2026 Intel Corporation. All Rights Reserved.
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

package pluginutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSriovVF(t *testing.T) {
	root := t.TempDir()
	pf := filepath.Join(root, "card0")
	vf := filepath.Join(root, "card1")

	if err := os.MkdirAll(filepath.Join(pf, "device"), 0750); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(vf, "device"), 0750); err != nil {
		t.Fatal(err)
	}

	if IsSriovVF(pf) {
		t.Fatal("PF without physfn should not be reported as VF")
	}

	if err := os.Symlink(filepath.Join(pf, "device"), filepath.Join(vf, "device", "physfn")); err != nil {
		t.Fatal(err)
	}

	if !IsSriovVF(vf) {
		t.Fatal("VF with physfn should be reported as VF")
	}
}

func TestIsSriovPFwithVFs(t *testing.T) {
	root := t.TempDir()
	pf := filepath.Join(root, "card0")

	if err := os.MkdirAll(filepath.Join(pf, "device"), 0750); err != nil {
		t.Fatal(err)
	}

	if IsSriovPFwithVFs(pf) {
		t.Fatal("missing sriov_numvfs should not count as PF with VFs")
	}

	if err := os.WriteFile(filepath.Join(pf, "device", "sriov_numvfs"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if IsSriovPFwithVFs(pf) {
		t.Fatal("sriov_numvfs=0 should not count as PF with VFs")
	}

	if err := os.WriteFile(filepath.Join(pf, "device", "sriov_numvfs"), []byte("2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if !IsSriovPFwithVFs(pf) {
		t.Fatal("sriov_numvfs=2 should count as PF with VFs")
	}
}
