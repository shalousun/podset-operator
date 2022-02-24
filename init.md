在linux系统上新建目录`Pod`。
```shell
go mod init pod-set
kubebuilder init --domain com.shalousun
```
创建CRD
```shell
kubebuilder create api \
--group data.clond \
--version v1 \
--kind PodSet
```