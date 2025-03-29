package device_plugin

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// GetDevicePluginOptions returns options to be communicated with Device
// Manager
func (c *ColocationMemoryDevicePlugin) GetDevicePluginOptions(_ context.Context, _ *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{PreStartRequired: true}, nil
}

// ListAndWatch returns a stream of List of Devices
// Whenever a Device state change or a Device disappears, ListAndWatch
// returns the new list
// 这里上报的不是内存总量，而是排除了在线任务和安全水位后的内存余量
// 增加：append到尾部
// 减少：从尾到头删除
// 申请：从头到尾申请
func (c *ColocationMemoryDevicePlugin) ListAndWatch(_ *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
	devs := c.dm.Devices()
	klog.Info("[ListAndWatch] number of init devices: ", len(devs))

	err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: devs})
	if err != nil {
		return errors.WithMessage(err, "send device failed")
	}

	klog.Infoln("waiting for device update")
	for range c.dm.notify {
		devs = c.dm.Devices()
		klog.Infof("device update,new device list [%s]", String(devs))
		_ = srv.Send(&pluginapi.ListAndWatchResponse{Devices: devs})
	}
	return nil
}

// GetPreferredAllocation returns a preferred set of devices to allocate
// from a list of available ones. The resulting preferred allocation is not
// guaranteed to be the allocation ultimately performed by the
// devicemanager. It is only designed to help the devicemanager make a more
// informed allocation decision when possible.
func (c *ColocationMemoryDevicePlugin) GetPreferredAllocation(_ context.Context, _ *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	// 这里的逻辑不生效，改成在MemoryManager中维护算了...
	// 好处：双向维护
	// 弊端：一致性怎么保证
	return &pluginapi.PreferredAllocationResponse{}, nil

	// var containerResponses []*pluginapi.ContainerPreferredAllocationResponse
	// klog.Info("GetPreferredAllocation 执行了")
	// // 遍历每个容器的分配请求
	// for _, req := range r.ContainerRequests {
	// 	availableDevices := req.AvailableDeviceIDs
	// 	allocationSize := req.AllocationSize
	// 	klog.Info("排序前", availableDevices, allocationSize)

	// 	// 根据业务逻辑对设备进行排序
	// 	// 例如：按设备 ID 升序排列
	// 	sort.Strings(availableDevices)
	// 	klog.Info("排序后", availableDevices)
	// 	// 选择前 N 个设备
	// 	preferredDevices := availableDevices[:allocationSize]
	// 	klog.Info("选择前 N 个设备", preferredDevices)

	// 	// 添加到响应中
	// 	containerResponses = append(containerResponses, &pluginapi.ContainerPreferredAllocationResponse{
	// 		DeviceIDs: preferredDevices,
	// 	})
	// }

	// // 返回优先分配的设备列表
	// return &pluginapi.PreferredAllocationResponse{
	// 	ContainerResponses: containerResponses,
	// }, nil
}

// Allocate is called during container creation so that the Device
// Plugin can run device specific operations and instruct Kubelet
// of the steps to make the Device available in the container
// 由于没有用原生k8s的内存资源，这里最重要的是要用cgroups对内存做实际的限制
// 如果原先申请的设备资源（动态内存）不够了，运行中的POD并不会被自动驱逐，还是要用cgroups的驱逐机制
func (c *ColocationMemoryDevicePlugin) Allocate(_ context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	ret := &pluginapi.AllocateResponse{}
	for _, req := range reqs.ContainerRequests {
		klog.Infof("[Allocate] received request: %v", strings.Join(req.DevicesIDs, ","))
		resp := pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"ColocationMemory": strings.Join(req.DevicesIDs, ","),
			},
		}
		ret.ContainerResponses = append(ret.ContainerResponses, &resp)
	}
	return ret, nil
}

// PreStartContainer is called, if indicated by Device Plugin during registeration phase,
// before each container start. Device plugin can run device specific operations
// such as reseting the device before making devices available to the container
func (c *ColocationMemoryDevicePlugin) PreStartContainer(_ context.Context, _ *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
