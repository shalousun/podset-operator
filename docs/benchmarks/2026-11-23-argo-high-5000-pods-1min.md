# PodLoadTest 1 分钟任务基准：argo-high / 5000 Pods

- 日期：2026-11-23
- 集群：`~/.kube/argo-high`（ACK，v1.36.2，49 节点）
- Operator 镜像：`tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v3`
- 部署参数：reclaimer 并发 50；内存 limit 2Gi
- 测试命名空间：`podloadtest-run5`（已删除）
- 任务：`sleep 60`（1 分钟业务 Pod）

## 1. 测试配置

```yaml
apiVersion: data.clond.com.shalousun/v1
kind: PodLoadTest
metadata:
  name: podloadtest-1min
spec:
  total: 5000
  maxInFlight: 4000
  submitRatePerSecond: 300
  submitBurst: 300
  createConcurrency: 100
  durationSeconds: 600
  podTimeoutSeconds: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: worker
        image: tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/busybox:20260914
        command: ["sh", "-c", "sleep 60; echo done"]
        resources:
          requests:
            cpu: 10m
            memory: 16Mi
          limits:
            cpu: 100m
            memory: 64Mi
  cleanup:
    deleteCompletedPods: true
    deletePVCs: true
    ttlSecondsAfterFinished: 0
```

对应样例：`config/samples/data.clond_v1_podloadtest_1min.yaml`

## 2. 提交结果

| 指标 | 值 |
|---|---|
| 提交总数 | 5000（created 指标 5002，约 2 次重叠） |
| 提交错误 | 0 |
| 开始 / 完成（UTC） | 09:18:49Z / 09:21:15Z |
| 提交耗时 | 146s |
| 整轮平均提交速率 | **34.25 pods/s** |
| 运行中 10s 窗口示例 | 30.3 pods/s |
| 提交完成时 in-flight | 3297（ContainerCreating 1001、Pending 5、Running 1919、Completed 381） |

## 3. 延迟与任务耗时

| 指标 | 平均 | p50 | p90 | p95 | p99 |
|---|---|---|---|---|---|
| `pod_create_duration_seconds` | **2.467s** | ≤5s | ≤5s | ≤10s | ≤10s |
| `pod_delete_duration_seconds` | **1.396s** | ≤2.5s | ≤5s | ≤10s | ≤10s |
| `pod_completion_duration_seconds` | **82.41s**（4753 个完成） | ≤120s | ≤300s | ≤300s | ≤300s |
| `pod_lifetime_seconds` | **106.05s**（4753 个回收） | ≤120s | ≤300s | ≤300s | ≤300s |

说明：

- 业务运行 60s；completion 平均 82.41s，说明启动/调度平均约 22s，但长尾很重（p90 已到 300s 桶）
- lifetime 平均 106.05s，比 completion 多约 **23.6s**，是回收队列等待
- **247 个 Pod 在 300s 超时前没有完成**（5000 个提交、4753 个完成），占 4.9%
- 从提交完成到全部排空约 **295s**，整个测试生命周期约 **441s**

## 4. 与 10s 任务对比

| 指标 | 10s / v2（100 并发） | 10s / v3（200 并发） | **60s 任务** |
|---|---|---|---|
| 整体提交速率 | 37.3 pods/s | 41.3 pods/s | **34.3 pods/s** |
| create 平均延迟 | 2.230s | 3.113s | **2.467s** |
| completion 平均 | 15.25s | 20.81s | **82.41s** |
| lifetime 平均 | 53.07s | 44.43s | **106.05s** |
| 回收队列延迟（平均） | 37.8s | 23.6s | **23.6s** |
| 超时 Pod | 1 | 543 | **247** |
| 提交完成时 in-flight | 636 | 1544 | **3297** |
| 提交耗时 | 134s | 121s | **146s** |
| 总测试时长（含排空） | ~237s | ~240s | **~441s** |

## 5. 结论

1. **60s 任务反而让提交速率略降**（34.3 vs 37.3 pods/s）：Pod 存活时间长，in-flight 接近 4000 上限，同时大量 ContainerCreating/Running 增加了启动和调度压力。
2. **启动/调度长尾更明显**：completion p50 已到 120s 桶、p90 到 300s 桶；247 个 Pod 触发 300s 超时。
3. **回收本身不是瓶颈**：reclaimer 50 worker，delete 平均 1.4s，回收队列平均等待约 23.6s，未出现大量 Completed 积压。
4. **1 小时容量估算**（按当前 34.25 pods/s 线性外推）：
   - 提交量约 **12.3 万/小时**
   - 按 95.1% 完成率，成功完成约 **11.7 万/小时**
   - 这是短时 burst 外推，持续运行下启动/超时和集群负载可能使实际值更低
5. **建议**：
   - 60s 任务场景建议把 `podTimeoutSeconds` 提到 600，或降低 `createConcurrency`/`submitRatePerSecond`，减少启动长尾导致的超时
   - 预热镜像、减少启动时间，比继续加 create/reclaimer 并发更有效
   - in-flight 接近 4000 时，`maxInFlight` 需要根据 60s 任务的目标吞吐调整（4000 / 60s ≈ 66 pods/s 理论上限）

## 6. 原始指标

- 过滤后的 run5 指标：[`2026-11-23-argo-high-5000-pods-1min.metrics.txt`](./2026-11-23-argo-high-5000-pods-1min.metrics.txt)
- 完整原始文件：`/tmp/argo5-metrics-final.txt`（未入库）
- 运行中快照：`/tmp/argo5-metrics1.txt`、`/tmp/argo5-metrics2.txt`
