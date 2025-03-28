package memory_manager

/**
author:liuyang
date:2025-3-28
NUMA节点内存资源解析
*/

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type NumaMemInfo struct {
	Total uint64 // 总内存（字节）
	Free  uint64 // 空闲内存（字节）
	Used  uint64 // 已用内存（字节）
}

// 获取指定NUMA节点的内存信息
func GetNumaMemInfo(nodeID int) (NumaMemInfo, error) {
	path := fmt.Sprintf("/sys/devices/system/node/node%d/meminfo", nodeID)
	data, err := os.ReadFile(path)
	if err != nil {
		return NumaMemInfo{}, fmt.Errorf("读取文件失败: %v", err)
	}

	var info NumaMemInfo
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// 解析字段（单位：kB）
		key := fields[2]
		value, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			continue
		}

		// 转换为字节
		valueBytes := value * 1024

		switch key {
		case "MemTotal:":
			info.Total = valueBytes
		case "MemFree:":
			info.Free = valueBytes
		case "MemUsed:":
			info.Used = valueBytes
		}
	}

	// 如果未直接提供Used，则计算
	if info.Used == 0 && info.Total > 0 {
		info.Used = info.Total - info.Free
	}

	return info, nil
}
