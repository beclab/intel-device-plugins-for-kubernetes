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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	dpapi "github.com/intel/intel-device-plugins-for-kubernetes/pkg/deviceplugin"
)

// The HAMi scheduler's side of an allocation (pkg/util in HAMi). The node lock
// is shared by every vendor: HAMi finds the pod being allocated per node, not
// per vendor.
const (
	hamiToAllocateAnnotation   = "hami.io/intel-vgpu-devices-to-allocate"
	nvidiaToAllocateAnnotation = "hami.io/vgpu-devices-to-allocate"
	hamiNodeLockAnnotation     = "hami.io/mutex.lock"
	hamiBindPhaseAnnotation    = "hami.io/bind-phase"
	hamiBindTimeAnnotation     = "hami.io/bind-time"
	hamiAssignedNodeAnnotation = "hami.io/vgpu-node"

	hamiBindAllocating = "allocating"
	hamiBindSuccess    = "success"
	hamiBindFailed     = "failed"

	nvidiaGPUResource   = "nvidia.com/gpu"
	hamiAllocateTimeout = 30 * time.Second
)

// hamiDevice is one card HAMi assigned to a container:
// "uuid,type,usedmem,usedcores,sharemode".
type hamiDevice struct {
	uuid      string
	memMiB    int64
	shareMode string
}

// splitHAMIContainers splits a to-allocate annotation into one entry per pod
// container, in pod.Spec.Containers order. A container without devices is an
// empty entry, and the encoder's trailing ';' adds one more.
func splitHAMIContainers(value string) []string {
	return strings.Split(value, ";")
}

func decodeHAMIDevices(entry string) ([]hamiDevice, error) {
	var devs []hamiDevice

	for _, d := range strings.Split(entry, ":") {
		if d == "" {
			continue
		}

		f := strings.Split(d, ",")
		if len(f) < 5 {
			return nil, errors.Errorf("malformed device %q", d)
		}

		mem, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "device %q", d)
		}

		devs = append(devs, hamiDevice{uuid: f[0], memMiB: mem, shareMode: f[4]})
	}

	return devs, nil
}

// nextHAMIRequest returns the first container of pod that still has cards to
// allocate, and the annotation value with that container's entry cleared.
func nextHAMIRequest(pod *corev1.Pod) (int, []hamiDevice, string, error) {
	entries := splitHAMIContainers(pod.Annotations[hamiToAllocateAnnotation])

	for i, entry := range entries {
		devs, err := decodeHAMIDevices(entry)
		if err != nil {
			return 0, nil, "", err
		}

		if len(devs) == 0 {
			continue
		}

		if i >= len(pod.Spec.Containers) {
			return 0, nil, "", errors.Errorf("device request for container %d, pod has %d", i, len(pod.Spec.Containers))
		}

		entries[i] = ""

		return i, devs, strings.Join(entries, ";"), nil
	}

	return 0, nil, "", errors.Errorf("no Intel device request left on pod %s/%s", pod.Namespace, pod.Name)
}

func hasPendingDevices(value string) bool {
	for _, entry := range splitHAMIContainers(value) {
		if strings.Contains(entry, ",") {
			return true
		}
	}

	return false
}

// AllocateResource replaces the default allocation for vgpuResource. The
// device IDs kubelet picked are placeholders: kubelet does not say which pod
// the request is for, and HAMi chose the cards, so both come from the pod
// HAMi is binding on this node.
func (dp *devicePlugin) AllocateResource(devType string, rqt *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	if devType != vgpuResource {
		return dp.Allocate(rqt)
	}

	if dp.kubeClient == nil {
		return nil, errors.New("hami: no Kubernetes client, cannot find the pod being allocated")
	}

	ctx, cancel := context.WithTimeout(context.Background(), hamiAllocateTimeout)
	defer cancel()

	pod, err := dp.pendingPod(ctx)
	if err != nil {
		return nil, err
	}

	response := &pluginapi.AllocateResponse{}

	for range rqt.ContainerRequests {
		cresp, err := dp.allocateNextContainer(ctx, pod)
		if err != nil {
			klog.Errorf("hami: allocation for pod %s/%s failed: %v", pod.Namespace, pod.Name, err)
			dp.finishAllocation(ctx, pod, hamiBindFailed)

			return nil, err
		}

		response.ContainerResponses = append(response.ContainerResponses, cresp)
	}

	dp.allocationTrySuccess(ctx, pod)

	return response, nil
}

