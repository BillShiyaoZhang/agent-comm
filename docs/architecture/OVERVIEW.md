# Agent Comm 代码结构

本仓库提供设备端通信组件、共享 Python 协作 runtime 与宿主适配器。当前安装和运行方式见 [工程指南](../guides/ENGINEERING.md)。

## 目录与责任

| 目录 | 责任 |
| --- | --- |
| `cmd/helper/` | 生产 helper：身份、命令、本机 HTTP/SSE、持久 inbox/outbox |
| `cmd/bootstrap/` | 可运行的 libp2p bootstrap/relay 节点入口 |
| `agent/` | Go SDK 高阶通信与名片、可靠消息接口 |
| `crypto/`、`session/`、`dr/` | 身份/信封、ECIES 会话、Double Ratchet 状态与存储 |
| `libp2p/`、`dht/`、`registry/`、`mq/` | 网络节点、发现、签名目录与加密信箱 |
| `contacts/`、`wot/`、`proto/` | 联系人、信任声明、protobuf 契约和生成代码 |
| `python/` | 可独立打包的宿主无关协作 runtime |
| `connectors/` | Hermes 和 OpenClaw 的宿主适配层 |
| `examples/` | SDK/协议的手工学习示例 |
| `tests/integration/` | 指定测试平台的 HTTP/P2P 验证程序 |
| `tools/` | 注册、发布下载和跨仓隔离进程验证工具 |
| `docs/` | 架构、运行指南、规划与维护依据 |

Go 公共包保持现有路径，避免破坏外部 `github.com/BillShiyaoZhang/agent-comm/...` imports。分层由包职责与子目录表达，不能为减少根目录数量而随意移动公共包。

## 两条消息路径

1. **现役 helper 可靠消息：** 本机 HTTP 接受请求并保存 outbox，后台通过 HTTPS MQ 发送已签名密文；收件方验证、解密并保存 inbox 后 ACK 平台，宿主完成消费后 ACK helper。
2. **Go SDK P2P：** `Agent.SendMessage` 提供独立的 libp2p/Double Ratchet 路径；它和 helper 的持久 MQ 发送契约不同。

ECIES/信封实现使用 X25519、HKDF-SHA256、AES-256-GCM 和 Ed25519 签名；`dr` 的消息加密使用 XChaCha20-Poly1305。不能把旧稿中的 AES-GCM-SIV、完整 Signal/X3DH 协议或所有路径都具备前向保密当作实现事实。

## 仓库分工

Platform 独立维护公共目录、信箱和 Relay，Web 独立维护账户与远程工作台，部署仓库组装它们。本 SDK 的 Python runtime 与连接器共享版本化接口且已有独立包边界；在需要独立发布权限/版本周期之前，无需额外 GitHub 仓库。

构建和验证见 [工程指南](../guides/ENGINEERING.md)，协议入口见 [PROTOCOL](PROTOCOL.md)。
