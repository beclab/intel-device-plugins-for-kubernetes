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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	testNode = "node-a"
	testBDF  = "0000:03:00.0"
)

func TestHAMIScanAdvertisesDiscreteCardsAsVGPU(t *testing.T) {
	root := t.TempDir()
	tc := TestCaseDetails{
		pciAddresses: map[string]string{testBDF: "card1"},
		sysfsdirs:    []string{"card1/device/drm/card1", "card1/device/drm/renderD129"},
		sysfsfiles: map[string][]byte{
			"card1/device/vendor":    []byte("0x8086"),
			"card1/device/device":    []byte("0xe223"),
			"card1/lmem_total_bytes": []byte("0x800000000"),
		},
		symlinkfiles: map[string]string{"card1/device/driver": "drivers/xe"},
		devfsdirs:    []string{"card1", "renderD129"},
	}

	sysfs, devfs, err := createTestFiles(root, tc)
	if err != nil {
		t.Fatal(err)
	}

	dp := newDevicePlugin(sysfs, devfs, cliOptions{sharedDevNum: 4, hami: true})
	dp.nodeName = testNode

	tree, err := dp.scan()
	if err != nil {
		t.Fatal(err)
	}

	if n := tree.DeviceTypeCount(vgpuResource); n != 4 {
		t.Errorf("%d %s devices, want 4", n, vgpuResource)
	}

	if n := tree.DeviceTypeCount(deviceTypeXe); n != 0 {
		t.Errorf("discrete card still advertised as %s (%d devices)", deviceTypeXe, n)
	}

	cards := dp.hamiCardsSnapshot()
	if len(cards) != 1 {
		t.Fatalf("%d HAMi cards, want 1", len(cards))
	}

	c := cards[0]
	if c.uuid != hamiCardUUID(testNode, testBDF) || c.bdf != testBDF || c.card != "card1" ||
		c.memMiB != 32768 || len(c.devSpecs) != 2 || !c.healthy {
		t.Errorf("unexpected card %+v", c)
	}

	dp = newDevicePlugin(sysfs, devfs, cliOptions{sharedDevNum: 4})

	tree, _ = dp.scan()
	if tree.DeviceTypeCount(vgpuResource) != 0 || tree.DeviceTypeCount(deviceTypeXe) != 4 {
		t.Errorf("without -hami the card must stay %s", deviceTypeXe)
	}
}

func TestHAMICardUUID(t *testing.T) {
	a, b := hamiCardUUID("node-a", testBDF), hamiCardUUID("node-b", testBDF)
	if a == b {
		t.Errorf("same PCI address on two nodes gives one ID %s", a)
	}

	if strings.ContainsAny(a, ":,;") {
		t.Errorf("%s contains a register separator", a)
	}

	if a != hamiCardUUID("node-a", testBDF) {
		t.Error("ID is not stable")
	}
}

func testCard() hamiCard {
	return hamiCard{
		uuid:    hamiCardUUID(testNode, testBDF),
		card:    "card1",
		bdf:     testBDF,
		model:   hamiModelType("Intel® Arc™ Pro B70 Graphics"),
		memMiB:  32768,
		numa:    1,
		healthy: true,
		devSpecs: []v1beta1.DeviceSpec{
			{HostPath: "/dev/dri/card1", ContainerPath: "/dev/dri/card1", Permissions: "rw"},
			{HostPath: "/dev/dri/renderD129", ContainerPath: "/dev/dri/renderD129", Permissions: "rw"},
		},
	}
}

