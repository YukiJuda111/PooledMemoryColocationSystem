package device_plugin

import (
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"liuyang/colocation-memory-device-plugin/pkg/utils"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type DeviceMonitor struct {
	devices map[string]*pluginapi.Device // uuid -> device
	notify  chan struct{}                // notify when device update
	mm      *memory_manager.MemoryManager
}

func NewDeviceMonitor(mm *memory_manager.MemoryManager) *DeviceMonitor {
	return &DeviceMonitor{
		devices: make(map[string]*pluginapi.Device),
		notify:  make(chan struct{}),
		mm:      mm,
	}
}

// List all devices
func (d *DeviceMonitor) List() error {
	// for _, dev := range d.mm.ColocMemoryList {
	// 	d.devices = append(d.devices, &pluginapi.Device{
	// 		ID:     dev,
	// 		Health: pluginapi.Healthy,
	// 	})
	// }

	for _, dev := range d.mm.Uuid2ColocMetaData {
		d.devices[dev.Uuid] = &pluginapi.Device{
			ID:     dev.Uuid,
			Health: pluginapi.Healthy,
		}
	}

	return nil
}

// Watch device change
func (d *DeviceMonitor) Watch() error {

	ticker := time.NewTicker(common.RefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		// 这里判断IsReady，防止pods_monitor在等待Pod进入running的时候还没更新ColocMetaData
		// 类似于锁
		if !d.mm.IsReady {
			klog.Info("[Watch] 有pod在创建过程中,跳过本次监控")
			continue
		}
		d.mm.UpdateState()
		if err := d.adjustDevices(); err != nil {
			klog.Errorf("[Watch] 调整设备失败: %v", err)
			return errors.WithMessagef(err, "调整设备失败")
		}
		klog.Info("[Watch] 混部内存块数量: ", d.mm.PrevBlocks)
	}

	return nil

}

// 调整虚拟块队列块数和设备列表
func (d *DeviceMonitor) adjustDevices() error {

	// 计算当前块数
	currentBlocks := int(d.mm.ColocMemory / common.BlockSize)
	delta := currentBlocks - d.mm.PrevBlocks

	// TODO: 为了测试方便,先不做防抖,记得改回来

	// // 滞后区间
	// if delta >= -common.DebounceThreshold && delta <= common.DebounceThreshold {
	// 	klog.Info("[adjustDevices] 防抖: 设备数量变化小于阈值，不做扩缩")
	// 	return nil
	// }

	// // 冷却保护
	// now := time.Now()
	// if !d.mm.LastUpdateTime.IsZero() && now.Sub(d.mm.LastUpdateTime) < common.MinAdjustmentInterval {
	// 	klog.Infof("[adjustDevices] 防抖: 两次调整间隔小于 %v,不做扩缩", common.MinAdjustmentInterval)
	// 	return nil
	// }

	switch {
	case delta > 0:
		// 增加块
		// TODO: 内存增加时，先看看在用池化内存的pod有没有可以换入的
		for range delta {
			d.generateNewBlock()
		}
		d.notify <- struct{}{}
		klog.Info("[adjustDevices] 增加设备数量: ", delta)
	case delta < 0:
		// 减少块
		removeCount := -delta
		d.removeColocDevicesMostUsed(removeCount)
	case delta == 0:
		// TODO: 不变
		klog.Info("[adjustDevices] 设备数量不变")
	}

	d.mm.PrevBlocks = currentBlocks
	d.mm.LastUpdateTime = time.Now()
	return nil
}

func (d *DeviceMonitor) removeColocDevicesMostUsed(removeCount int) {
	deletedCount := 0
	targetDeleteCount := removeCount

	// Step 1: 收集未使用的块
	unusedKeys := make([]string, 0, removeCount)
	for k, v := range d.mm.Uuid2ColocMetaData {
		if !v.Used {
			unusedKeys = append(unusedKeys, k)
			if len(unusedKeys) >= targetDeleteCount {
				break
			}
		}
	}

	// Step 2: 删除未使用的块
	for _, blkID := range unusedKeys {
		klog.Infof("[adjustDevices] 删除未使用块: %v", d.mm.Uuid2ColocMetaData[blkID])
		delete(d.mm.Uuid2ColocMetaData, blkID)
		delete(d.devices, blkID)
		deletedCount++
	}

	// Step 3: 如果不够，按 most_used 策略删除使用中的块（整 pod）
	if deletedCount < targetDeleteCount {
		type podStat struct {
			PodName  string
			BlockIDs []string
		}
		var podStats []podStat
		for podName, podInfo := range d.mm.Pod2PodInfo {
			podStats = append(podStats, podStat{
				PodName:  podName,
				BlockIDs: podInfo.BindColocIds,
			})
		}

		sort.Slice(podStats, func(i, j int) bool {
			return len(podStats[i].BlockIDs) > len(podStats[j].BlockIDs)
		})

		// 删除使用中块
		for _, pod := range podStats {
			if deletedCount >= targetDeleteCount {
				break
			}
			klog.Infof("[adjustDevices] 迁移 Pod: %s, 删除绑定块: %v", pod.PodName, pod.BlockIDs)
			for _, blkID := range pod.BlockIDs {
				delete(d.mm.Uuid2ColocMetaData, blkID)
				delete(d.devices, blkID)
				deletedCount++

				// 记录交换出去的块
				d.mm.Pod2PodInfo[pod.PodName].SwapColocIds = append(d.mm.Pod2PodInfo[pod.PodName].SwapColocIds, blkID)

				// 如果删除的块数量超过目标数量，维护新的块
				// 这样子对k8s来说多余的块空了出来，作为一个新的设备
				if deletedCount > targetDeleteCount {
					d.generateNewBlock()
				}
			}

			// 清空Pod绑定的虚拟内存块
			d.mm.Pod2PodInfo[pod.PodName].BindColocIds = []string{}

			klog.Infof("[adjustDevices] %s信息更新, BindColocIds数量: %d, SwapColocIds数量: %d", d.mm.Pod2PodInfo[pod.PodName].Name, len(d.mm.Pod2PodInfo[pod.PodName].BindColocIds), len(d.mm.Pod2PodInfo[pod.PodName].SwapColocIds))
			// TODO: 可加入异步迁移队列 migrationQueue <- pod.PodUID
		}
	}

	d.notify <- struct{}{}
	klog.Infof("[adjustDevices] 总共删除设备数量: %d（目标 %d）", deletedCount, targetDeleteCount)
}

func (d *DeviceMonitor) generateNewBlock() {
	newDeviceID := fmt.Sprintf(common.DeviceName, utils.GetUuid())

	d.mm.Uuid2ColocMetaData[newDeviceID] = &memory_manager.ColocMemoryBlockMetaData{
		Uuid:       newDeviceID,
		Used:       false,
		BindPod:    "",
		UpdateTime: time.Now(),
	}

	d.devices[newDeviceID] = &pluginapi.Device{
		ID:     newDeviceID,
		Health: pluginapi.Healthy,
	}
}

// Devices transformer map to slice
func (d *DeviceMonitor) Devices() []*pluginapi.Device {
	devices := make([]*pluginapi.Device, 0, len(d.devices))
	for _, device := range d.devices {
		devices = append(devices, device)
	}
	return devices
}

func String(devs []*pluginapi.Device) string {
	ids := make([]string, 0, len(devs))
	for _, device := range devs {
		ids = append(ids, device.ID)
	}
	return strings.Join(ids, ",")
}