func (dp *devicePlugin) allocateNextContainer(ctx context.Context, pod *corev1.Pod) (*pluginapi.ContainerAllocateResponse, error) {
	idx, devs, rest, err := nextHAMIRequest(pod)
	if err != nil {
		return nil, err
	}

	ctr := &pod.Spec.Containers[idx]

	// libvxpu and libvgpu both hook dlsym; only one can be preloaded.
	if _, ok := ctr.Resources.Limits[nvidiaGPUResource]; ok {
		return nil, errors.Errorf("container %s requests both %s and %s", ctr.Name, vgpuResource, nvidiaGPUResource)
	}

	cresp, err := dp.vgpuContainerResponse(pod, ctr, devs)
	if err != nil {
		return nil, err
	}

	if err := dp.patchPodAnnotations(ctx, pod, map[string]string{hamiToAllocateAnnotation: rest}); err != nil {
		return nil, err
	}

	pod.Annotations[hamiToAllocateAnnotation] = rest

	return cresp, nil
}

func (dp *devicePlugin) vgpuContainerResponse(pod *corev1.Pod, ctr *corev1.Container, devs []hamiDevice) (*pluginapi.ContainerAllocateResponse, error) {
	cards := map[string]hamiCard{}
	for _, c := range dp.hamiCardsSnapshot() {
		cards[c.uuid] = c
	}

	cresp := &pluginapi.ContainerAllocateResponse{Envs: map[string]string{}}
	limited := false

	for i, d := range devs {
		card, ok := cards[d.uuid]
		if !ok {
			return nil, errors.Errorf("card %s assigned by HAMi is not on this node", d.uuid)
		}

		for j := range card.devSpecs {
			cresp.Devices = append(cresp.Devices, &card.devSpecs[j])
		}

		for j := range card.mounts {
			cresp.Mounts = append(cresp.Mounts, &card.mounts[j])
		}

		cresp.Envs[fmt.Sprintf("VXPU_DEVICE_PCI_%d", i)] = card.bdf

		if d.shareMode == shareModeMemSlicing {
			cresp.Envs[fmt.Sprintf("CUDA_DEVICE_MEMORY_LIMIT_%d", i)] = fmt.Sprintf("%dm", d.memMiB)
			limited = true
		}
	}

	dp.allocMutex.Lock()
	for i := range dp.npuDeviceSpecs {
		cresp.Devices = append(cresp.Devices, &dp.npuDeviceSpecs[i])
	}
	dp.allocMutex.Unlock()

	if dp.vxpuHostLib == "" {
		if limited {
			return nil, errors.New("libvxpu is not installed on this node, so a memory quota cannot be enforced")
		}

		klog.Warningf("hami: libvxpu is not installed, %s/%s container %s can see every card", pod.Namespace, pod.Name, ctr.Name)

		return cresp, nil
	}

	if err := dp.injectVXPU(cresp, pod, ctr); err != nil {
		return nil, err
	}

	return cresp, nil
}

func (dp *devicePlugin) injectVXPU(cresp *pluginapi.ContainerAllocateResponse, pod *corev1.Pod, ctr *corev1.Container) error {
	// A fresh directory per container instance: libvxpu's ledger lives there,
	// and HAMi's monitor reads it per container.
	cacheDir := filepath.Join(dp.options.vxpuHostDir, "containers", fmt.Sprintf("%s_%s", pod.UID, ctr.Name))
	if err := os.RemoveAll(cacheDir); err != nil {
		return err
	}

	if err := os.MkdirAll(cacheDir, 0o777); err != nil {
		return err
	}

	if err := os.Chmod(cacheDir, 0o777); err != nil {
		return err
	}

	cresp.Envs["CUDA_DEVICE_MEMORY_SHARED_CACHE"] = vxpuCacheFile
	if dp.options.vxpuLogLevel != "" {
		cresp.Envs["LIBCUDA_LOG_LEVEL"] = dp.options.vxpuLogLevel
	}

	cresp.Mounts = append(cresp.Mounts,
		&pluginapi.Mount{ContainerPath: vxpuContainerLib, HostPath: dp.vxpuHostLib, ReadOnly: true},
		&pluginapi.Mount{ContainerPath: vxpuContainerCache, HostPath: cacheDir},
	)

	if !controlDisabled(ctr) {
		cresp.Mounts = append(cresp.Mounts, &pluginapi.Mount{
			ContainerPath: "/etc/ld.so.preload",
			HostPath:      filepath.Join(dp.options.vxpuHostDir, "ld.so.preload"),
			ReadOnly:      true,
		})
	}

	return nil
}

