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
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"os/exec"
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
		klog.Fatal("[WatchPods] ", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatal("[WatchPods] ", err)
	}

	// 监听default的 Pod 事件
	// TODO: 后续混部任务可能有个专门的namespace: "colocation-memory"
	watcher, err := clientset.CoreV1().Pods("default").Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		klog.Fatal("[WatchPods] ", err)
	}
	defer watcher.Stop()
	for event := range watcher.ResultChan() {
		pod, ok := event.Object.(*v1.Pod)
		if !ok {
			klog.Fatal("[WatchPods] unexpected type")
			continue
		}

		switch event.Type {
		// TODO: 这里会先监听历史的Pod创建事件，再持续监听新的Pod创建事件，后面在Pod换出池化内存时注意下，可能Pod维护的环境变量里面有CM-xxxx，但实际在mm.ColocMemoryMap中已经被删除
		case watch.Added:
			klog.Infof("[WatchPods] Pod created: %s/%s\n", pod.Namespace, pod.Name)

			// 等待 Pod 进入 Running 状态
			go m.waitForPodAndFetchDevIds(clientset, common.KubeConfigPath, pod.Namespace, pod.Name)

		case watch.Deleted:
			klog.Infof("[WatchPods] Pod deleted: %s/%s\n", pod.Namespace, pod.Name)

			// 更新设备的元数据
			for _, devId := range m.Pod2ColocIds[pod.Name] {
				m.Uuid2ColocMetaData[devId].BindPod = ""
				m.Uuid2ColocMetaData[devId].Used = false
				m.Uuid2ColocMetaData[devId].UpdateTime = time.Now()
			}
			// 删除 Pod 和设备 ID 的映射关系
			delete(m.Pod2ColocIds, pod.Name)
			klog.Info("[WatchPods] Pod2ColocIds: ", m.Pod2ColocIds)
		}
	}
}

// 等待 Pod 进入 Running 状态，然后执行 `env` 命令获取运行时环境变量
func (m *MemoryManager) waitForPodAndFetchDevIds(clientset *kubernetes.Clientset, config string, namespace, podName string) {
	// 等待 Pod 进入 Running 状态
	// TODO: 有空把弃用的函数改掉
	err := wait.PollImmediate(2*time.Second, 60*time.Second, func() (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return pod.Status.Phase == v1.PodRunning, nil
	})

	if err != nil {
		klog.Fatalf("[waitForPodAndFetchEnv] Error waiting for Pod %s/%s to be Running: %v\n", namespace, podName, err)
		return
	}

	// 执行 `kubectl exec` 获取环境变量
	envVars, err := m.getPodEnvVars(config, namespace, podName)
	if err != nil {
		klog.Fatalf("[waitForPodAndFetchEnv] Failed to get environment variables from Pod %s/%s: %v\n", namespace, podName, err)
		return
	}

	if _, ok := envVars[common.ResourceName]; !ok {
		klog.Errorf("[waitForPodAndFetchEnv] Pod %s/%s does not have environment variable %s\n", namespace, podName, common.ResourceName)
		return
	}

	// 解析环境变量，获取设备 ID
	devIds := strings.Split(envVars[common.ResourceName], ",")
	klog.Infof("[waitForPodAndFetchEnv] Pod %s/%s has environment variable %s: %v\n", namespace, podName, common.ResourceName, devIds)

	// devIds添加到mm.ColocMemoryMap中
	for _, devId := range devIds {
		if _, ok := m.Uuid2ColocMetaData[devId]; !ok {
			klog.Infof("[waitForPodAndFetchEnv] Device %s not found in ColocMemoryMap\n", devId)
			continue
		}
		// 更新设备的元数据
		m.Uuid2ColocMetaData[devId].BindPod = podName
		m.Uuid2ColocMetaData[devId].Used = true
		m.Uuid2ColocMetaData[devId].UpdateTime = time.Now()
		// 记录 Pod 和设备 ID 的映射关系
		m.Pod2ColocIds[podName] = append(m.Pod2ColocIds[podName], devId)
		klog.Infof("[waitForPodAndFetchEnv] Device %s bound to Pod %s/%s\n", devId, namespace, podName)
	}

	klog.Info("[waitForPodAndFetchEnv] Pod2ColocIds: ", m.Pod2ColocIds)

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
