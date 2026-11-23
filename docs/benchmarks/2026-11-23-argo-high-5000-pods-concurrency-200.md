# PodLoadTest 并发 200 基准：argo-high / 5000 Pods

> 后续 60s 任务实验见 [1-minute benchmark](./2026-11-23-argo-high-5000-pods-1min.md)。

> 前置基准：[v1 优化前](./2026-11-23-argo-high-5000-pods.md) / [v2 并发 100](./2026-11-23-argo-high-5000-pods-optimized.md)

- 日期：2026-11-23
- 集群：`~/.kube/argo-high`（ACK，v1.36.2，49 节点）
- Operator 镜像：`tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v3`
- 本轮实验参数：`createConcurrency: 200`、部署参数 `--reclaimer-concurrency=200`、内存 limit 2Gi
- 测试命名空间：`podloadtest-run4`（已删除）
- 实验后已将线上 reclaimer 并发恢复为 50

## 1. 测试配置

```yaml
spec:
  total: 5000
  maxInFlight: 4000
  submitRatePerSecond: 300
  submitBurst: 300
  createConcurrency: 200
  durationSeconds: 600
  podTimeoutSeconds: 120
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: worker
        image: tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/busybox:20260914
        command: ["sh", "-c", "sleep 10; echo done"]
```

## 2. 提交结果

| 指标 | 值 |
|---|---|
| 提交总数 | 5000（created 指标 5001，约 1 次重叠创建） |
| 提交错误 | 0 |
| 开始 / 完成（UTC） | 08:48:14Z / 08:50:15Z |
| 提交耗时 | 121s |
| 整轮平均提交速率 | **41.3 pods/s** |
| 运行中 10s 窗口示例 | 36.9 pods/s |
| 提交完成时 in-flight | 1544 |

## 3. 延迟与 Pod 启动

| 指标 | 平均 | p50 | p90 | p95 | p99 |
|---|---|---|---|---|---|
| `pod_create_duration_seconds` | **3.113s** | ≤5s | ≤10s | ≤10s | ≤10s |
| `pod_delete_duration_seconds` | **4.874s**（运行中曾 125ms） | ≤2.5s | ≤10s | ≤10s | ≤10s |
| `pod_completion_duration_seconds` | **20.81s**（4457 个完成） | ≤20s | ≤60s | ≤120s | ≤300s |
| `pod_lifetime_seconds` | 44.43s（4457 个回收） | ≤30s | ≤120s | ≤300s | ≤300s |

提交完成时的 Pod 状态：

```
ContainerCreating=823  Pending=107  Running=390  Completed=905
```

约 **543 个 Pod 在 120s 内没有进入完成状态，被 podTimeout 回收**。

## 4. 三轮对比

| 指标 | v1（并发 50 / worker 1） | v2（并发 100 / worker 50） | v3（并发 200 / worker 200） |
|---|---|---|---|
| 整体提交速率 | 32.9 pods/s | 37.3 pods/s | **41.3 pods/s** |
| create 平均延迟 | 1.414s | 2.230s | **3.113s** |
| create p95 | ≤5s | ≤10s | ≤10s |
| delete 平均延迟 | 未采集 | 未采集 | 4.874s（运行中 125ms） |
| completion 平均 | 未采集 | 15.25s | 20.81s |
| lifetime 平均 | 61.59s | 53.07s | 44.43s |
| 提交完成时 Completed 积压 | 1746 | 1554 | 905 |
| 超时 Pod | 0 | 1 | **543** |
| 回收排空 | ~18.6/s | ~20/s | ~17–20/s |

## 5. 结论

1. **继续加并发收益递减**：并发从 100 提到 200，吞吐仅从 37.3 提升到 41.3 pods/s（+11%），但 create 平均延迟从 2.23s 升到 3.11s，p50 落到 5s 桶。集群 API Server 的 Pod 提交路径已经饱和，更多并发主要是在排队。
2. **回收不是 delete API 慢**：运行中 delete 平均延迟只有 125ms；200 个 worker 同时删除时，排空阶段平均延迟被拉高到 4.87s，说明再增加 worker 只会增加 API Server 压力。实验后已把线上 reclaimer 恢复为 50。
3. **真正的瓶颈是 Pod 启动/调度**：提交完成时仍有 823 个 ContainerCreating、107 个 Pending；543 个 Pod 在 120s 超时前没有完成。镜像拉取、调度、kubelet 启动在高并发下成为主要限制。
4. **completion vs lifetime 指标很有价值**：completion 平均 20.81s（10s 业务 + 启动/调度），lifetime 44.43s（再含回收队列）；只看 lifetime 会把启动和回收延迟混在一起。
5. **推荐默认值**：`createConcurrency: 100`、`--reclaimer-concurrency=50`；CRD 上限保留 500 作为可调项；内存 limit 2Gi 保持（本轮稳定在 307Mi）。

## 6. 后续建议

- 如果目标是更高提交吞吐：先解决 Pod 启动/镜像拉取（节点预拉镜像、本地 registry、预热 DaemonSet），而不是继续加并发。
- 如果必须保证所有 Pod 完成：把 `podTimeoutSeconds` 提高到 300 或降低 `createConcurrency`，否则高并发下会有约 10% Pod 超时。
- 回收 worker 保持 50 左右；只有在确认 delete API 延迟低且队列积压时才考虑提高。
- 在集群空闲时段复测，排除现有 Argo 工作负载对 API Server 和调度器的影响。
- 代码/清单已保留 `--reclaimer-concurrency` 参数、delete 延迟指标、completion 指标和 CRD 500 并发上限。

## 7. 原始指标

- 过滤后的 run4 指标：[`2026-11-23-argo-high-5000-pods-concurrency-200.metrics.txt`](./2026-11-23-argo-high-5000-pods-concurrency-200.metrics.txt)
- 完整原始文件：`/tmp/argo4-metrics-final.txt`（未入库）
- 运行中快照：`/tmp/argo4-metrics1.txt`、`/tmp/argo4-metrics2.txt`
