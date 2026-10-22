# AmongClusters

**连接你并不控制的集群。**

AmongClusters 是面向 BYOC 场景的主权集群对等控制面。每个 Kubernetes
集群继续拥有自己的 API Server、CA、RBAC、调度与生命周期权威。Hub 只负责
分发签名身份、信任包、网关端点和显式服务声明，不接收成员集群凭据，也不在
默认应用数据路径中。

[English](README.md) · [依赖与责任边界](docs/DEPENDENCIES.md) ·
[场景矩阵](docs/SCENARIO-MATRIX.md) · [安全政策](SECURITY.md) ·
[贡献指南](CONTRIBUTING.md) · [发布制品清单](artifacts.json)

## 核心边界

- Agent 的 Ed25519 私钥只保留在所有者集群。
- 邀请与租户绑定、短期有效且只能使用一次；Hub 只保存其哈希。
- 只有双方确认对方信任包摘要后，Peer 才能进入可用状态。
- Gateway 使用 TLS 1.3 双向认证 QUIC，只接受显式安装的信任包与绑定身份。
- 仅发布经过策略允许的 L4/L7 服务，不同步 PodCIDR、EndpointSlice、Pod 地址或
  远程 API Secret。
- `ManagedAccessGrant` 是默认关闭的独立能力，不由健康的对等连接隐式授予。

## 独立组件标准

本仓库是 AmongClusters 产品源码、构建、测试与发布的唯一权威。上层 GitOps
仓库只能消费不可变发布制品，不应复制产品源码或成为隐藏运行时依赖。

官方发布包含：

- `linux/amd64` 与 `linux/arm64` 运行镜像；
- Hub、Agent、Headlamp 和 umbrella 四个 OCI Helm Chart；
- 只读 Headlamp 插件归档；
- keyless 签名、构建 provenance、SBOM 与校验和。

`artifacts.json` 是机器可读的发布表面清单，CI 会校验所有 Chart 版本、源码入口、
安全自动化和发布工作流是否一致。公共 DNS、Gateway Listener、NAT、BGP、集群
生命周期、租户身份与远程 Kubernetes 管理仍由各自组件和所有者负责。

## 本地验证

```sh
go test ./...
go vet ./...
python3 scripts/validate_artifacts.py
```

Chart 渲染与 Headlamp 构建的完整命令见 [贡献指南](CONTRIBUTING.md)。
