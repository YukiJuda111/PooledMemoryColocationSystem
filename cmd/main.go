package main

import (
	"liuyang/colocation-memory-device-plugin/pkg/device_plugin"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"liuyang/colocation-memory-device-plugin/pkg/utils"

	"k8s.io/klog/v2"
)

func main() {
	klog.Infof("device plugin starting")
	// 初始化memory manager
	mm := memory_manager.NewMemoryManager()

	// 初始化colocation memory device plugin
	dp := device_plugin.NewColocationMemoryDevicePlugin(mm)
	go dp.Run()

	// register when device plugin start
	if err := dp.Register(); err != nil {
		klog.Fatalf("register to kubelet failed: %v", err)
	}

	// watch kubelet.sock,when kubelet restart,exit device plugin,then will restart by DaemonSet
	stop := make(chan struct{})
	err := utils.WatchKubelet(stop)
	if err != nil {
		klog.Fatalf("start to kubelet failed: %v", err)
	}

	<-stop
	klog.Infof("kubelet restart,exiting")
}
