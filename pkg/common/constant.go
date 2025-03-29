package common

import "time"

const (
	ResourceName    string = "liuyang.io/colocation-memory"
	DevicePath      string = "/etc/gophers"
	DeviceSocket    string = "colocation-memory.sock"
	ConnectTimeout         = time.Second * 5
	BlockSize              = 512 * 1024 * 1024 // 512MB
	SafetyWatermark        = 0.1               // 10%安全水位
	K8sPodsBasePath        = "/sys/fs/cgroup/kubepods.slice"
	BurstablePath          = "/kubepods-burstable.slice/memory.current"
	BestEffortPath         = "/kubepods-besteffort.slice/memory.current"
	RefreshInterval        = 5 * time.Second
)
