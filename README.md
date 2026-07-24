[English](./README.en.md) | **简体中文**

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./assets/hero-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="./assets/hero-light.svg">
  <img alt="Airtap" src="./assets/hero-light.svg" width="880">
</picture>

<p align="center"><sub>一条命令在信创内网 GPU 盒上启动编码 Agent——零云上出口、零 SSH 隧道拼装、全量出口调用留痕可审。数据不出境的国企研发，第一次像用云端 runner 一样顺手。</sub></p>

<p align="center">
  <a href="./LICENSE"><img alt="license" src="https://img.shields.io/github/license/SuperMarioYL/airtap?color=blue"></a>
  <a href="https://github.com/SuperMarioYL/airtap/releases"><img alt="release" src="https://img.shields.io/github/v/release/SuperMarioYL/airtap?include_prereleases"></a>
  <a href="https://github.com/SuperMarioYL/airtap/actions/workflows/ci.yml"><img alt="ci" src="https://img.shields.io/github/actions/workflow/status/SuperMarioYL/airtap/ci.yml?branch=main&label=ci"></a>
  <img alt="go" src="https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white">
</p>

> **把 6 步 SSH + tmux + 端口转发压成一条命令，让数据不出境的国企研发第一次像用云端 runner 一样顺手。**

<h2><img src="https://api.iconify.design/tabler:topology-star-3.svg?color=%237C3AED&width=24" align="top" alt=""> 架构</h2>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./assets/atlas-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="./assets/atlas-light.svg">
  <img alt="Airtap 架构" src="./assets/atlas-light.svg" width="880">
</picture>

两个进程、各一个二进制：笔记本上的 `airtap` 客户端，与信创内网 GPU 盒上的 `airtapd` 守护进程。客户端经 mTLS（tcp `:7437`）与守护进程建立一条隧道，同一条连接既跑 agent 的工具调用流，也回传 TUI 流。Agent 在盒上本地运行——`read` / `write` / `list` / `bash` 工具直接操作盒上仓库，模型调用走盒内 `127.0.0.1`；egress allowlist 代理是盒上唯一的网络出口，任何不在 `egress.allow` 里的拨号都被拒绝并写入 `audit.log`。没有 SSH，没有 tmux，没有端口转发，没有云后端。

## 目录

