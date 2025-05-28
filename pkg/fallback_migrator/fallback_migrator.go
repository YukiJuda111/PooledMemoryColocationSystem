package fallback_migrator

import (
	"context"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func MigratePodToAnotherNode(yamlPath string) {

	// 加载 kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", common.KubeConfigPath)
	if err != nil {
		klog.Error("[MigratePodToAnotherNode] ", err)
		return
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Error("[MigratePodToAnotherNode] ", err)
		return
	}

	// 读取 YAML 文件
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		klog.Error("[MigratePodToAnotherNode] ", err)
		return
	}

	// 解码 YAML 到 Pod 对象
	pod := &corev1.Pod{}
	if err := yaml.Unmarshal(data, pod); err != nil {
		klog.Error("[MigratePodToAnotherNode] ", err)
		return
	}

	namespace := pod.Namespace
	if namespace == "" {
		namespace = "default"
	}

	// 获取现有的同名 Pod
	existingPod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err == nil {
		// 如果 Pod 存在并且在 cxl-server 节点上，删除它
		if existingPod.Spec.NodeName == "cxl-server" {
			klog.Info("[MigratePodToAnotherNode] Existing Pod found on cxl-server, deleting it...")

			err = clientset.CoreV1().Pods(namespace).Delete(context.Background(), existingPod.Name, metav1.DeleteOptions{})
			if err != nil {
				klog.Error("[MigratePodToAnotherNode] ", err)
				return
			}

		} else {
			klog.Info("[MigratePodToAnotherNode] Existing Pod found, not deleting...")
			return
		}
	} else {
		klog.Info("[MigratePodToAnotherNode] No existing Pod found, creating a new one...")
	}

	// 设置反亲和性，禁止调度到 cxl-server
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpNotIn,
								Values:   []string{"cxl-server"},
							},
						},
					},
				},
			},
		},
	}

	// 清除 metadata.uid/resourceVersion 否则无法重新创建
	pod.ResourceVersion = ""
	pod.UID = ""

	// 创建 Pod
	podClient := clientset.CoreV1().Pods(namespace)
	createdPod, err := podClient.Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		klog.Error("[MigratePodToAnotherNode] ", err)
		return
	}

	klog.Infof("[MigratePodToAnotherNode] Pod %s created successfully in namespace %s\n", createdPod.Name, namespace)
}
