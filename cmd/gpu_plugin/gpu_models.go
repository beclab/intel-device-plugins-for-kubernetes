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

import "strings"

// gpuModel is the marketing/architecture metadata for an Intel GPU PCI device
// id. There is no runtime API (sysfs / Level-Zero / device plugin) that exposes
// the human product name, architecture or codename, so this table is
// hand-maintained from Intel's supported-hardware documentation:
//
//	https://dgpu-docs.intel.com/overview/supported-hardware/i915-driver-gpus.html
//	https://dgpu-docs.intel.com/overview/supported-hardware/xe-driver-gpus.html
type gpuModel struct {
	name         string
	architecture string
	codename     string
}

// gpuModels maps a lowercase PCI device id (without the "0x" prefix) to its
// product metadata. Rows that list several ids in the Intel tables are expanded
// to one entry per id here.
var gpuModels = map[string]gpuModel{
	// --- i915-driver GPUs ---
	"7d51": {"Intel® Graphics", "Xe-LPG", "Arrow Lake-H"},
	"7d67": {"Intel® Graphics", "Xe-LPG", "Arrow Lake-S"},
	"7d41": {"Intel® Graphics", "Xe-LPG", "Arrow Lake-U"},
	"7dd5": {"Intel® Graphics", "Xe-LPG", "Meteor Lake"},
	"7d45": {"Intel® Graphics", "Xe-LPG", "Meteor Lake"},
	"7d40": {"Intel® Graphics", "Xe-LPG", "Meteor Lake"},
	"7d55": {"Intel® Arc™ Graphics", "Xe-LPG", "Meteor Lake"},
	"0bd5": {"Intel® Data Center GPU Max 1550", "Xe-HPC", "Ponte Vecchio"},
	"0bda": {"Intel® Data Center GPU Max 1100", "Xe-HPC", "Ponte Vecchio"},
	"56c0": {"Intel® Data Center GPU Flex 170", "Xe-HPG", "Alchemist"},
	"56c1": {"Intel® Data Center GPU Flex 140", "Xe-HPG", "Alchemist"},
	"5690": {"Intel® Arc™ A770M Graphics", "Xe-HPG", "Alchemist"},
	"5691": {"Intel® Arc™ A730M Graphics", "Xe-HPG", "Alchemist"},
	"5696": {"Intel® Arc™ A570M Graphics", "Xe-HPG", "Alchemist"},
	"5692": {"Intel® Arc™ A550M Graphics", "Xe-HPG", "Alchemist"},
	"5697": {"Intel® Arc™ A530M Graphics", "Xe-HPG", "Alchemist"},
	"5693": {"Intel® Arc™ A370M Graphics", "Xe-HPG", "Alchemist"},
	"5694": {"Intel® Arc™ A350M Graphics", "Xe-HPG", "Alchemist"},
	"56a0": {"Intel® Arc™ A770 Graphics", "Xe-HPG", "Alchemist"},
	"56a1": {"Intel® Arc™ A750 Graphics", "Xe-HPG", "Alchemist"},
	"56a2": {"Intel® Arc™ A580 Graphics", "Xe-HPG", "Alchemist"},
	"56a5": {"Intel® Arc™ A380 Graphics", "Xe-HPG", "Alchemist"},
	"56a6": {"Intel® Arc™ A310 Graphics", "Xe-HPG", "Alchemist"},
	"56b3": {"Intel® Arc™ Pro A60 Graphics", "Xe-HPG", "Alchemist"},
	"56b2": {"Intel® Arc™ Pro A60M Graphics", "Xe-HPG", "Alchemist"},
	"56b1": {"Intel® Arc™ Pro A40/A50 Graphics", "Xe-HPG", "Alchemist"},
	"56b0": {"Intel® Arc™ Pro A30M Graphics", "Xe-HPG", "Alchemist"},
	"56ba": {"Intel® Arc™ A380E Graphics", "Xe-HPG", "Alchemist"},
	"56bc": {"Intel® Arc™ A370E Graphics", "Xe-HPG", "Alchemist"},
	"56bd": {"Intel® Arc™ A350E Graphics", "Xe-HPG", "Alchemist"},
	"56bb": {"Intel® Arc™ A310E Graphics", "Xe-HPG", "Alchemist"},
	"a780": {"Intel® UHD Graphics 770", "Xe", "Raptor Lake-S"},
	"a781": {"Intel® UHD Graphics", "Xe", "Raptor Lake-S"},
	"a788": {"Intel® UHD Graphics", "Xe", "Raptor Lake-S"},
	"a789": {"Intel® UHD Graphics", "Xe", "Raptor Lake-S"},
	"a78a": {"Intel® UHD Graphics", "Xe", "Raptor Lake-S"},
	"a782": {"Intel® UHD Graphics 730", "Xe", "Raptor Lake-S"},
	"a78b": {"Intel® UHD Graphics", "Xe", "Raptor Lake-S"},
	"a783": {"Intel® UHD Graphics 710", "Xe", "Raptor Lake-S"},
	"a7a0": {"Intel® Iris® Xe Graphics", "Xe", "Raptor Lake-P"},
	"a7a1": {"Intel® Iris® Xe Graphics", "Xe", "Raptor Lake-P"},
	"a7a8": {"Intel® UHD Graphics", "Xe", "Raptor Lake-P"},
	"a7aa": {"Intel® Graphics", "Xe", "Raptor Lake-P"},
	"a7ab": {"Intel® Graphics", "Xe", "Raptor Lake-P"},
	"a7ac": {"Intel® Graphics", "Xe", "Raptor Lake-U"},
	"a7ad": {"Intel® Graphics", "Xe", "Raptor Lake-U"},
	"a7a9": {"Intel® UHD Graphics", "Xe", "Raptor Lake-P"},
	"a721": {"Intel® UHD Graphics", "Xe", "Raptor Lake-P"},
	"4905": {"Intel® Iris® Xe MAX Graphics", "Xe", "DG1"},
	"4907": {"Intel Server GPU SG-18M", "Xe", "DG1"},
	"4908": {"Intel® Iris® Xe Graphics", "Xe", "DG1"},
	"4909": {"Intel® Iris® Xe MAX 100 Graphics", "Xe", "DG1"},
	"4680": {"Intel® UHD Graphics 770", "Xe", "Alder Lake-S"},
	"4690": {"Intel® UHD Graphics 770", "Xe", "Alder Lake-S"},
	"4688": {"Intel® UHD Graphics 770", "Xe", "Alder Lake-S"},
	"468a": {"Intel® UHD Graphics 770", "Xe", "Alder Lake-S"},
	"468b": {"Intel® UHD Graphics 770", "Xe", "Alder Lake-S"},
	"4682": {"Intel® UHD Graphics 730", "Xe", "Alder Lake-S"},
	"4692": {"Intel® UHD Graphics 730", "Xe", "Alder Lake-S"},
	"4693": {"Intel® UHD Graphics 710", "Xe", "Alder Lake-S"},
	"46d3": {"Intel® Graphics", "Xe", "Twin Lake"},
	"46d4": {"Intel® Graphics", "Xe", "Twin Lake"},
	"46d0": {"Intel® UHD Graphics", "Xe", "Alder Lake-N"},
	"46d1": {"Intel® UHD Graphics", "Xe", "Alder Lake-N"},
	"46d2": {"Intel® UHD Graphics", "Xe", "Alder Lake-N"},
	"4626": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"4628": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"462a": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46a2": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46b3": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46c2": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46a3": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46b2": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46c3": {"Intel® UHD Graphics", "Xe", "Alder Lake-P"},
	"46a0": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46b0": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46c0": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46a6": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46aa": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46a8": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46a1": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46b1": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"46c1": {"Intel® Iris® Xe Graphics", "Xe", "Alder Lake-P"},
	"4c8a": {"Intel® UHD Graphics 750", "Xe", "Rocket Lake"},
	"4c8b": {"Intel® UHD Graphics 730", "Xe", "Rocket Lake"},
	"4c90": {"Intel® UHD Graphics P750", "Xe", "Rocket Lake"},
	"4c9a": {"Intel® UHD Graphics P750", "Xe", "Rocket Lake"},
	"4e71": {"Intel® UHD Graphics", "Xe", "Jasper Lake"},
	"4e61": {"Intel® UHD Graphics", "Xe", "Jasper Lake"},
	"4e57": {"Intel® UHD Graphics", "Xe", "Jasper Lake"},
	"4e55": {"Intel® UHD Graphics", "Xe", "Jasper Lake"},
	"4e51": {"Intel® UHD Graphics", "Xe", "Jasper Lake"},
	"4557": {"Intel® UHD Graphics", "Xe", "Elkhart Lake"},
	"4555": {"Intel® UHD Graphics", "Xe", "Elkhart Lake"},
	"4571": {"Intel® UHD Graphics", "Xe", "Elkhart Lake"},
	"4551": {"Intel® UHD Graphics", "Xe", "Elkhart Lake"},
	"4541": {"Intel® UHD Graphics", "Xe", "Elkhart Lake"},
	"9a59": {"Intel® UHD Graphics", "Xe", "Tiger Lake"},
	"9a78": {"Intel® UHD Graphics", "Xe", "Tiger Lake"},
	"9a60": {"Intel® UHD Graphics", "Xe", "Tiger Lake"},
	"9a70": {"Intel® UHD Graphics", "Xe", "Tiger Lake"},
	"9a68": {"Intel® UHD Graphics", "Xe", "Tiger Lake"},
	"9a40": {"Intel® Iris® Xe Graphics", "Xe", "Tiger Lake"},
	"9a49": {"Intel® Iris® Xe Graphics", "Xe", "Tiger Lake"},

	// --- Xe-driver GPUs ---
	"e223": {"Intel® Arc™ Pro B70 Graphics", "Xe2", "Battlemage"},
	"e222": {"Intel® Arc™ Pro B65 Graphics", "Xe2", "Battlemage"},
	"e212": {"Intel® Arc™ Pro B50 Graphics", "Xe2", "Battlemage"},
	"e211": {"Intel® Arc™ Pro B60 Graphics", "Xe2", "Battlemage"},
	"e20b": {"Intel® Arc™ B580 Graphics", "Xe2", "Battlemage"},
	"e20c": {"Intel® Arc™ B570 Graphics", "Xe2", "Battlemage"},
	"b080": {"Intel® Arc™ B390 GPU", "Xe3", "Panther Lake"},
	"b082": {"Intel® Arc™ B390 GPU", "Xe3", "Panther Lake"},
	"b084": {"Intel® Arc™ B390 GPU", "Xe3", "Panther Lake"},
	"b086": {"Intel® Arc™ B390 GPU", "Xe3", "Panther Lake"},
	"b081": {"Intel® Arc™ B370 GPU", "Xe3", "Panther Lake"},
	"b083": {"Intel® Arc™ B370 GPU", "Xe3", "Panther Lake"},
	"b085": {"Intel® Arc™ B370 GPU", "Xe3", "Panther Lake"},
	"b087": {"Intel® Arc™ B370 GPU", "Xe3", "Panther Lake"},
	"b090": {"Intel® Graphics", "Xe3", "Panther Lake"},
	"b0a0": {"Intel® Graphics", "Xe3", "Panther Lake"},
	"64a0": {"Intel® Arc™ Graphics", "Xe2", "Lunar Lake"},
	"6420": {"Intel® Graphics", "Xe2", "Lunar Lake"},
}

// lookupGPUModel returns the product metadata for a PCI device id read from
// sysfs (e.g. "0x9a49" or "9A49"). Unknown ids yield an empty gpuModel.
func lookupGPUModel(deviceID string) gpuModel {
	id := strings.ToLower(strings.TrimSpace(deviceID))
	id = strings.TrimPrefix(id, "0x")

	return gpuModels[id]
}
