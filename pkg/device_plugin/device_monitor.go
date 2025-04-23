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
	klog.Infoln("watching devices")

	ticker := time.NewTicker(common.RefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
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

	// 滞后区间
	if delta >= -common.DebounceThreshold && delta <= common.DebounceThreshold {
		klog.Info("[adjustDevices] 防抖: 设备数量变化小于阈值，不做扩缩")
		return nil
	}

	// 冷却保护
	now := time.Now()
	if !d.mm.LastUpdateTime.IsZero() && now.Sub(d.mm.LastUpdateTime) < common.MinAdjustmentInterval {
		klog.Infof("[adjustDevices] 防抖: 两次调整间隔小于 %v,不做扩缩", common.MinAdjustmentInterval)
		return nil
	}

	switch {
	case delta > 0:
		// 增加块
		// TODO: 内存增加时，先看看在用池化内存的pod有没有可以换入的
		for range delta {
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
		d.notify <- struct{}{}
		klog.Info("[adjustDevices] 增加设备数量: ", delta)
	case delta < 0:
		// 减少块
		removeCount := -delta
		// 在d.mm.ColocMemoryMap中找到Used == false的块并删除
		for range removeCount {
			found := false

			for k, v := range d.mm.Uuid2ColocMetaData {
				if !v.Used {
					klog.Info("[adjustDevices] 删除设备:", d.mm.Uuid2ColocMetaData[k])
					delete(d.mm.Uuid2ColocMetaData, k)
					delete(d.devices, k)

					found = true // 找到一个可以删除的块
					break
				}

				// TODO: 如果没有找到Used == false的块，直接删除used == true的块，然后根据维护信息把块对应的pod扔到cxl / 迁移
				// 维护了updateTime,刚好用lru来选择
				if !found {
					klog.Info("[adjustDevices] 没有找到可以删除的块，尝试进行池化内存迁移")

					lruKey, lruMeta := d.chooseColocBlocksByLru()
					if lruKey != "" {
						klog.Warningf("[adjustDevices] LRU删除正在使用的设备: %v,对应pod需迁移", lruMeta)

						// 迁移pod逻辑，回传error来检测是否迁移成功，保证一致性

						// 可以记录 lruMeta.PodUID 到迁移列表或迁移系统
						delete(d.mm.Uuid2ColocMetaData, lruKey)
						delete(d.devices, lruKey)

						// 因为这里的block是和pod绑定的，所以需要把这个pod的设备删除
						if _, ok := d.mm.Pod2ColocIds[lruMeta.BindPod]; ok {
							// 删除这个pod的设备
							delete(d.mm.Pod2ColocIds, lruMeta.BindPod)
							klog.Infof("[adjustDevices] 删除 %s 的设备映射关系", lruMeta.BindPod)
						}
					}

				}
			}
		}
		d.notify <- struct{}{}
		klog.Info("[adjustDevices] 移除设备数量: ", removeCount)
	case delta == 0:
		// TODO: 不变
		klog.Info("[adjustDevices] 设备数量不变")
	}

	d.mm.PrevBlocks = currentBlocks
	d.mm.LastUpdateTime = time.Now()
	return nil
}

// removeColocDevicesByLru 根据 LRU 策略删除设备（优先删除未使用的设备，如果不够则按更新时间最老的设备删除）
func (d *DeviceMonitor) removeColocDevicesByLru(removeCount int) {
	// 目标删除数量
	targetDeleteCount := removeCount
	deletedCount := 0

	// Step 1: 删除未使用的设备块
	var unusedKeys []string
	for k, v := range d.mm.Uuid2ColocMetaData {
		if !v.Used {
			unusedKeys = append(unusedKeys, k)
		}
	}

	// 删除未使用的块，直到删除目标满足
	for _, k := range unusedKeys {
		klog.Infof("[adjustDevices] 删除未使用设备: %v", d.mm.Uuid2ColocMetaData[k])
		delete(d.mm.Uuid2ColocMetaData, k)
		delete(d.devices, k)
		deletedCount++
		if deletedCount >= targetDeleteCount {
			break
		}
	}

	// Step 2: 如果删除数量不够，按 LRU 删除正在使用的块
	if deletedCount < targetDeleteCount {
		// 收集所有正在使用的设备块，并按照更新时间进行排序
		type usedBlockInfo struct {
			DeviceName string
			UpdateTime time.Time
			BindPod    string
		}
		var usedBlocks []usedBlockInfo
		usedBlock2Pod := make(map[string]string) // 用于记录每个 block 所绑定的 Pod

		for k, v := range d.mm.Uuid2ColocMetaData {
			if v.Used {
				usedBlocks = append(usedBlocks, usedBlockInfo{
					DeviceName: k,
					UpdateTime: v.UpdateTime,
					BindPod:    v.BindPod,
				})
				usedBlock2Pod[k] = v.BindPod
			}
		}

		// 按更新时间升序排序，最老的 block 排前面
		sort.Slice(usedBlocks, func(i, j int) bool {
			return usedBlocks[i].UpdateTime.Before(usedBlocks[j].UpdateTime)
		})

		// Step 3: 选择需要迁移的 Pod，直到满足删除数量
		podsToMigrate := make(map[string]bool)
		collectedBlocks := make(map[string]bool)

		// 选择 Pod，直到删除数量达到目标
		for _, blk := range usedBlocks {
			if deletedCount >= targetDeleteCount {
				break
			}
			podsToMigrate[blk.BindPod] = true
		}

		// Step 4: 删除选中的 Pod 绑定的所有块
		for podUID := range podsToMigrate {
			blockIDs := d.mm.Pod2ColocIds[podUID]
			klog.Warningf("[adjustDevices] 迁移 Pod: %s, 删除绑定块: %v", podUID, blockIDs)

			for _, blkID := range blockIDs {
				if _, exists := d.mm.Uuid2ColocMetaData[blkID]; exists && !collectedBlocks[blkID] {
					klog.Infof("[adjustDevices] 删除块: %s", blkID)
					delete(d.mm.Uuid2ColocMetaData, blkID)
					delete(d.devices, blkID)
					collectedBlocks[blkID] = true
					deletedCount++
				}
			}
			delete(d.mm.Pod2ColocIds, podUID)
			klog.Infof("[adjustDevices] 删除 Pod 映射: %s", podUID)

			// TODO: 执行迁移逻辑，比如 migrationQueue <- podUID
			if deletedCount >= targetDeleteCount {
				break
			}
		}
	}

	// 发送通知
	d.notify <- struct{}{}
	klog.Infof("[adjustDevices] 总共删除设备数量: %d（目标 %d）", deletedCount, targetDeleteCount)
}

func (d *DeviceMonitor) chooseColocBlocksByLru() (string, *memory_manager.ColocMemoryBlockMetaData) {
	var lruKey string
	var lruMeta *memory_manager.ColocMemoryBlockMetaData
	minTime := time.Now()

	for k, v := range d.mm.Uuid2ColocMetaData {
		if v.UpdateTime.Before(minTime) {
			minTime = v.UpdateTime
			lruKey = k
			lruMeta = v
		}
	}

	return lruKey, lruMeta
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
