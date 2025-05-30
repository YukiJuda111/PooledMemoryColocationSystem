package memory_manager

/**
author:liuyang
date:2025-3-28
cgroups内存资源解析
*/

import (
	"os"
	"strconv"
	"strings"
)

// 由于混部任务没有申请原生资源类型，k8s会自动将Pod归类为best effort
// 混部任务内存使用情况：/sys/fs/cgroup/kubepods.slice/kubepods-besteffort.slice/memory.current
// 其他任务内存使用情况：/sys/fs/cgroup/kubepods.slice/kubepods-burstable.slice/memory.current
// （这里没有保证guaranteed）
// 这里可以参考混部大框：https://www.bilibili.com/opus/698938934644703479

func GetCgroupsMemoryInfo(cgroupPath string) (uint64, error) {
	// 读取cgroupPath下的memory.current文件
	// 读取文件内容并转换为uint64
	data, err := os.ReadFile(cgroupPath)
	if err != nil {
		return 0, err
	}
	usage, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return usage, nil
}
