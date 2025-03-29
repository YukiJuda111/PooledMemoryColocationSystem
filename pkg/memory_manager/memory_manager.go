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
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"path"

	"k8s.io/klog/v2"
)

type MemoryManager struct {
	TotalMemory     uint64   // 系统总内存 (NUMA0 + NUMA1)
	OnlinePodsUsed  uint64   // 在线任务内存使用量
	SafetyMargin    uint64   // 安全水位
	ColocMemory     uint64   // 可用混部内存
	ColocMemoryList []string // 混部内存虚拟块队列 = 可用混部内存 / BlockSize
	PrevBlocks      int      // 用于维护先前的混部内存虚拟块数
}

func NewMemoryManager() *MemoryManager {
	mm := &MemoryManager{
		ColocMemoryList: make([]string, 0),
	}
	err := mm.Initialize()
	if err != nil {
		klog.Fatalf("[NewMemoryManager] 初始化内存信息失败: %v", err)
	}
	return mm
}

// 初始化内存信息
func (m *MemoryManager) Initialize() error {

	m.UpdateState()
	// 计算初始块数
	currentBlocks := int(m.ColocMemory / common.BlockSize)
	for i := range currentBlocks {
		m.ColocMemoryList = append(m.ColocMemoryList, fmt.Sprintf(common.DeviceName, i))
	}
	// 记录上次块数
	m.PrevBlocks = currentBlocks
	klog.Infof("[NewMemoryManager] 初始化内存信息: 系统总内存=%d, 在线任务内存=%d, 安全水位=%d\n", m.TotalMemory, m.OnlinePodsUsed, m.SafetyMargin)
	return nil
}

// 更新内存状态
func (m *MemoryManager) UpdateState() {

	total, onlinePodsUsed, err := getSystemMemeoryInfo()
	if err != nil {
		return
	}

	m.TotalMemory = total
	m.OnlinePodsUsed = onlinePodsUsed
	m.SafetyMargin = uint64(float64(total) * common.SafetyWatermark)
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

// 获取合并的NUMA信息
func getSystemMemeoryInfo() (total, k8sUsed uint64, err error) {
	// TODO: 这里写死了NUMA0,1
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
	k8sOnlineMemoryPath := path.Join(common.K8sPodsBasePath, common.BurstablePath)
	k8sOnlineMemoryUsage, err := GetCgroupsMemoryInfo(k8sOnlineMemoryPath)
	if err != nil {
		return 0, 0, err
	}
	klog.Info("[getSystemMemoryInfo] k8sOnlineMemoryUsage:", k8sOnlineMemoryUsage)

	return node0.Free + node1.Free, k8sOnlineMemoryUsage, nil
}
