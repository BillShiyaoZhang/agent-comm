# 协议与源码契约

本页索引当前实现。协议字段和编号以 `proto/*.proto` 为准，签名编码以 SDK 函数为准；修改 `.proto` 后必须同步生成 Go 文件并验证客户端/服务端兼容性。

## 契约入口

| 范围 | 源码 |
| --- | --- |
| 身份与 URN | [crypto/keys.go](../../crypto/keys.go) |
| 注册请求、解析结果 | [proto/registry.proto](../../proto/registry.proto)、[registry/client.go](../../registry/client.go)、[registry/validation.go](../../registry/validation.go) |
| 消息与签名信封 | [proto/agentcomm.proto](../../proto/agentcomm.proto)、[proto/envelope.proto](../../proto/envelope.proto)、[crypto/envelope.go](../../crypto/envelope.go) |
| MQ Store/Retrieve/ACK | [proto/mq.proto](../../proto/mq.proto)、[mq/client.go](../../mq/client.go)、[mq/auth.go](../../mq/auth.go) |
| ECIES 与封装 | [crypto/ecies.go](../../crypto/ecies.go)、[session/session.go](../../session/session.go) |
| Double Ratchet 帧与状态 | [dr/ratchet.go](../../dr/ratchet.go)、[dr/session.go](../../dr/session.go)、[dr/store.go](../../dr/store.go) |
| 本机 helper HTTP/SSE | [Hermes 接口合同](../guides/HERMES_INTEGRATION.md) |
| 协作与远程 RPC | [Python runtime](../../python/README.md) |

## 必须保留的语义

- URN 绑定 Ed25519 身份；Registry 记录的 X25519 公钥和 PeerID 必须通过记录签名校验。调用 `registry.BuildSignedMsg`，不要复制旧文档中的简化拼接公式。
- 信封签名绑定发件人、收件人、消息 ID 和加密字段。以同一消息 ID 重试时保留原始已签名密文。
- Retrieve/ACK 受收件人身份约束。HTTP ACK 是签名请求，旧匿名 ACK 已不可用。
- `accepted` 表示本机持久接纳，`platform_queued` 表示平台入队；本机 ACK 表示消费确认。它们不是业务任务完成证明。
- helper 的 HTTPS MQ 使用静态 X25519 信封，`dr` 是独立的状态化 P2P 协议。两者的加密算法和状态生命周期不能混写。

## 验证

现役 Go 测试与 [集成验证入口](../../tests/README.md) 检查签名篡改、跨身份操作、消息去重与持久投递。旧 Phase 草稿中的数据布局、缺少 X25519 的签名公式、无签名注册和已移除 CLI 命令不再作为契约保留。
