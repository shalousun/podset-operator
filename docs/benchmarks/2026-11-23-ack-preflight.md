# ACK 集群 1 分钟任务压测 preflight

- 日期：2026-11-23
- 目标集群：`~/.kube/ack`（ACK，v1.35.7）
- 结论：**环境可执行，但当前不空闲，operator 尚未安装；测试脚本已准备好，等待空闲窗口执行。**

## 1. 环境探测（只读）

| 项 | 结果 |
|---|---|
| 节点数 | **334** |
| 典型节点规格 | 188 CPU / ~353Gi 内存 / 703 Pods |
| 当前 Pod 总数 | **21,432** |
| 当前 CPU（抽样节点） | 29%–61% |
| 当前内存（抽样节点） | 22%–49% |
| Operator | **未安装**（无 `podset-operator-system`，无 `data.clond` CRD） |
| 权限 | 可创建 pods / namespaces / CRD / ClusterRoleBinding |
| 默认 SA 拉取凭据 | `acr-credential-secret-aggregation`（default 命名空间） |
| 资源配额 | 多个业务命名空间有 `platform-pods-*` 配额；新测试命名空间需在创建后确认 |

镜像仓库：

- operator 镜像 `tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v3` 已推送到 ACR
- 测试镜像 `tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/busybox:20260914` 已在之前集群使用
- 测试 Pod 未显式设置 `imagePullPolicy`；固定 tag 默认 `IfNotPresent`
- 新命名空间预期自动生成 ACR pull secret（脚本会检查；不会复制 secret）

## 2. 准备好的测试

1 分钟业务任务样例：

- `config/samples/data.clond_v1_podloadtest_1min.yaml`
- `total: 5000`
- `maxInFlight: 4000`
- `submitRatePerSecond: 300` / `submitBurst: 300`
- `createConcurrency: 100`
- `podTimeoutSeconds: 300`
- 任务：`sleep 60`
- 镜像：`infra/busybox:20260914`

坐标：

- 测试命名空间：`podloadtest-bench`（脚本默认，执行时新建）
- 预期提交耗时：在空闲 ACK 上约 2–5 分钟
- 全流程（含最后一批 60s 任务完成和回收）约 5–10 分钟

采集指标：

- `podloadtest_pods_created_total` / `pods_reclaimed_total` / `pod_create_errors_total`
- `pod_create_duration_seconds`
- `pod_delete_duration_seconds`
- `pod_completion_duration_seconds`
- `pod_lifetime_seconds`
- `pods_in_flight`
- PodLoadTest status（submitted / phase / startTime / completionTime）

## 3. 执行步骤（等空闲后进行）

### 3.1 部署 operator

```bash
./hack/deploy-podset-operator.sh \
  --kubeconfig ~/.kube/ack \
  --image tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v3
```

脚本会：

- 检查 CRD/ClusterRoleBinding 权限
- 创建 `podset-operator-system`
- 等待该命名空间自动生成 imagePullSecret（没有则中止，不复制/创建 secret）
- 用 `config/manifests/all.yaml` 生成 ACK 版本清单（替换镜像）
- 等待 manager rollout

### 3.2 执行 1 分钟压测并采集指标

```bash
./hack/run-podloadtest-benchmark.sh \
  --kubeconfig ~/.kube/ack \
  --sample config/samples/data.clond_v1_podloadtest_1min.yaml \
  --namespace podloadtest-bench \
  --cleanup
```

脚本会：

- 预检查 operator、CRD、metrics service、metrics-reader ClusterRole
- 新建测试命名空间并确认自动 pull secret
- 应用 PodLoadTest
- 创建临时 metrics scraper SA + ClusterRoleBinding
- 通过 HTTPS 8443 抓取运行中和最终 metrics
- 等待 phase=Completed 和 Pod 全部排空
- 保存 `/tmp/podloadtest-*` 指标与状态
- 清理测试命名空间和临时 RBAC（`--no-cleanup` 可保留）

两个脚本都支持 `--dry-run`，只做只读检查，不创建/修改资源。

## 4. 待办

- [ ] 等用户通知 ACK 集群空闲
- [ ] 执行 3.1 部署 operator
- [ ] 执行 3.2 1 分钟 5000 Pod 测试
- [ ] 整理指标报表并写入 `docs/benchmarks/`

> 注意：当前 ACK 集群有 21,432 个 Pod、CPU 30–60%，不是空闲状态；此时执行会混入其他业务负载，压测结果不可比。等空闲窗口再跑。
