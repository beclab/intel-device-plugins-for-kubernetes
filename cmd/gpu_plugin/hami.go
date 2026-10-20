// Copyright 2026 Intel Corporation. All Rights Reserved.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// HAMi mode (-hami) advertises discrete GPUs as vgpuResource and leaves
// placement to the HAMi scheduler: the plugin publishes the cards in HAMi's
// register format, and Allocate injects libvxpu for the cards HAMi chose.
// Integrated GPUs keep their gpu.intel.com/<driver> resource.
const (
	vgpuResource = "intel.com/vgpu"

	// HAMi's "id,count,devmem,devcore,type,numa,health,index,mode,arch:" list.
	hamiRegisterAnnotation = "hami.io/node-intel-register"
	// Separate from NVIDIA's hami.io/node-handshake, so a node with both
	// vendors' plugins does not have one overwrite the other's report.
	hamiHandshakeAnnotation = "hami.io/node-handshake-intel"
	// JSON list of hamiCardInfo: what the HAMi register has no field for.
	hamiCardsAnnotation   = "gpu.bytetrade.io/node-intel-cards"
	shareModeAnnotationFm = "sharemode.gpu.bytetrade.io/%s"

	shareModeExclusive   = "0"
	shareModeMemSlicing  = "1"
	supportTypeExclusive = "Exclusive"
	supportTypeMemSlice  = "MemorySlice"

	hamiDeviceMode     = "vxpu"
	hamiDeviceCores    = 100
	hamiRegisterPeriod = 30 * time.Second

	// Paths inside the containers libvxpu is injected into.
	vxpuContainerLib   = "/usr/local/vxpu/libvxpu.so"
	vxpuContainerCache = "/usr/local/vxpu/cache"
	vxpuCacheFile      = vxpuContainerCache + "/vxpu.cache"
)

// hamiCard is one discrete GPU as the HAMi path sees it.
type hamiCard struct {
	uuid     string
	card     string
	bdf      string
	model    string
	memMiB   int64
	numa     int
	index    int
	healthy  bool
	devSpecs []pluginapi.DeviceSpec
	mounts   []pluginapi.Mount
}

// hamiCardInfo is an entry of hamiCardsAnnotation. Card is the DRM card name
// of the legacy bytetrade.io/node-intel-register entry for the same GPU, so
// allocations recorded against it can be moved to UUID.
type hamiCardInfo struct {
	UUID         string   `json:"uuid"`
	Card         string   `json:"card"`
	BDF          string   `json:"bdf"`
	SupportTypes []string `json:"supportTypes"`
}

// hamiCardUUID is a cluster-unique, stable ID: HAMi maps device IDs to nodes,
// so the PCI address alone would collide between nodes. ':' separates cards in
// the register, so it cannot appear in the ID.
func hamiCardUUID(nodeName, bdf string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(nodeName))

	return fmt.Sprintf("intel-%08x-%s", h.Sum32(), strings.ReplaceAll(bdf, ":", "-"))
}

func hamiModelType(name string) string {
	name = strings.NewReplacer(",", " ", ":", " ", ";", " ").Replace(strings.TrimSpace(name))
	if name == "" {
		return "Intel-GPU"
	}

	if strings.Contains(name, "Intel") {
		return name
	}

	return "Intel-" + name
}

func numaNodeForCard(cardPath string) int {
	dat, err := os.ReadFile(filepath.Join(cardPath, "device", "numa_node"))
	if err != nil {
		return 0
	}

	numa, err := strconv.Atoi(strings.TrimSpace(string(dat)))
	if err != nil || numa < 0 {
		return 0
	}

	return numa
}

func (dp *devicePlugin) supportTypes() []string {
	if dp.vxpuHostLib == "" {
		return []string{supportTypeExclusive}
	}

	return []string{supportTypeExclusive, supportTypeMemSlice}
}