- [架构](#架构)
- [为什么需要 Airtap](#为什么需要-airtap)
- [安装](#安装)
- [快速上手](#快速上手)
- [用法](#用法)
- [演示](#演示)
- [配置](#配置)
- [与云端 runner 的差异](#与云端-runner-的差异)
- [路线图](#路线图)
- [付费与授权](#付费与授权)
- [许可证](#许可证)
- [贡献](#贡献)

<h2><img src="https://api.iconify.design/tabler:bulb.svg?color=%237C3AED&width=24" align="top" alt=""> 为什么需要 Airtap</h2>

信创 / 国企研发要在内网 GPU 盒（昇腾 910C / 海光 DCU）上跑编码 Agent，因为《数据安全法》与等保 2.0 要求 prompt 和代码上下文不得离开企业内网。现实是：SSH 进每台盒子、手工拼端口转发让笔记本够到 agent 的 HTTP 面、tmux 守会话、断线重连全套再来一遍，而且合规团队看不到"到底什么流量出了网"。Airtap 把 SSH + tmux + 隧道 + 逐盒配置压成一份声明式 `airtap.yaml` 契约：守护进程拒绝任何不在 `egress.allow` 里的出站拨号，每一次拨号（允许或拒绝）都追加进 `audit.log`。云端 runner（Harness IDE / Reachpad）假设后端云可达，数据不出境在架构上禁掉这个假设——所以这不是"难用"，是云 runner 结构上服务不了这个面。

<h2><img src="https://api.iconify.design/tabler:download.svg?color=%237C3AED&width=24" align="top" alt=""> 安装</h2>

```bash
# macOS（Homebrew）
brew install SuperMarioYL/tap/airtap

# Linux（直接下载二进制）
curl -L https://github.com/SuperMarioYL/airtap/releases/latest/download/airtap-linux-amd64.tar.gz | tar xz
sudo mv airtap /usr/local/bin/

# 或从源码
go install github.com/SuperMarioYL/airtap/cmd/airtap@latest
```

Gitee 镜像：`https://gitee.com/SuperMarioYL/airtap`（CN 网络优先）。需要 Go 1.24+。支持国产算力盒上运行 `airtapd`（Linux 昇腾 / 海光）；笔记本侧客户端支持 Linux 与 macOS，Windows 暂不在 v0.1 范围。

<h2><img src="https://api.iconify.design/tabler:rocket.svg?color=%237C3AED&width=24" align="top" alt=""> 快速上手</h2>

三条命令从冷启动到第一条 agent 流。`airtapd` 在每台盒子上只部署一次，之后永久复用。

```bash
# 1. 在笔记本上初始化（生成 airtap.yaml + mTLS CA / 客户端证书）
airtap init --box 10.0.0.5:7437

# 2.（一次性）把守护进程和 CA 拷到 GPU 盒并启动，监听 :7437 mTLS
scp airtapd ca.pem ops@gpu-box:/opt/airtap/ && ssh ops@gpu-box '/opt/airtap/airtapd &'

# 3. 连接 → 跑 agent → 看出口审计
airtap connect
airtap run "给 server.go 加一个 /health 接口"
airtap audit
```

<details><summary>示例输出</summary>

```
$ airtap connect
connected to 10.0.0.5:7437 (mTLS, ca=sha256:9f3a…) ✓

$ airtap run "给 server.go 加一个 /health 接口"
▸ reading /opt/proj/server.go            (tool: read)
▸ calling model deepseek-v3 @ 127.0.0.1:8000   (egress: allow)
▸ writing /opt/proj/server.go            (tool: write)
✓ done — 1 file changed, +9 −0

$ airtap audit
2026-07-24T10:11:02  ALLOW  127.0.0.1:8000  deepseek-v3  model call
# 全程只有这一条出站记录——零其他拨号
```

</details>

<h2><img src="https://api.iconify.design/tabler:terminal-2.svg?color=%237C3AED&width=24" align="top" alt=""> 用法</h2>

五个最常见的工作流。完整示例见 [`examples/`](./examples)。

```bash
# 切换盒上国产模型（DeepSeek-V3 / Qwen3-Coder / GLM-4.6，OpenAI 兼容端点）
airtap run --model qwen3-coder "把 /health 改成带就绪探针"

# 指定工作目录与允许的工具子集
airtap run --workdir /opt/proj/svc-a --tools read,bash "跑一下 go test ./..."

# 只跑一次连通性自检，不启动 agent
airtap connect --check

# 把出口审计导出给 SIEM（append-only，OEM tier 加 hash chain）
airtap audit --since 24h --json > audit-24h.json

# 在新盒子上重新签发客户端证书（旧证书吊销后）
airtap init --box 10.0.0.6:7437 --rotate
```

`airtap run "<prompt>"` 会在盒上跑一个最小 ReAct 循环：系统提示 → 调盒上模型 → 工具分发（`read` / `write` / `list` / `bash`）→ 循环到完成；只有工具结果与 diff 会过隧道回传到 TUI，模型拨号永远停在 `127.0.0.1`。

<h2><img src="https://api.iconify.design/tabler:photo.svg?color=%237C3AED&width=24" align="top" alt=""> 演示</h2>

![demo](assets/demo.gif)

90 秒演示：`airtap init` → `airtap connect` → `airtap run "给 server.go 加一个 /health 接口"` → `airtap audit`。演示在 QEMU 昇腾模拟器（CPU mock 模型）上录制，`assets/demo.cast` 是 asciinema 源，`assets/demo.gif` 是渲染产物，由 `docs/demo.tape` + `.github/workflows/demo.yml` 在发版时自动重渲。

<h2><img src="https://api.iconify.design/tabler:settings.svg?color=%237C3AED&width=24" align="top" alt=""> 配置</h2>

核心是 [`EgressManifest`](./examples/airtap.yaml) ——一份 `airtap.yaml` 把原本散在 SSH config、tmux 脚本、逐盒 env 里的四件事绑成一条契约：

| 键 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `box.addr` | string | 必填 | GPU 盒地址 `host:port` |
| `box.tls` | string | `mTLS` | 隧道鉴权方式（v0.1 仅 mTLS） |
| `box.ca` | string | `./ca.pem` | CA 证书路径 |
| `model.endpoint` | string | 必填 | 盒上模型端点（OpenAI 兼容，`127.0.0.1`） |
| `model.name` | string | 必填 | 模型名：`deepseek-v3` / `qwen3-coder` / `glm-4.6` |
| `egress.allow` | []string | 必填 | 允许拨号的目标白名单 |
| `egress.mode` | string | `airgap` | `airgap` = 拒绝一切不在 `allow` 里的出站 |
| `egress.audit` | string | `./audit.log` | 审计日志路径（append-only） |
| `agent.workdir` | string | 必填 | 盒上工作目录 |
| `agent.tools` | []string | `[read,write,list,bash]` | 启用的工具 |

守护进程把这份 manifest 当**契约**执行：不在 `egress.allow` 里的出站一律拒绝，允许与拒绝都写 `audit.log`。这不是配置文件，是云 runner 结构上做不到的 airgap + 审计原语。

<h2><img src="https://api.iconify.design/tabler:arrows-diff.svg?color=%237C3AED&width=24" align="top" alt=""> 与云端 runner 的差异</h2>

| 能力 | Airtap | Harness IDE / Reachpad | RelayBar |
| --- | :---: | :---: | :---: |
| 数据不出境 / airgap 出口 | ✓ | — | partial（只转发，不强制内网） |
| 出站审计 trail | ✓ | — | — |
| agent 生命周期（起 / 跑 / 停） | ✓ | ✓ | — |
| 信创国产算力（昇腾 / 海光） | ✓ | — | — |
| 一键安装 / 零后端运维 | partial（要拷一次 airtapd） | ✓（云托管） | ✓ |

诚实说：在"云端托管、零运维"这一项上 Harness IDE / Reachpad 更省心——它们的后端是云托管的，Airtap 要在盒上放一个守护进程。代价换来的是 airgap 出口 + 审计 + 信创面，这三样是云 runner 架构上给不了的。

<h2><img src="https://api.iconify.design/tabler:map-2.svg?color=%237C3AED&width=24" align="top" alt=""> 路线图</h2>

**v0.1.0（已发布）**

- [x] `airtap.yaml` 解析 + 校验，`airtap init` 签发 mTLS CA / 客户端证书
- [x] `airtapd` 在 :7437 接受 mTLS，`airtap connect` 回显连接
- [x] 盒上 ReAct agent 循环 + egress allowlist 代理 + `airtap run` TUI 流
- [x] `airtap audit` 渲染 append-only trail，录制 `assets/demo.cast`

**下一步**

- [ ] v0.2：agent-plugin spec（适配 Aider / Cline / Continue，内置最小循环之外）
- [ ] v0.3：多盒 / fleet 调度（一份 manifest 对一盒 → 一对多）
- [ ] hash-chained `audit.log`（OEM tier，防篡改）
- [ ] GPU / NPU 自动发现（当前端点手填）
- [ ] Windows 笔记本客户端（v0.1 仅 Linux / macOS）
- [ ] RBAC / 多用户 / SSO（v0.1 单人单盒）

<h2><img src="https://api.iconify.design/tabler:credit-card.svg?color=%237C3AED&width=24" align="top" alt=""> 付费与授权</h2>

Airtap 是**商业产品**，私有化部署授权。二进制对个人评估免费（你可以 clone、跑、看审计），但生产 / OEM 场景需要授权——这不是"以后再说"，是数据不出境客户的采购清单本来就有的项。

**私有化部署 + OEM 授权解锁三项：**

- **OEM branding** —— 把 Airtap logo 换成企业 logo，集成商可贴牌进自有国产化栈
- **审计聚合** —— 多盒 `audit.log` 汇总进企业 SIEM，统一出口留痕视图
- **air-gap egress 策略包** —— 预置国企等保三级合规 allowlist 模板，开箱即过等保

**定价：**

- 试点：¥30,000 / 首个组织 / 12 个月（pilot tier）
- 常年：¥80,000 / 组织 / 年（ongoing）
- 对公转账 + 增值税专用发票（国企采购标准流程；不支持信用卡）

**Hosted SaaS 结构上不提供** —— 数据不出境禁止任何把 prompt / 上下文发到境外端点的路径，包括付款数据。我们用自建极简 license-server（Go，on-prem，离线激活文件 `.lic`），不走 Stripe。

**最小成单路径：** 设计合作伙伴访谈 → 在他们盒上现场 `airtap run "给 server.go 加一个 /health 接口"` 跑通 → 生成 `airtap audit` 报告证明零出口 → IT 负责人拿审计报告走内部采购立项 → ¥30k 对公打款 + 开票 → 邮寄 `.lic` + 离线策略包。

要在你的信创环境里做一次试点评估，见 [`examples/airtap.yaml`](./examples/airtap.yaml) 起一份 manifest，或邮件 `leo.stack@outlook.com`。

<h2><img src="https://api.iconify.design/tabler:license.svg?color=%237C3AED&width=24" align="top" alt=""> 许可证</h2>

MIT — 见 [`LICENSE`](./LICENSE)。个人评估与 OSS 使用免费；生产 / OEM 部署见上面的[付费与授权](#付费与授权)。

<h2><img src="https://api.iconify.design/tabler:heart.svg?color=%237C3AED&width=24" align="top" alt=""> 贡献</h2>

欢迎 issue 与 PR。先开 issue 描述场景与复现路径，再发 PR。在信创 / 国产算力盒上的真实踩坑报告尤其有价值。

<p align=center><sub><a href=./LICENSE>MIT</a> © 2026 SuperMarioYL</sub></p>
