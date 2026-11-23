# PodSet Operator
An example of Kubernetes Operator built with the kubebuilder.

It contains two controllers:

- `PodSet`: the original example, which scales a fixed set of Pods up and down. It demonstrates how a CRD, a controller and the reconciliation loop fit together.
- `PodLoadTest`: a Pod submission load tester. It submits Pods at a configurable rate, reclaims completed Pods immediately, and exports Prometheus metrics for submission throughput. See the PodLoadTest section below.

The PodSet controller takes care of scaling up and down pods that run a sleep 3600 command in a busybox image. Nothing really fancy, but the point is to:

- see how the CRD and operator controller can be defined, packaged and deployed

- demonstrate the logic of reconcialation when a custom resource changes, or when pods are added or removed by a user

## Requirements
- controller-runtime: v0.25.x (k8s.io: v0.37.x)
- kubebuilder version: v2.3.1+
- kubernetes: v1.16+ (CRD apiextensions.k8s.io/v1)
- go version: v1.26+
- docker version: 17.03+
## Building and deploying
执行make install即可部署CRD到kubernetes：
```shell
make install
```
部署成功后，用api-versions命令可以查到该GV：
```shell
kubectl api-versions|grep data.clond
```
## 部署podset-operator集群
```shell
kubectl apply -f config/samples/data.clond_v1_podset.yaml
```
删除pod
```shell
kubectl apply -f config/samples/pod_delete.yaml
```
pod扩容
```shell
kubectl apply -f config/samples/scale_up.yaml
```
pod缩容
```shell
kubectl apply -f config/samples/scale_down.yaml
```
清理pod
```shell
kubectl delete -f config/samples/data.clond_v1_podset.yaml
```
pod查看
```shell
kubectl get pod
```
operator查看
```shell
kubectl get podset
```
operator的状态查看
```shell
kubectl describe podset podset-sample
```
operator的状态也是在controller中设置的。

## Pod 提交压测（PodLoadTest）
`PodLoadTest` 用于压测集群的 Pod 提交能力：按可配置速率并发提交 Pod，Pod 完成（Succeeded/Failed）后由独立控制器立即回收，避免元数据堆积。提交与回收运行在两个独立控制器和队列中，互不阻塞。

```shell
# 快速提交 + 完成即回收：默认 1000 个，100/s，最多 200 个未完成
kubectl apply -f config/samples/data.clond_v1_podloadtest.yaml

# 每个 Pod 绑定独立 PVC/PV 的版本
kubectl apply -f config/samples/data.clond_v1_podloadtest_storage.yaml

kubectl get plt -w
```

关键字段：

| 字段 | 说明 |
|---|---|
| `total` | 总共提交的 Pod 数；0 表示不限，直到 `durationSeconds` 到期或对象被删除 |
| `maxInFlight` | 同时未完成 Pod 的上限（背压） |
| `submitRatePerSecond` / `submitBurst` | 客户端令牌桶限流，避免打崩 API Server；0 表示不限速 |
| `createConcurrency` | 并发 create 请求数，用于压满 API Server |
| `durationSeconds` | 最长提交时长 |
| `template` | Pod 模板（完整 PodTemplateSpec，schemaless）；`restartPolicy` 为空时自动设为 `Never` |
| `storage` | 开启后每个 Pod 创建独立 PVC 并挂载 |
| `cleanup.deleteCompletedPods` / `deletePVCs` / `ttlSecondsAfterFinished` | 回收策略；完成的 Pod 默认立即删除 |
| `podTimeoutSeconds` | 未完成 Pod 的超时回收，防止异常 Pod 卡住测试 |
| `suspend` | 暂停提交，回收继续 |

> 提示：`template.image` 在每个节点首次拉取会显著影响结果。如果要测 API Server 的**纯提交能力**，请提前在所有节点拉好镜像（或使用本地镜像仓库/预热 DaemonSet）；否则测到的是镜像拉取和调度速度。

### 指标
Metrics 通过 manager 的 HTTPS `:8443/metrics` 暴露，使用 Kubernetes 原生认证鉴权（TokenReview + SubjectAccessReview）。不需要 `kube-rbac-proxy` sidecar，也不需要 cert-manager；controller-runtime 会自动生成自签名证书。

抓取端需要：
- 一个 ServiceAccount，并绑定到 `metrics-reader` ClusterRole
- 该 SA 的 bearer token；自签名证书需要 `insecureSkipVerify: true` 或配置对应 CA

```shell
kubectl create serviceaccount metrics-scraper -n monitoring
kubectl create clusterrolebinding metrics-scraper \
  --clusterrole=metrics-reader \
  --serviceaccount=monitoring:metrics-scraper

TOKEN=$(kubectl create token metrics-scraper -n monitoring)
kubectl -n podset-operator-system port-forward \
  svc/podset-operator-controller-manager-metrics-service 8443:8443 &
curl -sk -H "Authorization: Bearer ${TOKEN}" https://127.0.0.1:8443/metrics | head
```

