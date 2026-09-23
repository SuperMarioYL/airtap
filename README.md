[English](./README.en.md) · [Website](https://airtap.lei6393.com) · [GitHub](https://github.com/SuperMarioYL/airtap)

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/hero-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/hero-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/hero-dark.svg">
  <img src="./assets/presentation/hero-light.svg" width="960" alt="Hero diagram">
</picture>

# airtap

**让远程 Agent 在自己的计算节点上工作。**

Airtap 通过双向 TLS 连接客户端与 airtapd。守护进程按配置调用模型，并在指定工作目录执行工具循环。

v0.7.0 将运行上下文接入模型请求（断开即停，包括进行中的模型调用）、把外部 Agent（Aider）的运行输出流回客户端，并修复发布流水线使预编译二进制随每个版本发布。Aider 运行仍需要相应环境和依赖；下面的初始化示例不启动 Agent。

## 为什么需要它

远程开发节点需要明确模型端点、工具集合和允许访问的地址。一份共享 YAML 把这些设置集中起来，出口决策也能从日志检查。

- **一份共享配置** — 模型、工作目录、工具和出口在同一处定义。
- **双向认证连接** — 客户端与守护进程通过 mTLS 通信。
- **检查出口决策** — 已配置拨号器记录允许和拒绝的尝试。

## 架构

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/architecture-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-dark.svg">
  <img src="./assets/presentation/architecture-light.svg" width="960" alt="Architecture diagram">
</picture>

客户端读取配置并建立 mTLS 隧道；airtapd 分发已配置工具，模型请求经过出口拨号器。拨号器按 host:port 检查允许列表，记录允许和拒绝的尝试。Bash 隔离依赖 Linux 网络命名空间。

| 组件 | 职责 |
| --- | --- |
| `Thin client` | cmd/airtap |
| `mTLS tunnel` | internal/tunnel |
| `Agent loop` | internal/agent |
| `Egress + audit` | internal/egress; internal/audit |

## 安装与快速上手

使用仓库清单指定的运行时版本构建，并在仓库根目录运行示例。

```bash
git clone https://github.com/SuperMarioYL/airtap.git
cd airtap
go build ./cmd/airtap
go build ./cmd/airtapd
```

没有 Go 工具链时，可用预编译二进制（v0.7.0 起，linux/darwin × amd64/arm64，压缩包内含 `airtap` 与 `airtapd`）：

```bash
curl -L https://github.com/SuperMarioYL/airtap/releases/latest/download/airtap_linux_amd64.tar.gz | tar xz
```

随仓示例校验 examples/airtap.yaml，并比较本地模型地址与未列出的地址，不实际拨号。

```bash
go run ./examples/presentation-demo
```

## 实际运行示例

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/process-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/process-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/process-dark.svg">
  <img src="./assets/presentation/process-light.svg" width="960" alt="Process diagram">
</picture>

The local model address is allowed; example.com:443 is not.

```text
manifest: valid; model=deepseek-v3; tools=[read write list bash]
127.0.0.1:8000 allowed=true
example.com:443 allowed=false
```

完整命令与输出保存在 [docs/demo-results.json](./docs/demo-results.json). 输入和复现代码均随仓提供。

## 用法

CLI 提供以下操作。示例之外的命令需要替换成你的文件路径或标识。

```bash
mkdir -p local-config
go run ./cmd/airtap init --out ./local-config
go run ./cmd/airtap run --manifest ./airtap.yaml "Inspect this repository"
go run ./cmd/airtap audit --file ./audit.log
```

## 配置

init 前先创建输出目录。将配置和 TLS 材料部署到两端，妥善保护 ca.key，并根据环境设置 box.addr、model.endpoint、egress.allow 和 agent.workdir。agent.max_iterations 为 0 时采用默认上限。

## 集成与职责分工

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/integrations-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-dark.svg">
  <img src="./assets/presentation/integrations-light.svg" width="960" alt="Integrations diagram">
</picture>

以下路径已有源码实现。按任务选择输入，并把生成的结果与项目一起保存。

| 路径 | 已实现职责 |
| --- | --- |
| YAML | Model, tools and box settings |
| mTLS | Client / daemon stream |
| OpenAI-compatible HTTP | Configured model endpoint |
| Audit log | Outbound dial decisions |

## 限制与后续方向

- 离线示例只检查配置和策略匹配，不启动守护进程、不调用模型，也不验证 Linux 隔离。
- Bash 执行需要 Linux 网络命名空间及相应权限；无法创建隔离时会拒绝执行。
- 拨号器管理经过它的流量，不构成合规认证，也不是无关进程的系统防火墙。

后续方向包括目标节点的部署验证与更多 Agent 适配器，需要分别进行集成测试。

## 许可与贡献

许可见 [LICENSE](./LICENSE). 反馈问题时请提供最小输入、执行命令和实际输出。
