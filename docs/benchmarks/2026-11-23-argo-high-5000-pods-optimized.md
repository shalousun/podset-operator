# PodLoadTest 优化版基准：argo-high / 5000 Pods

> 后续并发 200 实验见 [concurrency-200 benchmark](./2026-11-23-argo-high-5000-pods-concurrency-200.md)。

- 日期：2026-11-23
- 集群：`~/.kube/argo-high`（ACK，v1.36.2，49 节点）
- Operator 镜像：`tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v2`
- 优化内容：
  - 创建器改用 manager cache 统计 in-flight，去掉每轮全量 APIReader List
  - Reclaimer `MaxConcurrentReconciles=50`
  - 无 storage 时跳过 PVC 查询
  - 新增 `pod_completion_duration_seconds`，区分业务耗时与回收队列延迟
- 测试命名空间：`podloadtest-run3`（已删除）
- 说明：run2 在回收阶段被集群节点缩容驱逐，计数器重置；run3 为干净完整结果，operator 无重启。

## 1. 测试配置

```yaml
apiVersion: data.clond.com.shalousun/v1
kind: PodLoadTest
metadata:
  name: podloadtest-sample
spec:
  total: 5000
  maxInFlight: 4000
  submitRatePerSecond: 300
  submitBurst: 300
  createConcurrency: 100
  durationSeconds: 600
  podTimeoutSeconds: 120
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: worker
        image: tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/busybox:20260914
        command: ["sh", "-c", "sleep 10; echo done"]
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

## 2. 提交结果

| 指标 | 值 |
|---|---|
| 提交总数 | 5000 / 5000 |
| 提交错误 | 0 |
| 开始时间（UTC） | 2026-11-23T08:14:41Z |
| 完成时间（UTC） | 2026-11-23T08:16:55Z |
| 提交耗时 | 134s |
| 整轮平均提交速率 | **37.3 pods/s** |
| 运行中 10s 窗口示例 | 28.2 pods/s（run2 同配置曾到 40.9/s，受集群负载波动影响） |

## 3. 创建延迟

| 指标 | 值 |
|---|---|
| `pod_create_duration_seconds` 平均 | **2.230s** |
| p50 / p90 | ≤2.5s / ≤5s |
| p95 / p99 | ≤10s / ≤10s |

`createConcurrency: 100` 比上一轮（50）提高了并发，但 Pod create API 延迟也从 1.414s 上升到 2.230s；提交吞吐只提升约 13%。

## 4. 回收结果

| 指标 | 值 |
|---|---|
| 回收总数 | 5000 / 5000 |
| 回收错误 | 0 |
| 提交完成时 Pod 状态 | ContainerCreating 150、Running 367、Completed 1554 |
| 提交完成时未删除的 Completed Pod | 1554 |
| 提交结束到全部排空 | 约 103s |
| 实测回收速率 | 约 20 pods/s |
| Operator 重启 | 0 |

## 5. 完成时长 vs 生命周期

| 指标 | 平均值 | p50 | p90 | p95 | p99 |
|---|---|---|---|---|---|
| `pod_completion_duration_seconds`（创建→完成） | **15.25s** | ≤20s | ≤20s | ≤30s | ≤300s |
| `pod_lifetime_seconds`（创建→回收） | **53.07s** | ≤60s | ≤120s | ≤120s | ≤300s |

新指标成功把业务耗时（10s sleep + 启动约 5s）从回收队列延迟中拆出来：平均回收队列等待约 **38s**。运行中积压较小时（56s 时）队列延迟约 6s，提交后期积压变大后延迟上升。

## 6. 优化前后对比

| 指标 | v1（优化前） | v2（优化后） | 变化 |
|---|---|---|---|
| 整体提交速率 | 32.9 pods/s | **37.3 pods/s** | **+13%** |
| createConcurrency | 50 | 100 | |
| create 平均延迟 | 1.414s | 2.230s | +58% |
| create p95 | ≤5s | ≤10s | 变差 |
| 提交完成时 Completed 积压 | 1746 | 1554 | -11% |
| 回收排空速率 | ~18.6 pods/s | ~20 pods/s | 基本持平 |
| `pod_lifetime_seconds` 平均 | 61.59s | 53.07s | -14% |
| `pod_completion_duration_seconds` | 无 | 15.25s | 新增 |
| Reclaimer worker 数 | 1 | 50 | |

## 7. 结论

1. **集群 Pod create API 延迟仍是根本瓶颈**：并发从 50 提到 100 后，请求延迟同步上升，吞吐只提升 13%；`submitRatePerSecond: 300` 和 `maxInFlight: 4000` 仍未触及。
2. **Reclaimer 并发提升只部分有效**：50 worker 能把积压较小时的回收延迟降到约 6s，但 delete API 延迟同样很高（约 2s），50 worker 的吞吐约 20–25/s，仍低于约 37/s 的创建速度，因此提交后期仍会积压。
3. **新增的 completion 指标很有价值**：`pod_completion_duration_seconds` 平均 15.25s，而 `pod_lifetime_seconds` 平均 53.07s，说明大约 38s 是回收队列延迟，而非业务运行时间。
4. **节点缩容会干扰基准**：run2 被节点缩容驱逐，计数器重置；做长时间基准时应避开节点缩容窗口或给 operator 加 PDB/更高优先级。

## 8. 下一步建议

- 若目标是测集群 API 提交上限：继续提高 `createConcurrency`（需要把 CRD 上限从 100 放宽）或寻找 API 延迟更低的时间窗口；单纯加并发会推高延迟。
- 若目标是高 churn 稳定回收：Reclaimer worker 提到 100–200，或按 namespace/UID 分片；否则把 `submitRatePerSecond` 控制在回收能力附近（约 20–25/s）。
- 考虑为 create/delete 延迟单独加 `pod_delete_duration_seconds`，确认 delete API 是否是当前回收瓶颈。
- 业务运行时间请使用 `pod_completion_duration_seconds`；`pod_lifetime_seconds` 包含回收队列。

## 9. 原始指标

- 过滤后的 run3 指标：[`2026-11-23-argo-high-5000-pods-optimized.metrics.txt`](./2026-11-23-argo-high-5000-pods-optimized.metrics.txt)
- 完整原始文件：`/tmp/argo3-metrics-final.txt`（未入库）
- 运行中快照：`/tmp/argo3-metrics1.txt`、`/tmp/argo3-metrics2.txt`
