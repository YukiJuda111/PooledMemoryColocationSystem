查看Pods使用的NUMA节点
```
crictl ps # 找到Running的容器
crictl inspect <container_name> | jq '.info.pid' # 找到pid
cat /proc/<pid>/numa_maps # 根据pid获取页的NUMA分布
```