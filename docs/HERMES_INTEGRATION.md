# Hermes 接入：通信合同与交接

本次实现提供 Hermes 所需的身份验证、持久消息、确认和会话关联接口。Hermes 仓库及真实 profile 不在本次部署范围；本仓库的 Hermes 插件已完成适配，后续任务负责安装配置和实际模型回合验收。

## 职责与启动顺序

1. **服务器 platform**：更新配套 SDK 子模块，重建并重启服务。具体命令、备份和兼容性见 `agent-comm-platform/HERMES_UPGRADE.md`。
2. **本机 helper**：保留已有身份目录，使用本次源码构建的二进制。启动 `agent-comm-helper daemon <keys_dir绝对路径> <云端HTTPS地址> [本机端口]`；默认本机端口 45042。每个身份只运行一个 helper，每个 inbox 只接一个活跃消费者。
3. **Hermes 插件**：按 [插件安装说明](../connectors/hermes-platform/README.md) 安装到运行 Gateway 的 Python 环境或实际用户插件目录，二选一。配置 `platform_url=http://127.0.0.1:45042`，明确通信对象的 URN；重启 Gateway 后验证真实 SSE 连接。

helper 主动通过 HTTPS 注册/解析身份和收发 MQ，启动时立即补拉，之后按 5 秒周期轮询，不要求云端反向连接 Windows。平台暂不可达时 helper 仍可启动、接收本机出站请求并等待重试。SDK 配置 `PlatformHTTPURL` 后，新的 `DeliverEnvelope` 和 `PollMessages` 使用 HTTP MQ；未配置时使用 libp2p MQ。传统 `SendMessage` 仍优先 DR/P2P，失败后通过 libp2p MQ 回退。

可靠发送通过持久 MQ。SDK 传统 `SendMessage` 的 DR/P2P 路径仍保留；可靠 listener 拒绝缺少稳定 ID 的旧 DR 入站流，让发送者回退到已签名 MQ 信封。签名直接信封在持久回调完成后才返回接收确认。

SDK 调用方使用 `PrepareMessage(ctx, recipientURN, plaintext, messageID)` 准备信封，先持久保存，再调用 `DeliverEnvelope(ctx, envelope)`；重试复用保存的完整信封。`StartListeningDurable(ctx, handler)` 的 handler 接收信封和解密后的 `ChatMessage` protobuf 字节；返回 nil 必须表示入站已持久接纳。关闭时先取消 ctx，再等待此方法返回的 done channel，最后关闭 inbox 数据库。传统 `StartListening` 保留无返回值回调，不能提供同样的应用落盘确认。

## 本机 HTTP 接口

接口位于 loopback，只供本机受信任进程调用，携带明文和本地消费权限。不要把它公开到外网。云端同名 `/api/v1/mq/*` 使用签名 protobuf 密文，不能用替换 URL 的方式直连。

| 方法与路径 | 请求/结果 |
| --- | --- |
| `GET /info` | 本机 `urn`、`peer_id`、`status` |
| `POST /api/v1/mq/store` | 请求见下方；返回 HTTP 202、`success:true`、稳定 `message_id` 和当前发送 `status` |
| `GET /api/v1/mq/status?message_id=...` | `message_id`、`status`、`attempts`、`last_error`；未知 ID 返回 404 |
| `GET /api/v1/mq/retrieve` | `{"messages":[...]}`，列出全部未本地 ACK 的入站消息 |
| `GET /api/v1/mq/subscribe` | SSE；`id: <message_id>`、`data: <入站JSON>`，连接后及每 5 秒补推未消费消息 |
| `POST /api/v1/mq/ack` | `{"message_ids":["..."]}`；返回 `success`、本次 `acked` 数量。重复 ACK 成功但数量为 0 |

出站示例：

```json
{
  "recipient_urn": "urn:agent-comm:agent:PEER_FINGERPRINT",
  "message_id": "request-unique-001",
  "text": "请检查任务输入",
  "conversation_id": "conversation-001",
  "task_id": "task-001",
  "kind": "task",
  "deadline": "2027-01-01T00:00:00Z",
  "hop_limit": 8
}
```

入站包含相同关联字段，`recipient_urn` 替换为经过验证的 `sender_urn`。`in_reply_to` 可指向上一条 wire ID。文本最多 262144 UTF-8 字节；conversation/task/kind/in_reply_to 各最多 256 字节；本机 message ID 为 1–128 个 ASCII 字母、数字或 `._:-`；hop_limit 范围 0–64，默认 8；kind 默认 `message`；deadline 使用 RFC3339。同 ID、同请求返回原状态；同 ID、不同内容返回 409。省略 message_id 时 helper 生成 ID；需要跨请求重试的调用方应自行保留稳定 ID。

SSE 的 `Last-Event-ID` 不是消费确认。连接恢复后会补推全部未 ACK 消息；消费者必须去重并在完成或持久接纳后显式 ACK。长任务期间重复事件是正常现象。

## 持久性和状态含义

helper 在 `<keys_dir>/mailbox.db` 中保存 inbox、outbox 及消费记录，SQLite 使用 WAL 和 FULL 同步。出站加密成功后先持久保存完整信封，再向平台发送；网络响应丢失或重启后复用相同 ID、签名和密文。失败按指数间隔重试，最大间隔 256 秒；没有 deadline 的请求持续重试。