func TestHAMIRegisterAnnotations(t *testing.T) {
	dp := newDevicePlugin("", "", cliOptions{sharedDevNum: 50})
	card := testCard()
	shareKey := "sharemode.gpu.bytetrade.io/" + card.uuid
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

	annos, err := dp.hamiRegisterAnnotations([]hamiCard{card}, nil, now)
	if err != nil {
		t.Fatal(err)
	}

	want := card.uuid + ",50,32768,100,Intel® Arc™ Pro B70 Graphics,1,true,0,vxpu,0:"
	if annos[hamiRegisterAnnotation] != want {
		t.Errorf("register %q, want %q", annos[hamiRegisterAnnotation], want)
	}

	if !strings.HasPrefix(annos[hamiHandshakeAnnotation], "Reported ") {
		t.Errorf("handshake %q", annos[hamiHandshakeAnnotation])
	}

	if annos[shareKey] != shareModeExclusive {
		t.Errorf("new card share mode %q, want Exclusive", annos[shareKey])
	}

	var infos []hamiCardInfo
	if err := json.Unmarshal([]byte(annos[hamiCardsAnnotation]), &infos); err != nil {
		t.Fatal(err)
	}

	if len(infos) != 1 || infos[0].Card != "card1" || infos[0].BDF != testBDF ||
		strings.Join(infos[0].SupportTypes, ",") != supportTypeExclusive {
		t.Errorf("without libvxpu: cards %+v", infos)
	}

	dp.vxpuHostLib = "/usr/local/vxpu/libvxpu.so.x"

	annos, _ = dp.hamiRegisterAnnotations([]hamiCard{card}, map[string]string{shareKey: shareModeMemSlicing}, now)
	if _, ok := annos[shareKey]; ok {
		t.Error("an existing share mode was overwritten")
	}

	_ = json.Unmarshal([]byte(annos[hamiCardsAnnotation]), &infos)
	if strings.Join(infos[0].SupportTypes, ",") != "Exclusive,MemorySlice" {
		t.Errorf("with libvxpu: support types %v", infos[0].SupportTypes)
	}
}

func TestInstallVXPU(t *testing.T) {
	src := filepath.Join(t.TempDir(), "libvxpu.so")
	host := t.TempDir()

	if err := os.WriteFile(src, []byte("lib v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := installVXPU(src, host)
	if err != nil {
		t.Fatal(err)
	}

	if again, _ := installVXPU(src, host); again != first {
		t.Errorf("reinstall moved the library: %s then %s", first, again)
	}

	_ = os.WriteFile(src, []byte("lib v2"), 0o644)

	second, _ := installVXPU(src, host)
	if second == first {
		t.Error("a new library got the old name")
	}

	if _, err := os.Stat(first); err != nil {
		t.Errorf("the old library was removed: %v", err)
	}

	preload, _ := os.ReadFile(filepath.Join(host, "ld.so.preload"))
	if string(preload) != vxpuContainerLib+"\n" {
		t.Errorf("ld.so.preload %q", preload)
	}
}

func TestNextHAMIRequestKeepsContainerPositions(t *testing.T) {
	uuid := hamiCardUUID(testNode, testBDF)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			hamiToAllocateAnnotation: ";" + uuid + ",Intel,4096,0,1:;" + uuid + ",Intel,0,0,0:;",
		}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a"}, {Name: "b"}, {Name: "c"}}},
	}

	idx, devs, rest, err := nextHAMIRequest(pod)
	if err != nil {
		t.Fatal(err)
	}

	if idx != 1 || len(devs) != 1 || devs[0].memMiB != 4096 || devs[0].shareMode != shareModeMemSlicing {
		t.Errorf("first request: container %d, %+v", idx, devs)
	}

	pod.Annotations[hamiToAllocateAnnotation] = rest

	if idx, _, _, _ = nextHAMIRequest(pod); idx != 2 {
		t.Errorf("second request for container %d, want 2", idx)
	}
}

type allocFixture struct {
	dp     *devicePlugin
	client *fake.Clientset
	pod    *corev1.Pod
}

