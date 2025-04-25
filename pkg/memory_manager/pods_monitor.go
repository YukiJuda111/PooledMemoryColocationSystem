package memory_manager

/**
author:liuyang
date:2025-3-29
通过client-go监听k8s的pod创建和删除事件,来绑定colocationMemoryBlock和pod,确保一致性
*/

// 监听pods的创建和删除，绑定pods和colocationMemoryBlock
// 创建时: mm.ColocMemoryMap[blockId].BindPod = podId, mm.ColocMemoryMap[blockId].Used = true
// 删除时: mm.ColocMemoryMap[blockId].BindPod = "", mm.ColocMemoryMap[blockId].Used = false

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// TODO: 后续放在Pod: 确保Pod的ServiceAccount具有相应的RBAC权限，能够访问Kubernetes API
func (m *MemoryManager) WatchPods() {
	klog.Info("[WatchPods] 监听Pod事件...")
	// 加载 kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", common.KubeConfigPath) // 修改路径
	if err != nil {
		klog.Error("[WatchPods] ", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Error("[WatchPods] ", err)
	}

	// 监听混部Pod事件
	// 混部任务有个专门的namespace: "colocation-memory"
	watcher, err := clientset.CoreV1().Pods("colocation-memory").Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		klog.Error("[WatchPods] ", err)
	}
	defer watcher.Stop()
	for event := range watcher.ResultChan() {
		pod, ok := event.Object.(*v1.Pod)
		if !ok {
			klog.Error("[WatchPods] unexpected type")
			continue
		}

		switch event.Type {
		// TODO: 这里会先监听历史的Pod创建事件，再持续监听新的Pod创建事件，后面在Pod换出池化内存时注意下，可能Pod维护的环境变量里面有CM-xxxx，但实际在mm.Uuid2ColocMetaData中已经被删除 (换出的时候别忘了维护就行）
		case watch.Added:
			m.handlePodAdded(clientset, pod.Namespace, pod.Name)
		case watch.Deleted:
			m.handlePodDeleted(pod.Name)
		}
	}
}

// 等待 Pod 进入 Running 状态，然后执行 `env` 命令获取运行时环境变量
func (m *MemoryManager) waitForPodAndFetchDevIds(clientset *kubernetes.Clientset, config string, namespace, podName string) {
	// 等待 Pod 进入 Running 状态
	// TODO: 有空把弃用的函数改掉
	// 这里有个时序问题，如果在等待Pod进入running的时候还没更新ColocMetaData，device monitor的判断就会出问题
	m.IsReady = false
	defer func() {
		m.IsReady = true
	}()
	err := wait.PollImmediate(2*time.Second, 60*time.Second, func() (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return pod.Status.Phase == v1.PodRunning, nil
	})

	if err != nil {
		klog.Errorf("[waitForPodAndFetchEnv] Error waiting for Pod %s/%s to be Running: %v\n", namespace, podName, err)
		return
	}

	// 执行 `kubectl exec` 获取环境变量
	envVars, err := m.getPodEnvVars(config, namespace, podName)
	if err != nil {
		klog.Errorf("[waitForPodAndFetchEnv] Failed to get environment variables from Pod %s/%s: %v\n", namespace, podName, err)
		return
	}
	numOfDevices := m.processPodEnvVars(envVars, podName)
	pid := m.inspectPodCgroup(podName)

	klog.Info("[waitForPodAndFetchEnv] Pod2PodInfo update: ", m.Pod2PodInfo[podName])

	m.setCgroupsMemoryLimit(pid, common.BlockSize*numOfDevices)
}

func (m *MemoryManager) setCgroupsMemoryLimit(pid int, limit int) {
	cgroupFile := fmt.Sprintf("/proc/%s/cgroup", fmt.Sprintf("%d", pid))
	data, err := os.ReadFile(cgroupFile)
	if err != nil {
		klog.Error("[setCgroupsMemoryLimit] ", err)
		return
	}
	klog.Info("[setCgroupsMemoryLimit] Read cgroup file: ", string(data))

	var cgroupPath string
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		if strings.HasPrefix(line, "0::") {
			parts := strings.Split(line, ":")
			if len(parts) == 3 {
				cgroupPath = parts[2]
			}
			break
		}
	}

	if cgroupPath == "" {
		klog.Error("[setCgroupsMemoryLimit] Failed to find cgroup path for PID ", pid)
		return
	}

	// 为了防止OOM容器重启后memory.max被重置，这里设置的是父目录的memory.max
	// 例如cgroup是/sys/fs/cgroup/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podc02daf39_1e4d_448a_a551_1a6080a293ac.slice/cri-containerd-f83b2fb4148c0269b10e181fcae93693c2a1259fa37b0fe2a88937e0f26e9470.scope
	// 那么应该设置kubepods-besteffort-podc02daf39_1e4d_448a_a551_1a6080a293ac.slice下的memory.max

	fullPath := filepath.Join("/sys/fs/cgroup", cgroupPath)
	parentDir := filepath.Dir(fullPath)
	err = os.WriteFile(filepath.Join(parentDir, "memory.max"), fmt.Appendf(nil, "%d", limit), 0644)
	if err != nil {
		klog.Error("[setCgroupsMemoryLimit] ", err)
		return
	}

	klog.Infof("[setCgroupsMemoryLimit] Set memory limit for PID %d to %d bytes", pid, limit)
}