// controlDisabled mirrors HAMi's CUDA_DISABLE_CONTROL=true opt-out: libvxpu
// is mounted but not preloaded, so the container sets LD_PRELOAD itself.
func controlDisabled(ctr *corev1.Container) bool {
	for _, env := range ctr.Env {
		if env.Name == "CUDA_DISABLE_CONTROL" {
			disabled, _ := strconv.ParseBool(env.Value)
			return disabled
		}
	}

	return false
}

// pendingPod finds the pod HAMi is binding on this node: the holder of the
// node lock, or else a pending pod HAMi assigned here that is still being
// allocated.
func (dp *devicePlugin) pendingPod(ctx context.Context) (*corev1.Pod, error) {
	node, err := dp.kubeClient.CoreV1().Nodes().Get(ctx, dp.nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	if lock := strings.Split(node.Annotations[hamiNodeLockAnnotation], ","); len(lock) == 3 && lock[1] != "" && lock[2] != "" {
		return dp.kubeClient.CoreV1().Pods(lock[1]).Get(ctx, lock[2], metav1.GetOptions{})
	}

	pods, err := dp.kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + dp.nodeName})
	if err != nil {
		return nil, err
	}

	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodPending {
			continue
		}

		if _, ok := p.Annotations[hamiBindTimeAnnotation]; !ok {
			continue
		}

		if p.Annotations[hamiBindPhaseAnnotation] == hamiBindAllocating && p.Annotations[hamiAssignedNodeAnnotation] == dp.nodeName {
			return p, nil
		}
	}

	return nil, errors.Errorf("no pod is being allocated on node %s", dp.nodeName)
}

// allocationTrySuccess finishes the allocation once neither Intel nor NVIDIA
// cards are left to allocate on the pod; otherwise the other plugin finishes.
func (dp *devicePlugin) allocationTrySuccess(ctx context.Context, pod *corev1.Pod) {
	fresh, err := dp.kubeClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("hami: can't re-read pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return
	}

	if hasPendingDevices(fresh.Annotations[hamiToAllocateAnnotation]) ||
		hasPendingDevices(fresh.Annotations[nvidiaToAllocateAnnotation]) {
		return
	}

	dp.finishAllocation(ctx, pod, hamiBindSuccess)
}

func (dp *devicePlugin) finishAllocation(ctx context.Context, pod *corev1.Pod, phase string) {
	if err := dp.patchPodAnnotations(ctx, pod, map[string]string{hamiBindPhaseAnnotation: phase}); err != nil {
		klog.Errorf("hami: can't set %s=%s on pod %s/%s: %v", hamiBindPhaseAnnotation, phase, pod.Namespace, pod.Name, err)
	}

	if err := dp.releaseNodeLock(ctx, pod); err != nil {
		klog.Errorf("hami: can't release the lock on node %s: %v", dp.nodeName, err)
	}
}

// releaseNodeLock retries because the patch is conditional on the node's
// resourceVersion: the lock must not be dropped if another pod took it since.
func (dp *devicePlugin) releaseNodeLock(ctx context.Context, pod *corev1.Pod) error {
	var err error

	for range 5 {
		if err = dp.releaseNodeLockOnce(ctx, pod); err == nil {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	return err
}

func (dp *devicePlugin) releaseNodeLockOnce(ctx context.Context, pod *corev1.Pod) error {
	node, err := dp.kubeClient.CoreV1().Nodes().Get(ctx, dp.nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	lock, ok := node.Annotations[hamiNodeLockAnnotation]
	if !ok || !strings.Contains(lock, pod.Name) {
		return nil
	}

	data, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations":     map[string]any{hamiNodeLockAnnotation: nil},
		"resourceVersion": node.ResourceVersion,
	}})
	if err != nil {
		return err
	}

	_, err = dp.kubeClient.CoreV1().Nodes().Patch(ctx, dp.nodeName, types.MergePatchType, data, metav1.PatchOptions{})

	return err
}

func (dp *devicePlugin) patchPodAnnotations(ctx context.Context, pod *corev1.Pod, annos map[string]string) error {
	data, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annos}})
	if err != nil {
		return err
	}

	_, err = dp.kubeClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, data, metav1.PatchOptions{})

	return err
}

var _ dpapi.ResourceAllocator = (*devicePlugin)(nil)
