package device_plugin

import (
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"liuyang/colocation-memory-device-plugin/pkg/utils"
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
	// TODO: 回传error

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
			// newDeviceID := fmt.Sprintf(common.DeviceName, i+d.mm.PrevBlocks)
			// d.mm.ColocMemoryList = append(d.mm.ColocMemoryList, newDeviceID)
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
		// if removeCount > len(d.mm.ColocMemoryList) {
		// 	d.mm.ColocMemoryList = d.mm.ColocMemoryList[:0]
		// 	d.devices = d.devices[:0]
		// } else {
		// 	d.mm.ColocMemoryList = d.mm.ColocMemoryList[:len(d.mm.ColocMemoryList)-removeCount]
		// 	d.devices = d.devices[:len(d.devices)-removeCount]
		// }

		// 在d.mm.ColocMemoryMap中找到Used == false的块并删除
		for range removeCount {
			for k, v := range d.mm.Uuid2ColocMetaData {
				if !v.Used {
					klog.Info("[adjustDevices] 删除设备:", d.mm.Uuid2ColocMetaData[k])
					delete(d.mm.Uuid2ColocMetaData, k)
					delete(d.devices, k)
					break
				}
				// TODO: 如果没有找到Used == false的块，直接删除used === true的块，然后根据维护信息把块对应的pod扔到cxl / 迁移
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
