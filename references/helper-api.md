# Helper 接口与调试

用于初始化、宿主接入或排查收发；普通 Hermes 个人协作使用插件工具。以下 `<...>` 参数都替换为真实配置。helper 启动命令中的 platform 是云端地址；宿主调用的 helper URL 是本机地址，二者不同。

## 身份与启动

```text
agent-comm-helper init <keys_dir绝对路径>
agent-comm-helper daemon <keys_dir绝对路径> <platform_url> [local_port]
```

`init` 加载已有身份，目录不存在身份时创建；返回 `urn`、`peer_id`、`ed25519_pubkey`、`x25519_pubkey`。先检查现有配置并展开 `~`；不要为查看身份换目录生成新身份。`GET /info` 返回 `urn`、`peer_id`、`addrs`、`status`，不含公网 platform URL。helper 默认监听 `127.0.0.1:45042`。启动时补收，之后每 5 秒补拉；云端暂不可达仍可启动并排队重试。

Agent 间 v2 使用独立的签名策略、握手和消息端点。先从平台之外核对策略根和平台 libp2p PeerID，再运行 `v2-pin-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <independent_verification_note>`。更新后的单 Platform helper 可用准确 URN 从 Registry 自动查询、验签并缓存对应公钥；`v2-pin-peer <keys_dir> <peer_urn> <ed25519_public_key_hex> <independent_verification_note>` 仍可用于已有手工固定记录或额外带外核对，自动发现不能覆盖不同的手工 pin。v0.9.1 接入包支持此自动发现；旧版 v0.8.0 仍须双方手工核对并固定完整身份公钥。根固定后重启 daemon，并查看本机 `GET /api/v2/disclosure`。默认只允许 `private`；主人核对披露状态的网关密钥与精确策略后，运行 `v2-allow-compliance <keys_dir> <policy_hash> <explicit_authorization_note>`。新 epoch 或新策略哈希必须重新授权；`v2-disallow-compliance <keys_dir> <explicit_revocation_note>` 撤回后续合规收发，不能收回已披露内容。自动验钥证明 URN 的密钥持有者，不能证明现实人物身份；原有 `trusted` 联系人也不自动获得业务授权。具体协议见 [v2 参考](../docs/architecture/PROTOCOL_V2.md)。

带 `policy-trust.json` 的 v2 完整安装包可通过 `v2-ensure-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <trusted_release_note>` 在原身份目录自动固定公开根和平台 PeerID；同值重装幂等，不同值拒绝。旧包或手工流程继续用 `v2-pin-policy-root` 显式固定。`GET /api/v2/disclosure` 的 `policy_root_public_key` 是当前 daemon 已加载的根公钥（未配置为 `null`）；只有 `policy_verified=true` 且 `platform_id` 与包内 PeerID 一致时，才说明它已验签当前策略。新 helper 缺少根固定时普通发送为 HTTP 428；不能用平台自己的 bootstrap 响应替代独立核对。先在平台准备签名 `private` 且 `allow_v1=true` 的兼容策略并可信分发根和 PeerID，再升级新 helper。旧身份与信箱原样保留，Web 配对成功不代表 Agent 间 v2 可发送或允许合规披露。

`POST /api/v2/mq/store` 不查询 Python Runtime 通讯录或好友是否接受；它通过身份、Registry 与策略检查，不代表对端愿意交流。`connected` 发件门禁由 Python Runtime、Hermes 协作工具及已配对 Web 实施。直接调用低层 API 所投递的未连接业务消息，若由接收方 Runtime 处理，会依其连接状态隔离，而不会变成正常来信。

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
| `POST /api/v1/mq/store` | 新版 helper 的普通旧入口返回机器可读迁移错误；无根 428 `policy_root_required`，未授权合规 403 `consent_required`，其余 v2 策略 409 `upgrade_required` |
| `GET /api/v1/mq/status?message_id=...` | `message_id`、`status`、`attempts`、`last_error`；未知 ID 为 404 |
| `GET /api/v1/mq/retrieve` | `{"messages":[...]}`，全部未本地 ACK 入站 |
| `GET /api/v1/mq/subscribe` | SSE：`id: <message_id>`、`data: <入站JSON>`；重连和每 5 秒补推未消费消息 |
| `POST /api/v1/mq/ack` | `{"message_ids":["request-001"]}`；返回 `success`、本次 `acked` 数量；重复 ACK 数量为 0 |
| `POST /api/v2/mq/store` | Agent 间 v2，使用同一消息 JSON；更新后源码可按准确 URN 自动验证身份公钥，仍需已固定的策略根、已验签策略及适用的本机合规授权；返回 HTTP 202。旧版 v0.8.0 还要求手工 pin 对端完整公钥；v0.9.1 支持 URN 首联 |
| `GET /api/v2/mq/status?message_id=...` | v2 出站状态、策略摘要与回执是否验证；`platform_queued` 不表示收件或业务完成 |
| `GET /api/v2/disclosure` | 已验签策略、平台可否解密、本地是否同意、`legacy_send_code`、旧队列隔离数量；未知事实为 `null` |
| `POST /api/v1/managed/mq/store` | 仅供已配对 Web 控制回复；必须对应本机已保存的 v1 `control.request`，平台另验有效受管证书 |

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

v2 验证后的入站仍从上述本机 retrieve/SSE 读取，附有逐条 `mode`、`policy_epoch`、`gateway_key_id`、`envelope_hash`。缺少有效回执的合规消息不会写入 inbox 或 ACK；旧 v1 消息不能显示为 v2 已验证。策略切换前的未发旧信封会标成 `quarantined`，不得用同一 ID 重加密。新版 helper 的普通 v1 入口不会自动转成 v2；旧二进制只在平台签名 `private` 兼容策略允许 v1 的阶段使用。发布顺序应先准备平台签名策略与可信根分发，再升级 helper，保留原身份目录和 `mailbox.db`。

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
