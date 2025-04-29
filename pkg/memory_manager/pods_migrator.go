package memory_manager

import (
	"fmt"
	"os/exec"

	"k8s.io/klog/v2"
)

// migrate pid fromnode tonode
func (m *MemoryManager) MigratePod(podName string, srcNode string, dstNode string) error {
	podInfo, ok := m.Pod2PodInfo[podName]
	if !ok {
		klog.Errorf("[MigratePod] Pod %s 不存在", podName)
		return fmt.Errorf("pod %s not found", podName)
	}

	pid := podInfo.Pid

	cmd := exec.Command("migratepages", fmt.Sprintf("%d", pid), srcNode, dstNode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("[MigratePod] 迁移 pod %s (pid: %d) 失败: %v\nOutput: %s", podName, pid, err, string(output))
		return fmt.Errorf("failed to migrate pages for pod %s (pid %d): %v\nOutput: %s",
			podName, pid, err, string(output))
	}

	klog.Infof("[MigratePod] 成功迁移 pod %s (pid: %d) 从节点 %s 到节点 %s", podName, pid, srcNode, dstNode)
	return nil
}