// 执行 `kubectl exec` 命令，获取 Pod 内的环境变量
// 获取 Pod 内的环境变量，指定 kubeconfig
func (m *MemoryManager) getPodEnvVars(kubeconfig, namespace, podName string) (map[string]string, error) {
	// 构造 kubectl 命令
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "exec", podName, "-n", namespace, "--", "env")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 执行命令
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %v, stderr: %s", err, stderr.String())
	}

	// 解析环境变量
	envVars := make(map[string]string)
	lines := strings.SplitSeq(stdout.String(), "\n")
	for line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			envVars[parts[0]] = parts[1]
		}
	}

	return envVars, nil
}

// 处理 Pod 的环境变量
func (m *MemoryManager) processPodEnvVars(envVars map[string]string, podName string) int {
	cnt := 0
	if resource, ok := envVars[common.ResourceName]; ok {
		podInfo := &PodInfo{
			Name:         podName,
			BindColocIds: []string{},
			Pid:          -1,
			SwapColocIds: []string{},
		}

		devIds := strings.SplitSeq(resource, ",")
		for devId := range devIds {
			m.updateDeviceMetadata(devId, podName, true)
			podInfo.BindColocIds = append(podInfo.BindColocIds, devId)
			cnt++
		}

		m.Pod2PodInfo[podName] = podInfo
	} else {
		klog.Errorf("[processPodEnvVars] Pod %s does not have environment variable %s", podName, common.ResourceName)
	}
	return cnt
}

func (m *MemoryManager) inspectPodCgroup(podName string) int {
	containers, err := m.listRunningContainers()
	if err != nil {
		klog.Errorf("[inspectPodCgroup] %v", err)
		return -1
	}

	containerID, err := m.findContainerForPod(containers, podName)
	if err != nil {
		klog.Errorf("[inspectPodCgroup] %v", err)
		return -1
	}

	pid, err := m.getContainerPid(containerID)
	if err != nil {
		klog.Errorf("[inspectPodCgroup] %v", err)
		return -1
	}

	m.Pod2PodInfo[podName].Pid = pid
	return pid
}

func (m *MemoryManager) listRunningContainers() ([]map[string]any, error) {
	cmd := exec.Command("crictl", "ps", "-o", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to execute crictl ps: %w", err)
	}

	var result struct {
		Containers []map[string]any `json:"containers"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse crictl ps output: %w", err)
	}

	return result.Containers, nil
}

func (m *MemoryManager) findContainerForPod(containers []map[string]any, podName string) (string, error) {
	for _, c := range containers {
		state := c["state"].(string)
		labels := c["labels"].(map[string]any)
		if labels["io.kubernetes.pod.name"] == podName && state == "CONTAINER_RUNNING" {
			return c["id"].(string), nil
		}
	}
	return "", fmt.Errorf("no running container found for pod %s", podName)
}

func (m *MemoryManager) getContainerPid(containerID string) (int, error) {
	cmd := exec.Command("crictl", "inspect", containerID)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	var inspectResult struct {
		Info struct {
			Pid int `json:"pid"`
		} `json:"info"`
	}

	if err := json.Unmarshal(output, &inspectResult); err != nil {
		return 0, fmt.Errorf("failed to parse inspect output: %w", err)
	}

	return inspectResult.Info.Pid, nil
}

// 更新设备元数据
func (m *MemoryManager) updateDeviceMetadata(devId, podName string, used bool) {
	if meta, ok := m.Uuid2ColocMetaData[devId]; ok {
		meta.BindPod = podName
		meta.Used = used
		meta.UpdateTime = time.Now()
	}
}

// 删除 Pod 和设备 ID 的映射关系
func (m *MemoryManager) removePodDeviceMapping(podName string) {
	for _, devId := range m.Pod2PodInfo[podName].BindColocIds {
		m.updateDeviceMetadata(devId, "", false)
	}
	delete(m.Pod2PodInfo, podName)
}

// 处理 Pod 创建事件
func (m *MemoryManager) handlePodAdded(clientset *kubernetes.Clientset, namespace, podName string) {
	klog.Infof("[handlePodAdded] Pod created: %s/%s", namespace, podName)
	go m.waitForPodAndFetchDevIds(clientset, common.KubeConfigPath, namespace, podName)
}

// 处理 Pod 删除事件
func (m *MemoryManager) handlePodDeleted(podName string) {
	klog.Infof("[handlePodDeleted] Pod deleted: %s", podName)
	m.removePodDeviceMapping(podName)
}
