# Helper 接口与调试

用于初始化、宿主接入或排查收发；普通 Hermes 个人协作使用插件工具。以下 `<...>` 参数都替换为真实配置。helper 启动命令中的 platform 是云端地址；宿主调用的 helper URL 是本机地址，二者不同。

## 身份与启动

```text
agent-comm-helper init <keys_dir绝对路径>
agent-comm-helper daemon <keys_dir绝对路径> <platform_url> [local_port]
```

`init` 加载已有身份，目录不存在身份时创建；返回 `urn`、`peer_id`、`ed25519_pubkey`、`x25519_pubkey`。先检查现有配置并展开 `~`；不要为查看身份换目录生成新身份。`GET /info` 返回 `urn`、`peer_id`、`addrs`、`status`，不含公网 platform URL。helper 默认监听 `127.0.0.1:45042`。启动时补收，之后每 5 秒补拉；云端暂不可达仍可启动并排队重试。

## 通信联系人

对本机 helper `POST /api/v1/contacts`，JSON 支持：

| 字段 | 含义 |
| --- | --- |
| `urn` | 通信对象 URN；兼容名 `contact_urn` |
| `x25519_pk` | 32 字节公钥的 hex；兼容名 `x25519_public_key` |
| `ed25519_pk` | Ed25519 公钥 hex；兼容名 `ed25519_public_key` |
| `peer_id` | libp2p peer ID；省略时须有 Ed25519 公钥可推导 |
| `display_name` | 显示名；兼容名 `alias` |
| `trusted`、`trust_tier` | 本机通信信任标记；`self`/`family`/`friend` tier 可映射为 trusted |
| `addrs` | 可选 multiaddr 字符串数组，写入 peerstore |

此接口成功返回 `{"success":true}`。从已核实的来源取得一致的 URN、公钥和 peer ID；写入成功不构成人与网络身份关系的认证。它不建立 runtime 的主人确认记录，也不导入完整名片文本。HTTP 不提供 GET 列表、删除或独立信任编辑；这些属于 [Go contacts.Store](../contacts/contact.go)。

## 可靠 HTTP 消息

所有地址相对于**本机 helper URL**。用 JSON 请求头 `Content-Type: application/json`。

| 方法/路径 | 请求或结果 |
| --- | --- |
| `POST /api/v1/mq/store` | 下方消息 JSON；HTTP 202，返回 `success`、`message_id`、`status` |
| `GET /api/v1/mq/status?message_id=...` | `message_id`、`status`、`attempts`、`last_error`；未知 ID 为 404 |
| `GET /api/v1/mq/retrieve` | `{"messages":[...]}`，全部未本地 ACK 入站 |
| `GET /api/v1/mq/subscribe` | SSE：`id: <message_id>`、`data: <入站JSON>`；重连和每 5 秒补推未消费消息 |
| `POST /api/v1/mq/ack` | `{"message_ids":["request-001"]}`；返回 `success`、本次 `acked` 数量；重复 ACK 数量为 0 |

```json
{
  "recipient_urn": "urn:agent-comm:agent:PEER_ID",
  "message_id": "request-001",
  "text": "请检查任务输入",
  "conversation_id": "conversation-001",
  "task_id": "task-001",
  "kind": "task",
  "in_reply_to": "previous-message-id",
  "deadline": "2027-01-01T00:00:00Z",
  "hop_limit": 8
}
```

使用任务的实际截止时间；无需关联的可选字段可省略。`text` 最多 262144 UTF-8 字节；conversation/task/kind/in_reply_to 各最多 256 字节；本机 message ID 为 1–128 个 ASCII 字母、数字或 `._:-`；`hop_limit` 为 0–64，默认 8；`kind` 默认 `message`；deadline 为 RFC3339。同 ID、同请求复用原状态；同 ID、不同内容返回 409。可省略 message_id 让 helper 生成，但跨请求重试要自行保留稳定 ID。

入站的 `sender_urn` 已通过传输验证，保留关联字段。先去重、处理或写入持久队列，再 ACK 本机 helper。平台 ACK 由 helper 在验证/解密并持久保存 inbox 后处理。不要因 SSE 收到、开始处理、`Last-Event-ID` 或模型调用返回就提前确认。状态与崩溃恢复细节见 [通信合同](../docs/guides/HERMES_INTEGRATION.md)。

## 原始密码学 CLI

只有自定义客户端或诊断才需要下列命令；helper daemon 和 Hermes 插件正常收发已封装这些工作。

```text
agent-comm-helper sign-retrieve <keys_dir> <recipient_urn> <unix_timestamp_seconds>
agent-comm-helper sign-store <keys_dir> <raw_body_hex>
agent-comm-helper encrypt-envelope <keys_dir> <recipient_urn> <recipient_x25519_pubkey_hex> <plaintext_hex> <message_id>
agent-comm-helper decrypt-envelope <keys_dir> <envelope_proto_hex>
```

签名命令返回 `signature`、`pubkey`。`encrypt-envelope` 返回绑定收发 URN 和稳定 ID 的已签名完整信封及 `envelope_proto_hex`。`decrypt-envelope` 接受完整 protobuf hex，验证后返回 `plaintext`、`sender_urn`、`recipient_urn`、`message_id`。旧的单公钥加密参数和分散 ciphertext/nonce/tag 解密参数已不接受。不要自己拼无签名信封或省略身份绑定；完整云端 HTTP 合同见 [通信合同](../docs/guides/HERMES_INTEGRATION.md) 与 [main.go](../cmd/helper/main.go)。

## 不启动宿主的单句导出

已安装 runtime 时，可用 reference CLI 在 stdout 导出一句话；省略 `--export-contact` 后的 ID 表示 self：

```text
python -m agent_comm_runtime.reference --state <collaboration.sqlite3> --agent-urn <own_urn> --platform-url <actual_platform_url> --export-contact [contact_id]
```

好友只从该 reference 宿主的已确认联系人中读取，不冒用其它宿主的 owner 身份。Hermes 使用原生工具 `export_contact` 及 `extra.public_platform_url` 配置自己的公网地址；好友仍须显式传入其实际 platform URL。支持自建 HTTP(S) 地址，拒绝 loopback/unspecified、凭据、query/fragment 和控制字符；缺失或无效时不会输出误导性短句。格式与边界见 [加好友文案](../SKILL.md#export-contact)。
