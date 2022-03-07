# PodSet Operator
An example of Kubernetes Operator built with the kubebuilder.

This PodSet operator takes care of scaling up and down pods that run a sleep 3600 command in a busybox image. Nothing really fancy, but the point is to:

- see how the CRD and operator controller can be defined, packaged and deployed

- demonstrate the logic of reconcialation when a custom resource changes, or when pods are added or removed by a user

## Requirements
- kubebuilder version: v2.3.1+
- kubernetes: v1.14+
- go version: v1.13+
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
## Build image
```shell
docker login -u shaloudocker -p sy654321
make docker-build docker-push IMG=shaloudocker/podset:v1
```
部署
```shell
make deploy IMG=shaloudocker/podset:v1
```
## Uninstall And Clean
```shell
make uninstall
```
