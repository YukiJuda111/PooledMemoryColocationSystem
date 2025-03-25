package memory_manager

// 增量式的内存管理
// 1. 初始化读取系统内存(numa0,numa1)、k8s在线任务内存、安全水位，混部内存 = 系统内存 - k8s在线任务内存 - 安全水位
// 2. 混部内存虚拟化分块： 混部资源 = 混部内存 / 512M，混部资源注册为colocationMemory0,colocationMemory1...
// 3. 维护混部资源为队列，动态监测每当有混部资源增加/减少时，从队尾开始相应增减colocationMemory
