# agent-comm 能力与 skill 对照

## 2026-09-17 当前能力增补

下文保留早期基线审计。当前 Runtime 额外提供 `contact_requests`、`prepare_contact_response`、`prepare_message`、`mark_read`；`prepare_contact` 在本人确认后发送持久好友请求，双方同意后才显示 `connected`，旧单边通讯录映射为 `unverified`。对应 Web RPC 为 `contacts.requests`、`contacts.respond`、`messages.send`、`inbox.mark_read`，并通过已配对的 `collaboration.execute` 访问同一 Runtime。完整动作与参数由 `describe.action_fields` 给出。

Hermes 的已配对 Web 会话与本机主人会话共用协作工具，权限由真实宿主上下文和本地配对共同检查。所有业务数据以本机 Store 为准；消息已读、好友决定与提醒终态同步到其它端。helper 新增 `POST /api/v1/platform/register` 供本机绑定脚本自动注册，`GET /api/v1/presence?urn=...` 校验近期签名心跳。平台注册不代表联系人已接受，也不代表已获主人权限。

当前操作说明见[个人协作 skill](../../connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md)与 [Hermes connector](../../connectors/hermes-platform/README.md)。

## 审计口径

基线为 SDK 提交 `a2d06d0eb5c8a283328986ff7ae60ec13e125891`。本表中的“原 skill”和行号均指该提交，不把本次补文档后的覆盖倒算为原有覆盖。审计读取源码、公开入口与技能正文；没有连接生产服务，也不把接口存在等同于部署后的运行验收。

两个原技能的职责与现状：

- **R：根技能** [SKILL.md](../../SKILL.md) / [SKILL_EN.md](../../SKILL_EN.md)。原正文主要是旧 helper 接入、身份初始化、签名与加解密。中文第 11 行虽提示按新文档接入，后续仍有失效操作；英文没有这一提示。跳转到 README 只算索引，不算逐项表明能力。
- **P：个人协作技能** [personal-collaboration/SKILL.md](../../connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md)。在 Hermes 原生主人会话调用 `agent_comm_collaboration`。基线的 **15 个 Runtime action 和 5 个业务 capability 已全部表明**；不能把根技能没写而 P 已写的能力称为“所有 skill 都遗漏”。

状态含义：**已覆盖**＝有明确名称和调用/使用说明；**部分**＝提到意图或路径，但缺可操作入口或关键语义；**缺失**＝正文未表明；**错误**＝存在与实际实现不符的操作或保证。**新增**是本次新实现，区别于原来已有但未写明的能力。

以下按公开入口逐项列出 Runtime、业务能力、远程方法、helper HTTP 和 CLI；Go SDK 只按公开能力族归纳，**不是所有底层函数的穷举**。传输联系人、公钥信任、协作联系人和主人远程配对是不同记录，不互相授予权限。

## 一、原来已有但 skill 未完整表明的能力

| 缺口 | 原来实际提供什么 | 应如何补齐 |
| --- | --- | --- |
| 本机身份查询 | helper `/info` 返回本机 URN、Peer ID、监听地址和运行状态 | 根 skill 给出查询入口；运行中不代表平台或对端可达 |
| 可靠消息状态与补收 | `/mq/status` 查询出站状态；`/mq/retrieve` 补拉未本地确认的消息；同 ID 重试、重启恢复与持久 ACK | 根 skill 说明状态层级、幂等重试、SSE 重放和本机确认合同 |
| 传输联系人登记 | `/api/v1/contacts` 保存 URN、公钥、Peer ID、显示名等 | 与 P 的本地别名绑定分开；不能当作 Hermes `allow_from`、主人配对或发信授权 |
| 主人远程工作台 | 本地配对/撤销/列举/消费；已配对方读取能力、联系人、事项、收件箱；Hermes 可运行远程会话并轮询结果 | 根 skill 逐项列方法、开关和本地配对前提；说明远程批准未实现 |
| 宿主无关 Runtime | 独立 Python 包、终端参考宿主、可选记忆/交互/传输适配器 | 根 skill 提供发现与接入索引；P 保持 Hermes 具体工作流 |
| 传统长名片 | Go SDK 已能生成、解析、导入含公开密钥和网络地址的多行文本卡片 | 作为 SDK 能力列明；基线没有 helper/runtime 的短邀请文案入口 |
| 低层 SDK 能力 | 联系人增删与信任标记、URN 注册解析、WoT 声明/路径、P2P/DR、可靠信封、DHT 等 | 作为开发接口索引表明，不能冒充当前 Hermes 模型工具或已安装扩展 |