func encodeHAMIRegister(cards []hamiCard, count int) string {
	var b strings.Builder

	for _, c := range cards {
		fmt.Fprintf(&b, "%s,%d,%d,%d,%s,%d,%t,%d,%s,0:", c.uuid, count, c.memMiB, hamiDeviceCores,
			c.model, c.numa, c.healthy, c.index, hamiDeviceMode)
	}

	return b.String()
}

// hamiRegisterAnnotations is the patch publishing cards. A card whose share
// mode was never set starts Exclusive; an existing value belongs to
// app-service and is left alone.
func (dp *devicePlugin) hamiRegisterAnnotations(cards []hamiCard, existing map[string]string, now time.Time) (map[string]string, error) {
	infos := make([]hamiCardInfo, 0, len(cards))
	annos := map[string]string{
		hamiRegisterAnnotation:  encodeHAMIRegister(cards, dp.options.sharedDevNum),
		hamiHandshakeAnnotation: "Reported " + now.String(),
	}

	for _, c := range cards {
		infos = append(infos, hamiCardInfo{UUID: c.uuid, Card: c.card, BDF: c.bdf, SupportTypes: dp.supportTypes()})

		key := fmt.Sprintf(shareModeAnnotationFm, c.uuid)
		if _, ok := existing[key]; !ok {
			annos[key] = shareModeExclusive
		}
	}

	data, err := json.Marshal(infos)
	if err != nil {
		return nil, err
	}

	annos[hamiCardsAnnotation] = string(data)

	return annos, nil
}

func (dp *devicePlugin) hamiCardsSnapshot() []hamiCard {
	dp.allocMutex.Lock()
	defer dp.allocMutex.Unlock()

	return append([]hamiCard(nil), dp.hamiCards...)
}

func (dp *devicePlugin) publishHAMIRegister(ctx context.Context) error {
	cards := dp.hamiCardsSnapshot()
	if len(cards) == 0 {
		return nil
	}

	node, err := dp.kubeClient.CoreV1().Nodes().Get(ctx, dp.nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	annos, err := dp.hamiRegisterAnnotations(cards, node.Annotations, time.Now())
	if err != nil {
		return err
	}

	return patchNodeAnnotations(ctx, dp.kubeClient, dp.nodeName, annos)
}

// runHAMIRegister re-reports on every period even when nothing changed: the
// HAMi scheduler answers each report with a "Requesting" handshake and drops
// the node's cards when no report follows within a minute.
func (dp *devicePlugin) runHAMIRegister(ctx context.Context) {
	ticker := time.NewTicker(hamiRegisterPeriod)
	defer ticker.Stop()

	for {
		if err := dp.publishHAMIRegister(ctx); err != nil {
			klog.Warningf("hami: failed to publish the register on node %s: %v", dp.nodeName, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-dp.scanDone:
			return
		case <-ticker.C:
		}
	}
}

func patchNodeAnnotations(ctx context.Context, clientset kubernetes.Interface, nodeName string, annos map[string]string) error {
	data, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annos}})
	if err != nil {
		return err
	}

	_, err = clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, data, metav1.PatchOptions{})

	return err
}

// installVXPU copies libvxpu from the plugin image into hostDir under a name
// carrying its content hash, and writes the ld.so.preload injected next to
// it. A running container keeps the file it was started with, so an upgrade
// never changes the library under it. Returns the installed path.
func installVXPU(src, hostDir string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)
	dst := filepath.Join(hostDir, "libvxpu.so."+hex.EncodeToString(sum[:])[:12])

	if err := os.MkdirAll(filepath.Join(hostDir, "containers"), 0o755); err != nil {
		return "", err
	}

	if _, err := os.Stat(dst); err != nil {
		if err := writeFileAtomic(dst, data, 0o755); err != nil {
			return "", err
		}
	}

	if err := writeFileAtomic(filepath.Join(hostDir, "ld.so.preload"), []byte(vxpuContainerLib+"\n"), 0o644); err != nil {
		return "", err
	}

	return dst, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
