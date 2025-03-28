package memory_manager

/**
author:liuyang
date:2025-3-19
内存监视
*/

// 增量式的内存管理
// 1. 初始化读取系统内存(numa0,numa1)、k8s在线任务内存、安全水位，混部内存 = 系统内存 - k8s在线任务内存 - 安全水位
// 2. 混部内存虚拟化分块： 混部资源 = 混部内存 / 512M，混部资源注册为colocationMemory0,colocationMemory1...
// 3. 维护混部资源为队列，动态监测每当有混部资源增加/减少时，从队尾开始相应增减colocationMemory

import (
	"fmt"
	"path"
	"time"

	"k8s.io/klog/v2"
)

const (
	BlockSize       = 512 * 1024 * 1024 // 512MB
	RefreshInterval = 5 * time.Second
	SafetyWatermark = 0.1 // 10%安全水位
	K8sPodsBasePath = "/sys/fs/cgroup/kubepods.slice"
	BurstablePath   = "/kubepods-burstable.slice/memory.current"
	BestEffortPath  = "/kubepods-besteffort.slice/memory.current"
)

type MemoryManager struct {
	TotalMemory     uint64   // 系统总内存 (NUMA0 + NUMA1)
	OnlinePodsUsed  uint64   // 在线任务内存使用量
	SafetyMargin    uint64   // 安全水位
	ColocMemory     uint64   // 可用混部内存
	ColocMemoryList []string // 混部内存虚拟块队列 = 可用混部内存 / BlockSize
	prevBlocks      int      // 用于维护先前的混部内存虚拟块数
}

func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		ColocMemoryList: make([]string, 0),
	}
}

// 初始化内存信息
func (m *MemoryManager) NewMemoryManager() error {

	m.updateState()
	m.adjustBlocks()
	klog.Infof("[NewMemoryManager] 初始化内存信息: 系统总内存=%d, 在线任务内存=%d, 安全水位=%d\n", m.TotalMemory, m.OnlinePodsUsed, m.SafetyMargin)
	return nil
}

// 定期刷新状态
func (m *MemoryManager) WatchColocationMemory() {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		m.updateState()
		m.adjustBlocks()
		klog.Info("[WatchMemory] 混部内存更新: ", m.ColocMemoryList[len(m.ColocMemoryList)-3:], m.prevBlocks)
	}
}

// 更新内存状态
func (m *MemoryManager) updateState() {

	total, onlinePodsUsed, err := getSystemMemeoryInfo()
	if err != nil {
		return
	}

	m.TotalMemory = total
	m.OnlinePodsUsed = onlinePodsUsed
	m.SafetyMargin = uint64(float64(total) * SafetyWatermark)
	m.calculateColocationMemory()
}

// 重新计算混部内存
func (m *MemoryManager) calculateColocationMemory() {
	// 计算可用混部内存
	available := int64(m.TotalMemory) - int64(m.OnlinePodsUsed) - int64(m.SafetyMargin)
	if available < 0 {
		m.ColocMemory = 0
	} else {
		m.ColocMemory = uint64(available)
	}
}

// 调整虚拟块队列
func (m *MemoryManager) adjustBlocks() {

	// 计算当前块数
	currentBlocks := int(m.ColocMemory / BlockSize)
	delta := currentBlocks - m.prevBlocks

	switch {
	case delta > 0:
		// 增加块
		for i := range delta {
			m.ColocMemoryList = append(m.ColocMemoryList, fmt.Sprintf("colocationMemory%d", i+m.prevBlocks))
		}
	case delta < 0:
		// 减少块（从尾部移除）
		removeCount := -delta
		if removeCount > len(m.ColocMemoryList) {
			m.ColocMemoryList = m.ColocMemoryList[:0]
		} else {
			m.ColocMemoryList = m.ColocMemoryList[:len(m.ColocMemoryList)-removeCount]
		}
	}

	m.prevBlocks = currentBlocks
}

// 获取合并的NUMA信息
func getSystemMemeoryInfo() (total, k8sUsed uint64, err error) {
	// TODO: 这里写死了NUMA0和NUMA1，后续可以考虑动态获取
	// 获取NUMA0信息
	node0, err := GetNumaMemInfo(0)
	if err != nil {
		return 0, 0, err
	}

	// 获取NUMA1信息
	node1, err := GetNumaMemInfo(1)
	if err != nil {
		return 0, 0, err
	}

	// 合并K8s使用量
	k8sOnlineMemoryPath := path.Join(K8sPodsBasePath, BurstablePath)
	k8sOnlineMemoryUsage, err := GetCgroupsMemoryInfo(k8sOnlineMemoryPath)
	if err != nil {
		return 0, 0, err
	}
	klog.Info("[getSystemMemoryInfo] k8sOnlineMemoryUsage:", k8sOnlineMemoryUsage)

	return node0.Free + node1.Free, k8sOnlineMemoryUsage, nil
}
