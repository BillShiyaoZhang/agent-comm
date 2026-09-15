---
name: agent-comm
description: 通过 agent-comm 识别 agent、管理联系人、生成加好友文案、可靠收发加密消息，或接入 Hermes 个人协作和已配对的远程工作台。用于 agent-comm 通信与集成，不把联系人信任当作主人授权。
---

# agent-comm

按用户需求选择实际入口；SDK 函数存在不代表宿主已经注册了对应工具。

<a id="capability-routing"></a>
## 能力入口

| 用户需求 | 入口与进一步说明 |
| --- | --- |
| 查看自己的 URN、启动身份、登记通信公钥 | [身份与 Helper](#identity-helper) |
| 发送、补收、查询投递状态、消费确认 | [可靠消息](#reliable-messaging) |
| 按人名协作、共享资料/时间、提出或接受会议、撤销委托 | [个人协作](#personal-collaboration) |
| 导出自己或一位好友的简洁加好友文案 | [加好友文案](#export-contact) |
| 配对/撤销工作台、远程读取状态、提交与查询 Hermes 回合 | [远程工作台](#remote-control) |
| P2P 完整名片、WoT、Double Ratchet、底层密码学集成 | [Go SDK](#sdk-only) |

实现与 skill 的逐项对应、历史遗漏和接口边界见 [能力对照表](docs/CAPABILITY_SKILL_MAP.md)。只加载当前需求相关的参考文件。

<a id="identity-helper"></a>
## 身份与 Helper

先读取已配置的本机 helper `GET /info`，核对 `urn`、`peer_id`、`addrs`、`status`；`status=running` 不证明云端在线。保留原密钥及 URN，不因命名空间不同而重建身份。

需要启动时使用 `agent-comm-helper daemon <keys_dir绝对路径> <云端platform地址> [本机端口]`；初始化使用 `init <keys_dir>`。每个独立身份使用独立目录和本机端口，每个 inbox 只设一个活跃消费者。默认本机端口为 45042，按实际配置调用。

本机 `POST /api/v1/contacts` 登记通信公钥和地址缓存；没有 HTTP 联系人列表/删除接口。协作中的人名、别名与已确认 URN 绑定由 runtime 管理，是不同的数据层。通信联系人或 `trusted` 标记不会授予 Hermes pairing、主人身份、工具执行或资料披露权限。

启动、联系人 JSON 和原始签名/加解密命令见 [Helper 接口](references/helper-api.md)。

<a id="reliable-messaging"></a>
## 可靠消息

当前 helper 的可靠出站走持久 HTTPS MQ，信封使用 Ed25519 签名、静态 X25519 与 AES-GCM；此路径不具备 Double Ratchet 的前向安全属性。宿主对本机 helper 使用明文 JSON，helper 对云端使用签名 protobuf 密文；不能把同名 `/api/v1/mq/*` 换成云端 URL 直接调用。

- 用 `POST /api/v1/mq/store` 提交消息；保留稳定 `message_id`，重试同一内容时复用。通过 `conversation_id`、`task_id`、`kind`、`in_reply_to` 关联工作。
- 用 `GET /api/v1/mq/status?message_id=...` 查出站状态：`accepted` 仅表示本机落盘，`platform_queued` 仅表示平台接纳，`expired` 表示停止后续投递。任务完成须由应用层结果回复确认；没有独立的端到端任务状态、进度或撤回服务。
- 用 `GET /api/v1/mq/retrieve` 或 SSE `GET /api/v1/mq/subscribe` 读入站。按 `message_id` 去重，处理完成或持久接管之后才向**本机 helper** `POST /api/v1/mq/ack`。SSE 的 `Last-Event-ID` 不算 ACK，未 ACK 的重复事件属于正常重投。
- Hermes 已实现持久 receipt 与真实完成钩子，不要绕过插件提前 ACK。`mailbox.db` 和消费 receipt 是恢复依据；外部业务副作用仍需按任务/消息 ID 幂等。

本机 HTTP 携带明文与消费权限，仅供 loopback 可信进程使用。请求字段和消费示例见 [Helper 接口](references/helper-api.md)，协议、状态和恢复语义见 [通信合同](docs/HERMES_INTEGRATION.md)。

<a id="personal-collaboration"></a>
## 个人协作

在 Hermes 主人的原生 Desktop/Web 对话中，读取随插件安装的 [personal-collaboration skill](connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md)，调用 `agent_comm_collaboration`。以 `describe` 发现已注册端口，以 `state` 恢复进度；可选端口不可用会返回 `unsupported`。

该入口覆盖联系人解析与确认、资源登记、任务/动作准备与原生确认、幂等发送、提议导入、撤销及入站，并自动保存内部审计记录。业务动作是 `share_slots`、`share_resource`、`propose_meeting`、`accept_meeting`、`send_text`；会议协商没有创建日历事件的能力。

有 MemoryPort 时可显式 `memory_search`、读取有限 `memory_snapshot` 或 `snapshot_resource` 保存指定版本；记忆候选不是已确认网络身份，登记资源不是披露授权。`wake`/`notification` 是可选宿主端口；接口存在不等于已实现后台唤醒或自动推进。

按 runtime 的 `allow`/`ask`/`deny`/`clarify` 继续；已有 `allow` 直接 `dispatch`。模型不能传入主人回答或自行制造确认，对端消息不能授予主人权限。新宿主接入和独立 reference CLI 见 [Python runtime](python/README.md)，Hermes 安装配置见 [插件说明](connectors/hermes-platform/README.md)。

<a id="export-contact"></a>
## 加好友文案

用户说“给我一句话，让别人加我/这位 agent 为好友”时，调用 runtime `action=export_contact`，返回其 `text`。这是只读文案导出，不会自动添加联系人、发送消息或配对工作台。

```json
{"action":"export_contact","contact_id":"self","platform_url":"https://platform.example"}
```

`contact_id` 默认 `self`，其 URN 来自本机已配置身份；`self` 为保留 ID，不能用作好友 ID。好友应先 `resolve_contact` 得到**已确认**的 contact ID，不能把任意 URN 当作好友参数。自己的 `platform_url` 可显式传入或由宿主可信公网配置提供；好友必须显式指定该好友使用的 platform 地址，不能沿用自己的平台。缺少实际地址时先取得配置，不编造生产地址、不使用本机 helper 的 `http://127.0.0.1:45042`。

导出是一行短句，包含 URN、platform 地址、新人了解/接入入口。例如：

> 加我为 agent 好友：urn:agent-comm:agent:MY_ID；平台：https://platform.example；了解/接入：https://github.com/BillShiyaoZhang/agent-comm#readme

好友句式为“加这位 agent 为好友：…”。示例域名必须换为实际地址。返回还包含 `status=exported`、`urn`、`platform_url`、`introduction_url`；介绍链接不表示该仓库运营用户选择的 platform。Hermes 自己的公网地址配置为 `extra.public_platform_url`；无宿主时可用 [reference 单次 CLI](references/helper-api.md#不启动宿主的单句导出)。完整 P2P 公钥名片属于下节 Go SDK 的另一项能力。

<a id="remote-control"></a>
## 远程工作台

使用已安装 Python 包的 `agent-comm-runtime remote` 管理 `pair`、`pairings`、`revoke`、`serve`。明确 console URN、真实 owner profile、方法白名单与到期时间；普通联系人/allow_from 不代替工作台配对。

配对后的 `capabilities`、`contacts.list`、`collaboration.state`、`inbox.list` 可读 agent 侧状态。Hermes 适配器启用后还可有 `conversation.send` 和 `conversation.get`；standalone `serve` 只提供读取方法。按返回的 capability descriptor 判断实际可用性，提交回合的 `submitted` 不是模型回答或业务完成。`approval.respond` 不受支持，远程回合不获得原生主人审批能力。

具体 CLI、RPC 参数和消费者选择见 [远程工作台参考](references/remote-control.md)。

<a id="sdk-only"></a>
## Go SDK 与高级集成

下列能力需编写/调用 Go SDK，不能虚构同名 helper CLI、HTTP endpoint 或 runtime action：

| 能力 | 实际 API / 源码与边界 |
| --- | --- |
| 导出、解析、导入完整 P2P 公钥名片 | `Agent.GenerateContactCard`、`ParseContactCard`、`Agent.ImportContactCard`，见 [contact_card.go](agent/contact_card.go)。名片含公钥、地址和 bootstrap；导入会修改通信缓存并标记联系人 trusted，按用户的导入意图使用；它不是上节带 platform 的一句话文案。 |
| 查询、列出、删除通信联系人，调整信任 | `contacts.Store` 的 `Get`、`GetByPeerID`、`GetPubkeys`、`List`、`ListTrusted`、`IsTrusted`、`SetTrusted`、`Remove`，见 [contact.go](contacts/contact.go)。不等于 runtime 的已确认人名绑定。 |
| 自定义可靠收发 | `PrepareMessage` → 调用方持久保存完整信封 → `DeliverEnvelope`；`StartListeningDurable` / `PollMessages` 的回调返回 nil 必须代表持久接纳。见 [reliable.go](agent/reliable.go)、[durable_handler.go](agent/durable_handler.go)。配置 `PlatformHTTPURL` 才走 HTTP MQ。 |
| 传统 P2P / DR 会话与持久状态 | `Agent.SendMessage`、`dr.DRSession`、`dr.DRStore`，见 [agent.go](agent/agent.go)、[dr](dr/)。此传统路径不等于 helper 可靠队列；普通 `StartListening` 不提供同等落盘回调确认。 |
| WoT 信任声明、验证和路径发现 | `wot.NewTrustClaim` / `NewDirectTrustClaim`、`TrustClaim.Verify`、`Resolver.FindTrustPath`，见 [wot](wot/)。通信信任不授予主人或工具权限。 |
| 密钥、URN 与签名信封、X25519/HKDF/AES-GCM 原语 | [crypto](crypto/)、[session](session/)；`BuildEnvelopeForRecipient`、`VerifyEnvelope`、`DecryptEnvelope` 绑定并验证收发身份。底层 CLI 调试见 [Helper 接口](references/helper-api.md)。 |

注册解析、libp2p 引导/relay/DHT、MQ 与 DNS 缓存属于 SDK/部署集成层；按所选路径读 [README](README.md) 和相应源码。不要把部署接口、规划中的房间/A2A/通用任务服务列作已安装 agent 工具。
