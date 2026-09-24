# Agent 间 v2 隐私与合规信封

此页记录 SDK/helper **源码已实现**的 v2 协议。它不表示任何公开安装包或线上 Platform 已升级。完整信任边界与策略迁移见部署仓库的[设计说明](../../../../docs/architecture/COMPLIANCE_GATEWAY.md)。v1 protobuf 与 v2 JSON 使用不同端点，不能互相解析。

## 启用与信任来源

在双方各自设备上，先通过平台之外的可信渠道取得并核对完整 Ed25519 公钥，再明确固定；策略签名根和平台 libp2p PeerID 也必须从可信安装/部署渠道取得。不能把同一平台未认证的 bootstrap 响应当作固定 PeerID 的证据。URN 自证明公钥属于该 URN，但**不会证明该 URN 就是现实中的某个人**。旧联系人 `trusted` 和平台注册表结果不自动成为 v2 身份锚。

```text
agent-comm-helper v2-pin-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <independent_verification_note>
agent-comm-helper v2-pin-peer <keys_dir> <peer_urn> <peer_ed25519_public_key_hex> <independent_verification_note>
```

旧版只固定根公钥、没有平台 ID 的 pin 文件无法启用 v2；独立核对 PeerID 后可用上面的命令、相同根公钥为旧文件补上 ID，不能从 bootstrap 自动补齐，也不能覆盖不同根或已有平台 ID。v2 完整接入包可把经可信发布渠道核对的公开根和 PeerID 放入校验清单覆盖的 `policy-trust.json`，安装脚本以 `v2-ensure-policy-root` 固定到原身份目录；同值重装幂等，异值拒绝且不自动轮换。没有该文件的旧安装包不能声称已固定。新 helper 缺少 pin 时拒绝普通消息发送。发布应先部署签名 `private` 兼容策略并独立分发根与 PeerID，再升级 helper。

两个手工 pin 命令均拒绝空核对记录和覆盖已有不同密钥。保留原身份目录和 `mailbox.db`；不要重新初始化身份。默认只允许 `private`。先在本机 `GET /api/v2/disclosure` 查看 daemon 实际加载的公开 `policy_root_public_key`，以及经该根验签的 `platform_id`、`mode`、`policy_epoch`、`policy_hash`、`gateway_key_id`、`platform_can_decrypt`、有效期与隔离队列数量。根未配置时 `policy_root_public_key=null`；未经验证的模式或解密能力也为 `null`，不是 `false`。主人核对**这一份精确策略**并同意网关解密后，在**每一端**执行：

```text
agent-comm-helper v2-allow-compliance <keys_dir> <disclosure.policy_hash> <explicit_authorization_note>
agent-comm-helper v2-disallow-compliance <keys_dir> <explicit_revocation_note>
```

授权只适用于当时已验签的完整策略哈希；新 epoch、网关密钥或其他签名策略字段变化都重新进入 `consent_required`。撤回会阻止后续合规新收发；已经准入的消息、在途网络请求和已披露明文不能收回。缺少本地授权时，即使平台策略要求 `compliance`，helper 也停止 v2 握手、收发和新本机入队。首次读取新策略仍会记住更高 epoch，并隔离切换前未交付的旧密文。

## 握手与消息

`v2/` 包定义固定字段顺序的紧凑 JSON；签名字段也出现，签名预映像中该字段为 `null`。`Canonical` 不做 HTML 转义，解析器会重编码比较，拒绝重复、额外或非规范字段。所有 `[]byte` 字段用标准 base64；时间为 Unix 秒。线格式、Go/TypeScript 对照样本在 [`v2/testdata/interop.json`](../../v2/testdata/interop.json)。