## 二、Runtime action 逐项对应（基线 15 项）

统一入口为 `Runtime.dispatch({"action": ...}, context=可信宿主上下文)`，Hermes 包装为 `agent_comm_collaboration`。参数白名单见 [runtime.py](../../python/agent_comm_runtime/runtime.py) 基线 L6–22；Hermes 注册见 [hermes.py](../../connectors/hermes-platform/hermes_platform_agent_comm/collaboration/hermes.py) 基线 L278–283。所有 action 先获取并重验真实主人上下文；模型参数不能提供 owner、context 或回答。

“新 skill 对应”指本次整理后的根技能能力索引及 P 的具体工作流；P 的原有覆盖不会因整理改变结论。

| action / 实际能力 | 实现证据（基线） | 原 R / P | 边界与新 skill 对应 |
| --- | --- | --- | --- |
| `describe`：发现 action、业务能力与已注册端口 | `runtime.py` L78–81 | 缺失 / 已覆盖 P L15–24 | 列出 action 不保证所需端口存在；新根 skill [个人协作](../../SKILL.md#personal-collaboration)索引，P「Starting and recovering」 |
| `state`：恢复联系人、事项、动作、待确认项、收件箱与提议 | `runtime.py` L82–83；[store.py](../../python/agent_comm_runtime/store.py) L657–673 | 缺失 / 已覆盖 P L26 | 可选 `task_id` 筛选；按主人 profile 隔离；新根 skill [个人协作](../../SKILL.md#personal-collaboration)，P「Starting and recovering」 |
| `inbox`：同步 helper 消息并读取持久收件箱 | `runtime.py` L138–143；`store.py` L587–621 | 缺失 / 已覆盖 P L27–29 | 需要 transport；先持久接管后 ACK；外部文本不是主人授权；新根 skill [个人协作](../../SKILL.md#personal-collaboration)，P「Starting and recovering」 |
| `import_proposal`：导入收到的结构化提议 | `runtime.py` L94–95；`store.py` L623–655 | 缺失 / 已覆盖 P L121–126 | 只用已落盘 `message_id`，导入不等于同意；P「Scope and typed actions」 |
| `register_resource`：保存有限材料快照 | `runtime.py` L84–85；`store.py` L109–129 | 缺失 / 已覆盖 P L33–34 | `resource_id`、`title`、`text`；登记不授予对外披露权；P「Starting and recovering」 |
| `resolve_contact`：按 ID 或别名解析协作联系人 | `runtime.py` L86–87；`store.py` L171–178 | 部分（泛称联系人） / 已覆盖 P L30–32 | 单一匹配才 allow；零个或多个匹配 clarify；不是公共网络搜人；P「Starting and recovering」 |
| `prepare_contact`：准备明确 URN 的本地联系人绑定 | `runtime.py` L88–89；`store.py` L150–169 | 部分（泛称联系人） / 已覆盖 P L30–32 | `contact_id`、`aliases`、`urn`；需原生确认；已确认记录不可改绑；P「Starting and recovering」 |
| `prepare_task`：准备受范围约束的委托 | `runtime.py` L90–91；`store.py` L197–221 | 缺失 / 已覆盖 P L35–36、L48–84 | 接收人、能力、材料、时段、次数、到期时间写入 scope；P「Scope and typed actions」 |
| `prepare_action`：准备不可变动作并求策略决定 | `runtime.py` L92–93；`store.py` L267–355 | 缺失 / 已覆盖 P L86–113 | 返回 allow/ask/deny/clarify；变更内容用新 `operation_id`；P「Scope and typed actions」 |
| `confirm`：由宿主展示问题并收取主人回答 | `runtime.py` L98–108；`store.py` L371–450 | 缺失 / 已覆盖 P L37–46 | 只收 `approval_id`，不能模型代传 approved/回答；缺 interaction 为 unsupported；P「Starting and recovering」 |
| `dispatch`：发送已准备并获准的具体动作 | `runtime.py` L138–141；`store.py` L478–548 | 缺失 / 已覆盖 P L107–119 | 每次最多尝试 4 个未接纳收件人；sending 用同 ID 继续；accepted 只指本机入队；P「Scope and typed actions」 |
| `revoke`：撤销事项后续发送权限 | `runtime.py` L96–97；`store.py` L469–476 | 缺失 / 已覆盖 P L130 | 接受 `task_id`；不能撤回已送出的材料；P「Scope and typed actions」 |
| `memory_search`：有限记忆候选检索 | `runtime.py` L109–122 | 缺失 / 已覆盖 P L18–24 | 可选 memory adapter；显式 query、1–20 条，不构成身份或披露授权；P「Starting and recovering」 |
| `memory_snapshot`：读取指定有限快照 | `runtime.py` L123–137 | 缺失 / 已覆盖 P L19–24 | 可选 memory adapter；明确 reference、1–8000 字符；读到不等于可分享；P「Starting and recovering」 |
| `snapshot_resource`：把确切记忆版本登记为资源 | `runtime.py` L123–136 | 缺失 / 已覆盖 P L20–24 | 保留 reference/source/version；仍须独立披露授权；P「Starting and recovering」 |

## 三、业务 capability 逐项对应（基线 5 项）

这些是 `prepare_action.operation.capability` 的取值，**不是五个独立工具**。名单见 [runtime.py](../../python/agent_comm_runtime/runtime.py) 基线 L80 与 [Hermes schema](../../connectors/hermes-platform/hermes_platform_agent_comm/collaboration/hermes.py) 基线 L256；具体校验、策略和消息编译见 [policy.py](../../python/agent_comm_runtime/policy.py)。根技能原来均缺失；P 原来均已覆盖。新根技能汇总，精确 payload 留在 P「Scope and typed actions」。

| capability | 实际内容与 payload | 原 skill 证据 | 边界 |
| --- | --- | --- | --- |
| `share_slots` | 发送候选时间；`slots:[{start,end}]` | P L86–96 | 只分享获准的候选时段；没有自动读取日历 |
| `share_resource` | 分享已登记资源快照；`resource_id` | P L102 | 只发送获准的准确材料版本，不读取任意文件 |
| `propose_meeting` | 发送结构化约见提议；`proposal_id,version,topic,participant_ids,start,end` | P L103 | 明确版本、主题、参与人和时段；不创建日历事件 |
| `accept_meeting` | 对已知当前提议发送同意；完整当前提议字段 | P L104、L121–126 | 先由收件箱导入对方提议；不能凭模型编造对方同意 |
| `send_text` | 发送具体自由文本；`text` | P L105–109 | 全文单独确认；scope 中有该能力也不能跳过全文审阅 |

## 四、远程控制方法逐项对应

协议是 `agent-comm-control/v1`，经已有 helper 的认证加密邮箱传递 `control.request/control.response`。方法白名单、权限检查及执行见 [remote.py](../../python/agent_comm_runtime/remote.py) 基线 L19–20、L134–142、L186–233。**原 R、P 均未表明以下 remote 方法**；README 中有远程入口提示，不替代 skill 覆盖。新根 skill 的[远程控制](../../SKILL.md#remote-control)统一列出。

| method | 基线实际入口与证据 | 部署前提 / 边界 |
| --- | --- | --- |
| `capabilities` | `RemoteBridge._invoke` L190–198 | 已存在且允许该方法的本地配对；返回方法及逐项 available，不能只看名字 |
| `contacts.list` | `_invoke` L199–201 | 读取配对 owner 的协作联系人，不是任意 agent 好友抓取或 helper 公钥库 |
| `contacts.add` | 新版源码 `RemoteBridge._invoke` 与 Store 联系人写入 | 用户在 Web 提交 `{contact_id,aliases,urn}` 即确认绑定；须本机显式配对该方法，主体从配对导出，结果保存在 agent |
| `collaboration.state` | `_invoke` L202–207 | 可选 `task_id`，读取 owner 的协作状态；不给原生确认权 |
| `inbox.list` | `_invoke` L202–207 | 可选 `task_id`，读取已落盘协作收件箱；不是任意公共邮箱 |
| `conversation.send` | `_invoke` L208–222；[platform.py](../../connectors/hermes-platform/hermes_platform_agent_comm/platform.py) L436–479 | Hermes 注册 `conversations=True` 后才可用，还需 remote 开关、本地方法配对和 Gateway 接纳；返回 submitted/turn_id，不等于已经回答 |
| `conversation.get` | `_invoke` L223–230；`platform.py` L481–492 | Hermes 会话入口；按 conversation_id 返回最近至多 100 回合的状态/文本/回答；独立轮询完成结果 |
| `approval.respond` | 新版源码 `RemoteBridge._invoke` 与 Store 审批状态转移 | 用户提交 `{approval_id,decision:"approve"|"deny"}`；须单独配对权限，校验归属、期限、撤销与版本；保存决定但不直接发消息或执行外部工具 |

Standalone `remote serve` 不创建真实会话执行器。[Hermes platform.py](../../connectors/hermes-platform/hermes_platform_agent_comm/platform.py) 的真实消息事件与完成回调才提供会话执行。Web 新增写方法需要发布匹配的 runtime 与 Web，并在本机显式更新配对；旧配对和只读同步 worker 不会自动获得或使用它们。自定义 `register_handler` 是可信宿主开发接口，未注册的方法不算可用产品能力。

### 远程管理 CLI（基线 4 项，原 R/P 均缺失）

入口为 `agent-comm-runtime remote ...` 或 `python -m agent_comm_runtime.daemon remote ...`，见 [daemon.py](../../python/agent_comm_runtime/daemon.py) 基线 L18–36、L40–100。新根 skill [远程控制](../../SKILL.md#remote-control)列出命令并链接 Python README。

| command | 实际能力 | 必需/可选参数与边界 |
| --- | --- | --- |
| `remote pair` | 本机管理员授予某 console 的明确方法与有效期 | 指定 `--state` 或 `--hermes-profile`，`--console-urn`、重复 `--allow`、带时区 `--expires`；非 Hermes 需 `--owner-principal`；不通过远程 RPC 自助配对 |
| `remote revoke` | 撤销指定 console 配对 | state/profile 和 `--console-urn`；撤销配对不等于删除协作联系人 |
| `remote pairings` | 列出已有本地配对 | state/profile；读取 remote SQLite，不需打开材料库 |
| `remote serve` | 消费持久邮箱，响应读取 RPC，接管普通协作来信 | `--agent-urn`、协作 state/profile；可选 `--helper-url`、`--once`；同 helper 不同时运行 Hermes remote 消费器 |

## 五、本机 helper HTTP endpoint 逐项对应（基线 7 项）

路由与实现见 [daemon.go](../../cmd/helper/daemon.go) 基线 L205–219；当前合同见 [HERMES_INTEGRATION.md](../guides/HERMES_INTEGRATION.md)。这是**本机明文 IPC**，与云端同名密文接口不同；不能只换 URL 直接打云端。新根 skill [身份与 helper](../../SKILL.md#identity-helper)和[可靠收发](../../SKILL.md#reliable-messaging)列出全部 7 项。

| endpoint | 实际能力 / 源码基线行 | 原根 skill | 实际合同与边界 |
| --- | --- | --- | --- |
| `GET /info` | 查询运行身份；L234–247 | 缺失 | 返回 urn、peer_id、addrs、status；基线不返回公钥或云平台地址；查询成功不证明端到端可达 |
| `POST /api/v1/mq/store` | 接纳持久出站请求；L367–410 | 部分且错误：R L48 写首选 P2P/DR | HTTP 202 + `success:true` + 稳定 message_id；同 ID 同内容幂等，不同内容冲突；可靠 helper 出站走 HTTPS MQ |
| `GET /api/v1/mq/subscribe` | SSE 实时入站及重放；L412–464、L477–494 | 部分：R L49 提到 SSE | 重连/周期补推全部未本地 ACK 消息；Last-Event-ID 不等于消费 ACK |
| `GET /api/v1/mq/retrieve` | 拉取未本地 ACK 的持久入站；L496–508 | 缺失 | 返回 `messages`；供恢复与对账，不能与活跃消费者抢确认 |
| `POST /api/v1/mq/ack` | 确认本机消费；L510–529 | 错误：R L116 / 英文 L114 要求直接向平台确认 | `message_ids` 1–1000 个；处理完成或持久接管后确认本机 helper；平台 ACK 由 helper 持久入站后负责 |
| `GET /api/v1/mq/status?message_id=...` | 查询出站投递状态；L531–547 | 缺失 | 返回状态、attempts、last_error；accepted / platform_queued / expired 都不是对方业务完成；未知 ID 为 404 |
| `POST /api/v1/contacts` | 添加/更新传输联系人公钥记录；L249–365 | 部分：激活条件泛称“联系人”，无接口 | URN、X25519 公钥，以及 Peer ID 或可推导它的 Ed25519 公钥；支持显示名/别名、地址与信任字段；不等于 P 联系人绑定、allow_from 或 owner 配对 |

`/store`、`/subscribe`、`/contacts` 后缀兼容路由属于同一接口的别名，不另计为三种能力。HTTP 内部的持久信封复用、重试、SSE 重放、入站验签/解密与 ACK 属于可靠传输保证，不是由模型直接调用的独立业务动作。

### helper CLI（基线 6 项）

命令分发表见 [main.go](../../cmd/helper/main.go) 基线 L25–45。新根 skill [底层 SDK](../../SKILL.md#sdk-only)及 [helper 接口参考](../../references/helper-api.md)给出真实签名，日常协作优先用 Runtime/连接器。

| command | 真实签名 / 实现证据 | 原根 skill | 边界 |
| --- | --- | --- | --- |
| `init` | `init <keys_dir>`；`main.go` L65–88 | 已覆盖 R L58–70 | 加载或创建本机身份，输出 URN、Peer ID、公开密钥；不要为查当前身份随意创建新目录 |
| `daemon` | `daemon <keys_dir> <platform_url> [local_port]`；`daemon.go` L61–73 | 部分：提到常驻及端口，没有完整启动签名 | platform_url 是云平台；不同身份使用独立目录和端口；默认本机 45042 |
| `sign-retrieve` | `sign-retrieve <keys_dir> <urn> <timestamp>`；`main.go` L91–125 | 已覆盖 R L73–84 | 底层调试/集成签名，不是正常本机 inbox 调用所需步骤 |
| `sign-store` | `sign-store <keys_dir> <body_hex>`；`main.go` L128–157 | 已覆盖 R L86–90 | 签署准确请求字节；正常本机 `/store` 由 helper 处理认证 |
| `encrypt-envelope` | `encrypt-envelope <keys_dir> <recipient_urn> <recipient_pubkey_hex> <plaintext_hex> <message_id>`；`main.go` L160–202 | **错误**：R L95 / 英文 L93 少 recipient_urn、message_id | 输出签名完整信封及 `envelope_proto_hex`；稳定 ID 与收件人绑定不可省略 |
| `decrypt-envelope` | `decrypt-envelope <keys_dir> <envelope_proto_hex>`；`main.go` L205 起 | **错误**：R L101 / 英文 L99 仍传散列密文字段 | 只接受完整认证 protobuf 信封；旧无签名散字段用法被拒绝 |

## 六、宿主集成与低层 SDK 能力族

本节归纳公开 API，不主张有同名模型 action。原 R 常泛称 P2P、密钥、联系人，但未给下列大多数具体能力的入口；原 P 聚焦个人协作，不负责底层 SDK。新根 skill [底层 SDK](../../SKILL.md#sdk-only)及 [helper 接口参考](../../references/helper-api.md)链接本表及源码。

| 能力族 / 公开入口 | 实现证据（基线） | 原 skill 覆盖与实际边界 |
| --- | --- | --- |
| 独立 Python Runtime 与参考宿主 | [reference.py](../../python/agent_comm_runtime/reference.py) `main` / `demo`；[python/README.md](../../python/README.md) L5–23 | R 缺失，P 只写 Hermes。`python -m agent_comm_runtime.reference --demo` 离线演示；交互参考宿主要求真实终端；不能把 stdin 接模型输出冒充主人确认 |
| Host / Interaction / Memory / Transport 注册、发现和调用 | [ports.py](../../python/agent_comm_runtime/ports.py) `AdapterRegistry.register/require/load_entry_point/describe/invoke`；[Hermes adapter](../../connectors/hermes-platform/hermes_platform_agent_comm/collaboration/hermes.py) L195–202 | P 已表明可选端口发现与记忆使用；R 缺失独立装配入口。Hermes 默认无 MemoryPort，需明确安装选择的 adapter |
| host wake / interaction notification 扩展合同 | `ports.py`；`python/README.md` | 通用端口仍可选，没有公共任意通知 action。0.1.1/connector 1.4.0 另以持久 attention feed 与可选 Hermes Desktop companion 实现纯提醒，不使用 wake 唤醒后台私人模型；见 connector README |
| Hermes 原生通道与持久完成回调 | [plugin.py](../../connectors/hermes-platform/hermes_platform_agent_comm/plugin.py) L17–34；[platform.py](../../connectors/hermes-platform/hermes_platform_agent_comm/platform.py) `send` / `on_processing_complete` | R 部分（通道/SSE）；遗漏关联字段、完成 receipt、allow_from、循环/期限约束。协作/remote 模式限制绕过受控流程直接发送；不是任意外部控制命令入口 |
| OpenClaw channel 适配 | [channel.ts](../../connectors/openclaw-channel/src/channel.ts)、[connector README](../../connectors/openclaw-channel/README.md) | R 部分，写明框架但不能把 Hermes 的个人协作/remote 能力自动归给 OpenClaw；具体接入以该连接器实现为准 |
| 传统多行文本名片生成、解析、导入 | [contact_card.go](../../agent/contact_card.go) `GenerateContactCard` L31、`ParseContactCard` L69、`ImportContactCard` L165，Agent 方法 L223/L228 | R/P 缺失；仅 Go SDK。含公开密钥与可选地址，导入会更新 peerstore/session/contact；基线无 helper CLI、HTTP 或 Runtime 短文案入口 |
| 传输联系人增删、查询、公钥缓存与信任标记 | [contact.go](../../contacts/contact.go) `Add/Get/GetByPeerID/GetPubkeys/SetTrusted/List/ListTrusted/Remove` L84–260 | R 部分泛称联系人；SDK 级 CRUD 不等于 Runtime 可删除已确认绑定；不是自动主人授权 |
| 传统即时收发与生命周期 | [agent.go](../../agent/agent.go) `SendMessage` L145、`OnMessage` L308、`Close` L313；[handler.go](../../agent/handler.go) `StartListening` L15 | R 部分描述 P2P/DR，却错误套用于当前 helper。传统 SendMessage 路径与可靠出站路径并存；旧无返回值回调不保证应用持久接纳 |
| 可靠信封准备、投递、持久监听和单批轮询 | [reliable.go](../../agent/reliable.go) `PrepareMessage` L24、`DeliverEnvelope` L78；[durable_handler.go](../../agent/durable_handler.go) `StartListeningDurable` L28、`PollMessages` L109 | R/P 未列 SDK 入口；调用方先保存完整信封，重试复用；handler 返回 nil 表示持久接纳，不是模型已完成业务 |
| 公共身份注册/URN 网络解析 | [registry/client.go](../../registry/client.go) `Client.Resolve/RegisterWithSignature`、`HTTPClient.RegisterWithPolicy/Resolve` | R 部分（frontmatter 提 URN 解析，无可操作入口）；查到网络身份不是本地人名绑定或授权；可信结果须校验签名和密钥绑定 |
| MQ 密文存储、拉取和确认客户端 | [mq/client.go](../../mq/client.go) `Client` / `HTTPClient` 的 `Store/Retrieve/Ack` | R 部分且旧 ACK 流程错误；SDK 网络客户端处理密文和认证，本机 helper HTTP 处理明文，不可混用 |
| WoT 声明生成/验证、保存/查询与信任路径搜索 | [wot/claim.go](../../wot/claim.go) `NewTrustClaim/NewDirectTrustClaim/Verify`；[wot/store.go](../../wot/store.go)；[wot/resolver.go](../../wot/resolver.go) `FindTrustPath/FindTrustPathSimple/FetchClaimsAbout/VerifyTrustPath` | R/P 缺失；Go 开发接口，无 Runtime/CLI 模型动作。信任路径不授予 Hermes pairing、发信范围或工具执行权限 |
| 身份、公钥、签名、加解密与会话/DR 存储 | [crypto](../../crypto)、[session/session.go](../../session/session.go)、[dr](../../dr) | R 部分（仅少量 helper 命令）；底层密码学组件不证明当前 HTTPS MQ 具备 DR 前向安全 |
| libp2p 节点、地址连接、DHT 查询/引导与诊断 | [libp2p/host.go](../../libp2p/host.go)、[dht/dht.go](../../dht/dht.go) | R 部分架构图；SDK 基础设施，不是好友管理或个人协作的独立模型能力 |

## 七、本次新增：简洁加好友文案

**新增 `export_contact`**，区别于第六节原有 Go SDK 长名片。用户需要的是可直接复制的一句话：包含指定 agent 的真实 URN、实际云平台地址和新人介绍链接，支持自己或一个已确认的联系人。

| 新能力 | 原基线 | 本次实现入口 | skill 对应与边界 |
| --- | --- | --- | --- |
| 导出自己的加好友短文案 | Runtime/helper 均无此能力；只有 Go SDK 多行密钥卡 | `agent_comm_collaboration` / `Runtime` 的 `action=export_contact` | 新根 skill [加好友文案](../../SKILL.md#export-contact)及 P 的导出说明；输出文本，不发消息、不加好友、不创建授权 |
| 导出已确认对方的加好友短文案 | 原 `resolve_contact/state` 可读已确认 URN，但无专用导出格式 | 同一 action 选择已确认 `contact_id` | 单个明确对象；不遍历或披露对方的好友关系，不把未知 URN 包装成已确认联系人 |
| 本机命令行导出短文案 | 原参考宿主只接受交互式 JSON 或离线 demo | `python -m agent_comm_runtime.reference --state <db> --agent-urn <own-urn> --platform-url <actual-url> --export-contact [contact_id]` | 新增只读非交互出口；默认导出自己，对方从该 owner 的已确认记录读取 |

实现见 [runtime.py](../../python/agent_comm_runtime/runtime.py) 的 `export_contact` 分支、[store.py](../../python/agent_comm_runtime/store.py) 的 `contact_urn` 和 [contact_export.py](../../python/agent_comm_runtime/contact_export.py)。`contact_id` 默认 `self`；自己可使用宿主明确配置的 `public_platform_url`，也可明确传 `platform_url`；导出对方时必须提供已确认的准确 `contact_id` 及该 agent 的 `platform_url`。结果含 `status=exported`、`text`、`urn`、`platform_url`、`introduction_url`，直接向用户返回 `text` 即可。

短文案中的平台应来自实际可信配置或用户明确提供的信息，不能用本机 `http://127.0.0.1:45042` 作为公网平台，也不能把安装默认值当作已核实的部署地址。现有好友记录没有平台字段，不能套用自己的平台。校验拒绝 loopback/未指定主机地址，允许真实局域网 HTTP 部署；导出不访问网络核实地址。缺失信息会拒绝生成，不猜默认值；结果不包含本地别名或私钥。介绍链接指向项目 README，便于尚未接入的人了解和开始。完整使用方式见新根 skill [加好友文案](../../SKILL.md#export-contact)与 P 的「Export a friend invitation」；本节不把新增入口计入基线 15 项。

`self` 现为本方身份保留 ID，不能新建为好友 ID；本主人旧库存在同名绑定或待确认记录时，
导出明确拒绝并提示换 ID 确认好友、由宿主迁移旧绑定，不静默修改身份或删除数据。
可选的本方平台配置只在实际导出时校验，配置错误不影响查询状态或撤销委托。

## 八、仍未提供的能力与需纠正的旧结论

- **未提供**：默认完整记忆导出/自动写回、当前 Hermes 后台私人会话唤醒、通用日历写入、支付、任意操作系统执行；这些不能靠补写 skill 变成已实现能力。
- **没有独立端到端任务状态/取消/进度服务**。`kind=cancel` 是应用数据，`accepted` 是本机落盘，`platform_queued` 是平台入队；应用成功必须看具体对方回复及业务约定。
- **当前可靠 helper 出站采用 HTTPS MQ**；传统 SDK 的 DR/P2P 仍存在。原根 skill 把所有 helper 消息描述成“首选 P2P、Double Ratchet 前向安全”不符合当前实现。
- **本机消费 ACK 与平台 ACK 分层**。连接器处理完成或持久接管后确认本机 helper；helper 已验证且持久保存入站后确认平台。不能照旧根技能直接向平台销毁消息。
- **加好友文案不是信任或授权凭据**。复制文字、导入公钥名片、本地绑定别名、允许通信、主人工作台配对和具体对外发信仍是不同动作。

维护约定：以后新增 Runtime action、业务 capability、remote method、helper endpoint 或 CLI 命令时，同时更新对应 skill 和本表；可选端口与部署开关必须写明。保留本次基线覆盖结论，以便区分“原已实现的文档缺口”和“本次/后续新增实现”。

## 九、本次验证（2026-09-15）

- Python runtime 全量 **65 项通过**，其中新增导出专项 **25 项**：自己/已确认好友、跨主人隔离、单行 CLI、平台地址、只读行为、旧 `self` 冲突及配置错误不影响撤销。
- Hermes connector 全量 **116 项通过**，使用本机源码 `110baa095bc7135a0624557a9cc35df0f98ece0f` 和临时 profile。新测试覆盖真实原生上下文、公共平台配置传递、导出无需确认及不使用 loopback 地址。
- 既有声明支持的 Hermes `b6b53c69a6ed49cb099cf1bfe76b5e6edd718e5a` 隔离源码也完成 116 项回归；确认交互测试兼容旧事件协议和新版 JSON-RPC server request。更新的是测试中的界面模拟，生产确认权限保持原有校验。
- 根 skill 中英文与随包 `personal-collaboration` 的 frontmatter/引用检查通过。所有运行使用临时数据与测试 helper，没有向真实好友发送消息。

这些是当前工作区的源码与 skill 修订；未发布新的 wheel/安装包，也未更新运行中的 Hermes 或公网服务。