仅本地调试时，可以在 `config/default/manager_metrics_patch.yaml` 中把 `--metrics-secure` 改为 `false`，以 HTTP 且无鉴权方式暴露 metrics（不推荐）。

| 指标 | 说明 |
|---|---|
| `podloadtest_pods_created_total` | 成功提交的 Pod 总数 |
| `podloadtest_pod_create_errors_total` | 提交失败的次数 |
| `podloadtest_pod_create_duration_seconds` | 单次 Pod 提交 API 延迟 |
| `podloadtest_pvcs_created_total` | 创建的 PVC 总数 |
| `podloadtest_pods_in_flight` | 当前未完成的 Pod 数 |
| `podloadtest_submission_rate_per_second` | 最近 10 秒平均提交速率 |
| `podloadtest_pods_reclaimed_total` | 已回收的 Pod 数 |
| `podloadtest_pod_lifetime_seconds` | 完成 Pod 的生命周期分布 |
| `podloadtest_pod_completion_duration_seconds` | Pod 从创建到完成（Succeeded/Failed）的耗时，不含回收队列等待 |
| `podloadtest_pod_delete_duration_seconds` | 回收器发出 Pod delete 请求的 API 延迟 |

回收并发可通过 manager 参数 `--reclaimer-concurrency` 调整（默认 50）。

每秒提交数：

```promql
sum(rate(podloadtest_pods_created_total[1m]))
```

注意：client-go 默认只有 20 QPS，会限制提交速度。`main.go` 提供了 `--kube-api-qps`（默认 1000）和 `--kube-api-burst`（默认 2000），请根据 API Server 能力调整；`submitRatePerSecond` 才是业务侧的提交限流。

## Build image
```shell
make docker-build docker-push IMG=shaloudocker/podset:v1
```
基于 kustomize 直接部署（`make kustomize` 会把 kustomize 安装到项目的 `./bin`）：
```shell
make deploy IMG=shaloudocker/podset:v1
```

## 基于 YAML 部署 Operator
`config/manifests/all.yaml` 是 `config/default` 渲染出的 All-in-One 清单，包含 Namespace、CRD、RBAC、Metrics Service 和 Deployment。

### 方式一：直接使用 GitHub 上的 YAML（无需克隆仓库）

```shell
# 部署 operator
kubectl apply -f https://raw.githubusercontent.com/shalousun/podset-operator/main/config/manifests/all.yaml

# 确认
kubectl get pods -n podset-operator-system
kubectl get crd | grep data.clond

# 创建压测任务（普通 / 带 PVC）
kubectl apply -f https://raw.githubusercontent.com/shalousun/podset-operator/main/config/samples/data.clond_v1_podloadtest.yaml
kubectl apply -f https://raw.githubusercontent.com/shalousun/podset-operator/main/config/samples/data.clond_v1_podloadtest_storage.yaml
kubectl get plt -w

# 卸载
kubectl delete -f https://raw.githubusercontent.com/shalousun/podset-operator/main/config/manifests/all.yaml
```

> 注意：清单中的镜像由 `config/manager/kustomization.yaml` 决定，当前指向 `shaloudocker/podset:v1`。
> 该镜像构建于 2022 年，不包含 PodLoadTest 控制器。发布新版本前，请先用方式二构建并推送新镜像，
> 并把这里的 tag 更新为新镜像（例如 `v2`），否则一键部署得到的是旧版本 operator。

### 方式二：使用自己构建的镜像生成 YAML
1. 构建并推送镜像：

```shell
make docker-build docker-push IMG=<registry>/podset-operator:<tag>
```

2. 用你的镜像重新生成 All-in-One YAML：

```shell
make allYaml IMG=<registry>/podset-operator:<tag>
```

`make allYaml` 会先更新 `config/manager/kustomization.yaml` 中的镜像，再生成 `config/manifests/all.yaml`。提交这两个文件后，方式一里的 GitHub raw 链接就会使用新镜像。如果不想修改仓库里的文件，也可以直接编辑 `config/manifests/all.yaml` 里的 `image:` 字段。

3. 部署：

```shell
kubectl apply -f config/manifests/all.yaml
```

4. 确认：

```shell
kubectl get pods -n podset-operator-system
kubectl get crd | grep data.clond
```

5. 创建压测任务：

```shell
kubectl apply -f config/samples/data.clond_v1_podloadtest.yaml
kubectl get plt -w
```

6. 卸载：

```shell
kubectl delete -f config/manifests/all.yaml
```

## Uninstall And Clean
```shell
make uninstall
```