| 状态/确认 | 能证明的事实 |
| --- | --- |
| `accepted` | 本机出站请求已落盘；尚未证明平台接纳 |
| `platform_queued` | 平台持久 MQ 已确认相同 ID；未证明对端已处理 |
| `expired` | helper 发现发送 deadline 已经过期，停止投递 |
| 平台 ACK | 收件 helper 已验证并持久保存入站 |
| 本机 ACK | 消费插件已确认处理该入站；不是发送者可查询的业务状态 |
| 带 `in_reply_to` 的 `kind=result` 消息 | 应用层回复；具体任务成功含义由 Hermes/调用方约定 |

目前没有独立的端到端任务状态查询、取消或进度服务。不能将 `accepted` 或 `platform_queued` 展示成“对方任务完成”。deadline 在发送前和插件执行前检查；已被平台接纳的消息不会因状态查询自动撤回。

入站只有在完整验证、解密和 inbox 提交成功后才 ACK 平台。Hermes 使用真实 `on_processing_complete` 钩子，先持久记录完成 receipt，再 ACK helper；仅 `handle_message` 返回不触发 ACK。重启或丢 ACK 后已完成消息只补 ACK，不重复调度。外部工具已产生副作用、但完成 receipt 尚未落盘时发生崩溃，仍可能重做；业务操作需要使用 `task_id`/`message_id` 作为幂等键。

平台信封默认 TTL 为 7 天；平台历史清理会限制其去重保留期。未读队列满时平台拒绝新存储并返回 HTTP 429，让 helper 重试，不淘汰已经接纳的未读消息。helper 和 Hermes 完成记录用于更长时间的幂等性，因此升级不要清空这些数据库。当前 helper inbox/outbox 没有自动清理策略，运营方需关注磁盘用量。

## 安全协议升级

- HTTP retrieve/subscribe 的签名公钥必须对应收件 URN。ACK 对原始 JSON 请求体签名并携带 recipient_urn 和 timestamp。
- libp2p MQ 从加密连接取得对端公钥，在共享存储层检查同样的收件和 ACK 权限。
- `EncryptedEnvelope` 增加 `recipient_urn`、`sender_ed25519_pubkey` 和 `signature`。Ed25519 签名绑定完整信封，包括 sender/recipient/message ID、X25519 公钥和密文；接收端验证成功后才使用 sender 身份。
- HTTPS MQ 密文采用静态 X25519 共享密钥与 AES-GCM，不具备 Double Ratchet 的前向安全属性。签名解决身份和消息绑定，不把它等同于 DR。
- 新平台和接收 SDK 拒绝没有签名的旧信封，不能替旧消息推断发送身份。上线前处理完旧队列，或保留备份后由原发送者重发；所有收发客户端一起升级。
- 原始 CLI `encrypt-envelope` 现需 `<keys_dir> <recipient_urn> <recipient_pubkey_hex> <plaintext_hex> <message_id>`，返回 `envelope_proto_hex`；`decrypt-envelope` 只接受 `<keys_dir> <envelope_proto_hex>`。旧字段参数不再接受。Hermes 插件不应调用这些密码学 CLI。
- 旧 SDK `BuildEnvelope(pubkey, plaintext)` 调用仍能编译，但会明确报错；需补第三个 `recipientURN` 参数，或改用带稳定 ID 的 `BuildEnvelopeForRecipient(recipientURN, pubkey, plaintext, messageID)`。旧注册结果缺少有效身份签名时也不再用于 Agent 的消息发送。

## Hermes 后续任务

- 确认实际 `HERMES_HOME` 和 Gateway 使用的 Python，安装本仓库插件及其依赖。Windows 主目录可能在 `%LOCALAPPDATA%\hermes`，不要套用默认 Unix 路径。
- 保留原身份，确认 helper `/info` 返回预期 URN；配置 loopback URL、profile 独立的 receipts 路径、明确的 `allow_from` 对端 URN。旧插件曾自动写入 pairing 的授权需按实际意图检查。
- 确认外来消息 `is_bot=True`、`allow_gateway_control=False`，仅获得通信许可；不得因联系人信任自动获得工具执行或用户审批权限。
- conversation_id 映射独立 Hermes thread，没有 conversation_id 时使用 task_id；均没有时使用对端 DM。不会自动共享当前桌面/CLI transcript。
- 用两个隔离身份跑一次有停止条件的真实模型回合，检查关联回复、SSE 断开恢复、处理期间重启、重复消息、hop limit 和工具副作用幂等。

不需要修改 Hermes 核心。A2A、Relay contract、HTTP runs 控制台、房间和平台聊天 UI 属于后续产品能力，不是此次通信接入的前置条件。

## 验证

```sh
go test ./...
go build -o build/agent-comm-helper ./cmd/helper
# 在已安装 Hermes 的环境中，按插件 README 设置 PYTHONPATH
python -m unittest discover -s connectors/hermes-platform/tests -v
# platform 与配套 SDK 构建后，运行真实本机进程测试
python tools/test_helper_platform.py --helper build/agent-comm-helper --platform /path/to/platform
```

真实本机进程测试创建独立 platform、两个 helper 和临时身份，覆盖双向身份验证收发、会话/任务字段、无消费者入站、helper 崩溃恢复、平台离线时出站持久化、平台恢复后重试、收件 helper 离线后的补收。它使用 loopback HTTP，不连接生产域名，也不调用模型。