1. A 用已固定的 B 身份检查 Registry 中 B 自签的 X25519 公钥。双方经平台交换 Ed25519 已签的 `Init`、`Accept`，各包含一次性 X25519 公钥、随机数、策略摘要、模式与套件。双方核对后由临时 ECDH 与完整握手摘要派生本次密钥，并相互核验 `Finished` MAC。
2. `private` 正文密钥只从已完成的临时会话与单调序号派生。信封没有密钥槽。平台只见路由和密文。
3. `compliance` 每条消息生成独立随机 CEK，AES-256-GCM 加密正文**一次**；RFC 9180 X25519/HKDF-SHA256/AES-256-GCM HPKE 分别给收件方和指定网关封装同一 CEK。网关解开正文并签准入回执；回执中的 HMAC 持钥证明必须与收件方实际解开的 CEK 一致。平台无须持有 Agent 私钥。

`Policy` 由独立固定的 Ed25519 根签署，包含平台 ID、epoch、有效期、模式、套件、网关加密公钥、回执签名公钥、受管 Web issuer 公钥。域分离前缀是 `agent-comm-v2-policy\x00`、`agent-comm-v2-handshake\x00`、`agent-comm-v2-envelope\x00`、`agent-comm-v2-receipt\x00`。哈希是完整已签规范字节的 SHA-256 小写 hex。网关持钥证明是 `HMAC-SHA256(HKDF-SHA256(CEK, zero_salt_32, "agent-comm-v2/admission-proof"), SHA256(raw_envelope))`。

握手、正文和回执必须与**当前**签名策略完全一致。收到更高 epoch 后删除旧会话，所有未发完的旧请求（包括还没加密的本机明文）标为 `quarantined`；已生成的旧信封保留原字节，不以同一消息 ID 重新加密。旧队列默认不当作新模式消息交付。`platform_queued` 只表示平台准入，入站在验签、解密、验证回执并持久写入后才 ACK；业务完成另算。

## helper API

固定根后重启 daemon。`POST /api/v2/mq/store` 接受与本机 v1 store 相同的消息 JSON，返回 HTTP 202、稳定 `message_id` 和本次观察的披露模式；`GET /api/v2/mq/status?message_id=...` 返回 `status`、`policy_hash`、`receipt_verified` 等。v2 验证后的入站进入原有本机 `/api/v1/mq/retrieve`/SSE/ACK，增加 `mode`、`policy_epoch`、`gateway_key_id`、`envelope_hash`。这些是逐条消息的验证结果，不是全局 UI 标签。

新版 helper 的普通 `POST /api/v1/mq/store` 是迁移断点：无固定根返回 HTTP 428 `policy_root_required`；已验签策略要求合规但尚未按该哈希授权时返回 403 `consent_required`；其余 v2 就绪状态返回 409 `upgrade_required`。这些响应均带 `disclosure`、`v2_store_path` 和 `disclosure_path`，不会把 v1 请求静默改成 v2。Hermes、Python runtime 和 OpenClaw 的新版普通发件客户端会先读披露状态，v2 就绪时改用 `/api/v2/mq/store`；只对没有披露端点的**旧版 helper**保留 v1 回退。受管 Web 控制回复使用独立的本机 `/api/v1/managed/mq/store`，helper 只接受与已保存 v1 控制请求对应的 `control.response`；平台仍独立核验收件方的有效受管证书。

平台 HTTPS 使用 `/api/v2/policy`、`/api/v2/handshake/*` 和 `/api/v2/mq/*`；接入客户端见 [`v2/client.go`](../../v2/client.go)。旧 v1 本机接口仍用于明确的受管 Web 控制路由；它不能显示为 Agent 间 v2 私密或合规已验证消息。v2 模式下 helper 禁用直连 v1/DR 入站流，防止绕过平台准入。

## 验证边界

`go test ./v2 ./cmd/helper ./agent` 包括 RFC 9180 官方 X25519 KEM 值、双向临时握手、网关与收件方同密文解密、错误 CEK/槽/正文拒绝、队列序号原子持久化与策略切换隔离。该测试不证明已上线、真实人身份核对、网关私钥从未复制、明文从未被再转交或正文没有另一层加密。