func newAllocFixture(t *testing.T, toAllocate string, limits corev1.ResourceList) allocFixture {
	t.Helper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app", Name: "llama", UID: "uid-1",
			Annotations: map[string]string{
				hamiToAllocateAnnotation:   toAllocate,
				hamiBindPhaseAnnotation:    hamiBindAllocating,
				hamiBindTimeAnnotation:     "1",
				hamiAssignedNodeAnnotation: testNode,
			},
		},
		Spec: corev1.PodSpec{NodeName: testNode, Containers: []corev1.Container{
			{Name: "init-like"},
			{Name: "server", Resources: corev1.ResourceRequirements{Limits: limits}},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNode, Annotations: map[string]string{
		hamiNodeLockAnnotation: "2026-10-20T00:00:00Z,app,llama",
	}}}

	lib := filepath.Join(t.TempDir(), "libvxpu.so.abc")
	_ = os.WriteFile(lib, []byte("lib"), 0o644)

	dp := newDevicePlugin("", "", cliOptions{sharedDevNum: 50, vxpuHostDir: t.TempDir()})
	dp.nodeName = testNode
	dp.hamiCards = []hamiCard{testCard()}
	dp.vxpuHostLib = lib
	client := fake.NewClientset(pod, node)
	dp.kubeClient = client

	return allocFixture{dp: dp, client: client, pod: pod}
}

func (f allocFixture) allocate() (*v1beta1.AllocateResponse, error) {
	return f.dp.AllocateResource(vgpuResource, &v1beta1.AllocateRequest{
		ContainerRequests: []*v1beta1.ContainerAllocateRequest{{DevicesIds: []string{"vgpu-card1-7"}}},
	})
}

