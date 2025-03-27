package common

import "time"

const (
	ResourceName   string = "liuyang.io/colocation-memory"
	DevicePath     string = "/etc/gophers"
	DeviceSocket   string = "colocation-memory.sock"
	ConnectTimeout        = time.Second * 5
)
