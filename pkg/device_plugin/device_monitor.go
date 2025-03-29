package device_plugin

import (
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type DeviceMonitor struct {
	devices []*pluginapi.Device
	notify  chan struct{} // notify when device update
	mm      *memory_manager.MemoryManager
}

func NewDeviceMonitor(mm *memory_manager.MemoryManager) *DeviceMonitor {
	return &DeviceMonitor{
		devices: make([]*pluginapi.Device, 0),
		notify:  make(chan struct{}),
		mm:      mm,
	}
}

// List all devices
func (d *DeviceMonitor) List() error {
	for _, dev := range d.mm.ColocMemoryList {
		d.devices = append(d.devices, &pluginapi.Device{
			ID:     dev,
			Health: pluginapi.Healthy,
		})
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
		klog.Info("[Watch] 混部内存更新: ", d.mm.ColocMemoryList[len(d.mm.ColocMemoryList)-3:], d.mm.PrevBlocks)
	}

	return nil

}

// 调整虚拟块队列块数和设备列表
func (d *DeviceMonitor) adjustDevices() error {
	// TODO: 回传error

	// 计算当前块数
	currentBlocks := int(d.mm.ColocMemory / common.BlockSize)
	delta := currentBlocks - d.mm.PrevBlocks

	switch {
	case delta > 0:
		// 增加块
		// TODO: 内存增加时，先看看在用池化内存的pod有没有可以换入的
		for i := range delta {
			newDeviceID := fmt.Sprintf(common.DeviceName, i+d.mm.PrevBlocks)
			d.mm.ColocMemoryList = append(d.mm.ColocMemoryList, newDeviceID)
			d.devices = append(d.devices, &pluginapi.Device{
				ID:     newDeviceID,
				Health: pluginapi.Healthy,
			})
		}
		d.notify <- struct{}{}
		klog.Info("[adjustDevices] 增加设备数量: ", delta)
	case delta < 0:
		// 减少块（从尾部移除）
		// TODO: 内存减少时，得用env来检查有没有不够用的pod，把pod换入CXL或兜底迁移
		removeCount := -delta
		if removeCount > len(d.mm.ColocMemoryList) {
			d.mm.ColocMemoryList = d.mm.ColocMemoryList[:0]
			d.devices = d.devices[:0]
		} else {
			d.mm.ColocMemoryList = d.mm.ColocMemoryList[:len(d.mm.ColocMemoryList)-removeCount]
			d.devices = d.devices[:len(d.devices)-removeCount]
		}
		d.notify <- struct{}{}
		klog.Info("[adjustDevices] 移除设备数量: ", removeCount)
	case delta == 0:
		// TODO: 不变
		klog.Info("[adjustDevices] 设备数量不变")
	}

	d.mm.PrevBlocks = currentBlocks
	return nil
}

func (d *DeviceMonitor) Devices() []*pluginapi.Device {
	return d.devices
}

func String(devs []*pluginapi.Device) string {
	ids := make([]string, 0, len(devs))
	for _, device := range devs {
		ids = append(ids, device.ID)
	}
	return strings.Join(ids, ",")
}