func (f allocFixture) state(t *testing.T) (*corev1.Pod, *corev1.Node) {
	t.Helper()

	pod, err := f.client.CoreV1().Pods("app").Get(context.Background(), "llama", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	node, err := f.client.CoreV1().Nodes().Get(context.Background(), testNode, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	return pod, node
}

func mountFor(cresp *v1beta1.ContainerAllocateResponse, containerPath string) *v1beta1.Mount {
	for _, m := range cresp.Mounts {
		if m.ContainerPath == containerPath {
			return m
		}
	}

	return nil
}

func TestHAMIAllocateMemorySlice(t *testing.T) {
	uuid := hamiCardUUID(testNode, testBDF)
	f := newAllocFixture(t, ";"+uuid+",Intel,4096,0,1:;", nil)

	resp, err := f.allocate()
	if err != nil {
		t.Fatal(err)
	}

	cresp := resp.ContainerResponses[0]
	if cresp.Envs["VXPU_DEVICE_PCI_0"] != testBDF || cresp.Envs["CUDA_DEVICE_MEMORY_LIMIT_0"] != "4096m" ||
		cresp.Envs["CUDA_DEVICE_MEMORY_SHARED_CACHE"] != vxpuCacheFile {
		t.Errorf("envs %v", cresp.Envs)
	}

	if len(cresp.Devices) != 2 || cresp.Devices[0].HostPath != "/dev/dri/card1" {
		t.Errorf("devices %v", cresp.Devices)
	}

	if m := mountFor(cresp, vxpuContainerLib); m == nil || m.HostPath != f.dp.vxpuHostLib {
		t.Errorf("libvxpu mount %v", m)
	}

	if m := mountFor(cresp, "/etc/ld.so.preload"); m == nil {
		t.Error("ld.so.preload not mounted")
	}

	cache := mountFor(cresp, vxpuContainerCache)
	if cache == nil || cache.HostPath != filepath.Join(f.dp.options.vxpuHostDir, "containers", "uid-1_server") {
		t.Fatalf("cache mount %v", cache)
	}

	if _, err := os.Stat(cache.HostPath); err != nil {
		t.Errorf("cache directory: %v", err)
	}

	pod, node := f.state(t)
	if hasPendingDevices(pod.Annotations[hamiToAllocateAnnotation]) {
		t.Errorf("request not erased: %q", pod.Annotations[hamiToAllocateAnnotation])
	}

	if pod.Annotations[hamiBindPhaseAnnotation] != hamiBindSuccess {
		t.Errorf("bind phase %q", pod.Annotations[hamiBindPhaseAnnotation])
	}

	if _, ok := node.Annotations[hamiNodeLockAnnotation]; ok {
		t.Error("node lock not released")
	}
}

func TestHAMIAllocateExclusiveSetsNoLimit(t *testing.T) {
	uuid := hamiCardUUID(testNode, testBDF)
	f := newAllocFixture(t, ";"+uuid+",Intel,32768,100,0:;", nil)

	resp, err := f.allocate()
	if err != nil {
		t.Fatal(err)
	}

	envs := resp.ContainerResponses[0].Envs
	if envs["VXPU_DEVICE_PCI_0"] != testBDF {
		t.Errorf("card not pinned: %v", envs)
	}

	if _, ok := envs["CUDA_DEVICE_MEMORY_LIMIT_0"]; ok {
		t.Errorf("Exclusive got a memory limit: %v", envs)
	}
}

func TestHAMIAllocateFailureReleasesLock(t *testing.T) {
	for name, fx := range map[string]func(*testing.T) allocFixture{
		"card not on node": func(t *testing.T) allocFixture {
			return newAllocFixture(t, ";intel-00000000-0000-09-00.0,Intel,4096,0,1:;", nil)
		},
		"also requests NVIDIA": func(t *testing.T) allocFixture {
			return newAllocFixture(t, ";"+hamiCardUUID(testNode, testBDF)+",Intel,4096,0,1:;",
				corev1.ResourceList{nvidiaGPUResource: resource.MustParse("1")})
		},
		"quota without libvxpu": func(t *testing.T) allocFixture {
			f := newAllocFixture(t, ";"+hamiCardUUID(testNode, testBDF)+",Intel,4096,0,1:;", nil)
			f.dp.vxpuHostLib = ""

			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := fx(t)
			if _, err := f.allocate(); err == nil {
				t.Fatal("allocation succeeded")
			}

			pod, node := f.state(t)
			if pod.Annotations[hamiBindPhaseAnnotation] != hamiBindFailed {
				t.Errorf("bind phase %q", pod.Annotations[hamiBindPhaseAnnotation])
			}

			if _, ok := node.Annotations[hamiNodeLockAnnotation]; ok {
				t.Error("node lock not released")
			}
		})
	}
}

func TestHAMIAllocateLeavesLockWhileNVIDIAPending(t *testing.T) {
	uuid := hamiCardUUID(testNode, testBDF)
	f := newAllocFixture(t, ";"+uuid+",Intel,4096,0,1:;", nil)
	_, _ = f.client.CoreV1().Pods("app").Patch(context.Background(), "llama", "application/merge-patch+json",
		[]byte(`{"metadata":{"annotations":{"hami.io/vgpu-devices-to-allocate":"GPU-1,NVIDIA,1000,0,1:;;"}}}`), metav1.PatchOptions{})

	if _, err := f.allocate(); err != nil {
		t.Fatal(err)
	}

	pod, node := f.state(t)
	if pod.Annotations[hamiBindPhaseAnnotation] != hamiBindAllocating {
		t.Errorf("bind phase %q while NVIDIA cards are pending", pod.Annotations[hamiBindPhaseAnnotation])
	}

	if _, ok := node.Annotations[hamiNodeLockAnnotation]; !ok {
		t.Error("node lock released while NVIDIA cards are pending")
	}
}

func TestHAMIAllocateOtherResourcesUseDefault(t *testing.T) {
	dp := newDevicePlugin("", "", cliOptions{sharedDevNum: 1})
	if _, err := dp.AllocateResource(deviceTypeXe, &v1beta1.AllocateRequest{}); err == nil {
		t.Error("xe allocation did not fall back to the default")
	}
}
