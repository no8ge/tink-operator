# tink-operator

一个基于 Kubernetes Operator 的自动化测试平台，用于管理测试任务的执行和结果收集。

## Description

tink-operator 是一个 Kubernetes Operator，提供了两种自定义资源类型来管理自动化测试：

- **Runner**: 一次性测试任务，基于 Kubernetes Job 实现，适用于单元测试、集成测试等短期任务
- **Executor**: 长期运行的测试服务，基于 Kubernetes Deployment 实现，适用于性能测试、监控测试等持续运行的任务

该 Operator 集成了 MinIO 客户端，支持自动上传测试结果和报告到 S3 兼容的存储系统，并提供完整的生命周期管理和状态监控。

## 核心特性

- **双模式支持**: Runner（一次性任务）和 Executor（长期服务）
- **自动结果收集**: 集成 MinIO 客户端，自动上传测试结果到 S3 存储
- **状态监控**: 实时监控任务状态和报告上传进度
- **Kubernetes 原生**: 基于 Job 和 Deployment，充分利用 K8s 能力
- **灵活配置**: 支持自定义存储配置和报告路径

## 使用示例

### Runner 示例（一次性测试任务）
```yaml
apiVersion: autotest.atop.io/v1alpha1
kind: Runner
metadata:
  name: unit-test-runner
spec:
  parallelism: 1
  completions: 1
  backoffLimit: 3
  ttlSecondsAfterFinished: 300
  template:
    spec:
      containers:
      - name: test-runner
        image: golang:1.21
        command: ["go", "test", "./..."]
        volumeMounts:
        - name: shared-data
          mountPath: /data
      volumes:
      - name: shared-data
        emptyDir: {}
  artifacts:
    enabled: true
    path: /data
  storage:
    endpoint: "minio.example.com:9000"
    bucket: "test-results"
    prefix: "unit-tests"
    accessKey: "minioadmin"
    secretKey: "minioadmin"
```

### Executor 示例（长期运行服务）
```yaml
apiVersion: autotest.atop.io/v1alpha1
kind: Executor
metadata:
  name: performance-test-executor
spec:
  replicas: 2
  selector:
    matchLabels:
      app: performance-test
  template:
    metadata:
      labels:
        app: performance-test
    spec:
      containers:
      - name: performance-test
        image: k6:latest
        command: ["k6", "run", "/scripts/load-test.js"]
        volumeMounts:
        - name: shared-data
          mountPath: /data
      volumes:
      - name: shared-data
        emptyDir: {}
  artifacts:
    enabled: true
    path: /data
  storage:
    endpoint: "minio.example.com:9000"
    bucket: "test-results"
    prefix: "performance-tests"
    accessKey: "minioadmin"
    secretKey: "minioadmin"
```

## Getting Started

### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/tink-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/tink-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/tink-operator:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/tink-operator/<tag or branch>/dist/install.yaml
```

## Contributing

我们欢迎社区贡献！如果您想为 tink-operator 项目做出贡献，请遵循以下步骤：

### 开发环境设置
1. Fork 本仓库到您的 GitHub 账户
2. 克隆您的 fork 到本地开发环境
3. 确保安装了所有必要的依赖（Go 1.21+, Docker, kubectl）
4. 运行 `make help` 查看所有可用的 make 目标

### 贡献流程
1. 创建功能分支：`git checkout -b feature/your-feature-name`
2. 进行代码修改并添加测试
3. 运行测试：`make test`
4. 确保代码通过 lint 检查：`make lint`
5. 提交更改：`git commit -m "Add your feature"`
6. 推送到您的 fork：`git push origin feature/your-feature-name`
7. 创建 Pull Request

### 代码规范
- 遵循 Go 语言标准格式
- 添加适当的注释和文档
- 为新功能编写测试用例
- 确保所有测试通过

### 报告问题
如果您发现 bug 或有功能请求，请在 GitHub Issues 中创建新的 issue，并提供详细的描述和复现步骤。

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

