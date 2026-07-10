// Copyright 2025 Intel Corporation. All Rights Reserved.
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
	"path"
	"slices"
	"strings"

	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	// npuDeviceRE matches NPU (accel) device names, e.g. accel0.
	npuDeviceRE = `^accel[0-9]+$`
)

// npuIDs lists the known Intel NPU PCI device IDs, ported from the NPU device
// plugin so that discovery stays consistent between the two.
var npuIDs = []string{
	"0x7e4c", // Core Ultra Series 1
	"0x643e", // Core Ultra 200V Series
	"0xad1d", // Core Ultra Series 2
	"0x7d1d", // Core Ultra Series 2 (H)
	"0xb03e", // Core Ultra Series 3
	"0xfd3e", // WCL
	"0xd71d", // NVL
}

// isCompatibleNPUDevice reports whether the given accel device is a supported
// Intel NPU. Ported from the NPU device plugin to keep discovery identical.
func (dp *devicePlugin) isCompatibleNPUDevice(name string) bool {
	if !dp.npuDeviceReg.MatchString(name) {
		klog.V(4).Info("Incompatible NPU device: ", name)
		return false
	}

	dat, err := os.ReadFile(path.Join(dp.npuSysfsDir, name, "device/vendor"))
	if err != nil {
		klog.Warning("Skipping. Can't read NPU vendor file: ", err)
		return false
	}

	if strings.TrimSpace(string(dat)) != vendorString {
		klog.V(4).Info("Non-Intel accelerator device: ", name)
		return false
	}

	dat, err = os.ReadFile(path.Join(dp.npuSysfsDir, name, "device/device"))
	if err != nil {
		klog.Warning("Skipping. Can't read NPU device file: ", err)
		return false
	}

	datStr := strings.Split(string(dat), "\n")[0]
	if !slices.Contains(npuIDs, datStr) {
		klog.Warning("Unknown NPU device ID: ", datStr)
		return false
	}

	return true
}

// scanNPU discovers Intel NPU (accel) devices on the node and returns the
// device specs needed to expose them to a container. It returns nil when no
// NPU devices are present (e.g. the accel sysfs directory does not exist).
func (dp *devicePlugin) scanNPU() []pluginapi.DeviceSpec {
	files, err := os.ReadDir(dp.npuSysfsDir)
	if err != nil {
		klog.Infof("NPU scan: can't read sysfs dir %q: %v", dp.npuSysfsDir, err)
		return nil
	}

	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}

	klog.Infof("NPU scan: sysfs dir %q has %d entrie(s): %v (devfs dir %q)",
		dp.npuSysfsDir, len(files), names, dp.npuDevfsDir)

	var specs []pluginapi.DeviceSpec

	for _, f := range files {
		name := f.Name()

		if !dp.isCompatibleNPUDevice(name) {
			klog.Infof("NPU scan: %q is not a compatible NPU device, skipping", name)
			continue
		}

		devPath := path.Join(dp.npuDevfsDir, name)
		if _, err = os.Stat(devPath); err != nil {
			klog.Infof("NPU scan: dev node %q missing (%v), skipping", devPath, err)
			continue
		}

		klog.Infof("Found NPU device %s at %s", name, devPath)

		// even querying metrics requires the device to be writable
		specs = append(specs, pluginapi.DeviceSpec{
			HostPath:      devPath,
			ContainerPath: devPath,
			Permissions:   "rw",
		})
	}

	klog.Infof("NPU scan found %d device(s): %+v", len(specs), specs)

	return specs
}
