package device_plugin

import (
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"strings"
	"time"

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

	// w, err := fsnotify.NewWatcher()
	// if err != nil {
	// 	return errors.WithMessage(err, "new watcher failed")
	// }
	// defer w.Close()

	// errChan := make(chan error)
	// go func() {
	// 	defer func() {
	// 		if r := recover(); r != nil {
	// 			errChan <- fmt.Errorf("device watcher panic:%v", r)
	// 		}
	// 	}()
	// 	for {
	// 		select {
	// 		case event, ok := <-w.Events:
	// 			if !ok {
	// 				continue
	// 			}
	// 			klog.Infof("fsnotify device event: %s %s", event.Name, event.Op.String())

	// 			if event.Op == fsnotify.Create {
	// 				dev := path.Base(event.Name)
	// 				d.devices[dev] = &pluginapi.Device{
	// 					ID:     dev,
	// 					Health: pluginapi.Healthy,
	// 				}
	// 				d.notify <- struct{}{}
	// 				klog.Infof("find new device [%s]", dev)
	// 			} else if event.Op&fsnotify.Remove == fsnotify.Remove {
	// 				dev := path.Base(event.Name)
	// 				delete(d.devices, dev)
	// 				d.notify <- struct{}{}
	// 				klog.Infof("device [%s] removed", dev)
	// 			}

	// 		case err, ok := <-w.Errors:
	// 			if !ok {
	// 				continue
	// 			}
	// 			klog.Errorf("fsnotify watch device failed:%v", err)
	// 		}
	// 	}
	// }()

	// err = w.Add("/etc/gophers")
	// if err != nil {
	// 	return fmt.Errorf("watch device error:%v", err)
	// }

	// return <-errChan

	ticker := time.NewTicker(common.RefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		d.mm.UpdateState()
		d.adjustDevices()
		klog.Info("[Watch] 混部内存更新: ", d.mm.ColocMemoryList[len(d.mm.ColocMemoryList)-3:], d.mm.PrevBlocks)
	}
	return nil

}

// 调整虚拟块队列块数和设备列表
func (d *DeviceMonitor) adjustDevices() {

	// 计算当前块数
	currentBlocks := int(d.mm.ColocMemory / common.BlockSize)
	delta := currentBlocks - d.mm.PrevBlocks

	switch {
	case delta > 0:
		// 增加块
		// TODO: 内存增加时，先看看在用池化内存的pod有没有可以换入的
		for i := range delta {
			d.mm.ColocMemoryList = append(d.mm.ColocMemoryList, fmt.Sprintf("colocationMemory%d", i+d.mm.PrevBlocks))
		}
	case delta < 0:
		// 减少块（从尾部移除）
		// TODO: 内存减少时，得用env来检查有没有不够用的pod，把pod换入CXL或兜底迁移
		removeCount := -delta
		if removeCount > len(d.mm.ColocMemoryList) {
			d.mm.ColocMemoryList = d.mm.ColocMemoryList[:0]
		} else {
			d.mm.ColocMemoryList = d.mm.ColocMemoryList[:len(d.mm.ColocMemoryList)-removeCount]
		}
	case delta == 0:
		// TODO: 不变
	}

	d.mm.PrevBlocks = currentBlocks
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
