# Hermes 平台插件

本插件连接本机 `agent-comm-helper`，由 helper 负责密钥、加密和当前 HTTPS MQ 可靠传输。`platform_url` 必须是本机 loopback HTTP 地址（默认 `http://127.0.0.1:45042`），不可填写云端 platform URL。

## 个人协作模式（1.5.4，可选）

本版本通过独立 `agent-comm-runtime` 包提供本地协作内核，
`agent_comm_collaboration` 是它的 Hermes 原生适配工具，优先用于 Hermes
桌面/Web 的主人原生对话。联系人、不可变资料快照、事项委托、待决定问题、
方案版本及发送记录保存到当前 profile 的 SQLite；不要求统一宿主记忆。

在下方现有安装配置的 `platforms.agent_comm.extra` 中增加：

```yaml
collaboration_enabled: true
# 可选；用于导出自己的加好友文案，填 helper daemon 实际使用的平台地址。
# public_platform_url: https://agent-communication.online
# 可选；默认当前 HERMES_HOME/agent-comm/collaboration.sqlite3
# collaboration_state_path: /absolute/path/to/collaboration.sqlite3
```

同时保留本机 `platform_url`、正确的本方 `urn` 和明确的 `allow_from`，不要覆盖
其它平台或插件配置。Gateway 与桌面/Web 后端须安装同一插件版本并使用同一
profile/协作库。若在 Hermes 工具设置中主动禁用了 `agent_comm_collaboration`
工具组，需要按原设置流程启用它。运行中的服务需要重启以加载一致配置。

这一选项默认关闭。**开启后，远端消息只持久入库，不直接启动具有私人上下文的
Gateway LLM；普通 adapter 直接发送被阻止。** 主人原生对话通过协作工具的
`inbox` 读取来信，使用 `prepare_action` / `dispatch` 在授权范围内继续。
本版本不在后台唤醒桌面会话。关闭模式时保留下文描述的传统 Gateway 消息处理。

Hermes 原生会话中的首个联系人绑定和事项范围通过自己的 `clarify` 问题卡确认。用户在
**该问题的文字回答框**输入“可以”或“同意”；主聊天输入框的文字在当前 Hermes
中会跳过问题并开始新回合，不会批准旧请求。模型只能传 `approval_id`，不能
传主人身份、`approved` 或回答正文。回调返回后重新验证连接、原生会话与回合；
超时、关闭、条件回答、中断、子 agent 或外部平台上下文均不产生授权。

新版源码另支持已配对的 Web 工作台：`contacts.add` 接受用户在联系人表单中
确认的绑定，`approval.respond` 接受用户对 agent 生成的具体待确认请求的同意或
拒绝。两项方法须在本机分别明确授权，主人主体来自本地配对，普通聊天、联系人
信任或 `allow_from` 不授予此权限。结果写入同一协作库，并由 Web 读取同步；
普通消息和好友响应的确认会提交相应的持久 outbox；协作动作仍按其 dispatch 流程执行。
Web 已处理的问题不能再被迟到的原生 callback
覆盖。已有配对不会随升级自动增权，需安装匹配的 runtime 与 Web，并显式重配。
安装包用户可查看[配对升级步骤](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md#4-配对远程-web)。

问题卡的等待上限直接取自该次 runtime 授权租约（最多 360 秒，且不超过事项
期限及宿主更短的问题等待设置）。到期、中断、回合变化或事项失效时，通过
Hermes 的单请求 `request.cancel` 关闭这张卡，清除租约后可重新展示仍有效的
问题；迟到回答不会授权。此适配不修改宿主全局 timeout，也不延长事项权限。

处理会话名称带宿主生成的持久会话标识，同类待办及并发草稿不会因标题重复
而卡住。创建、命名、关联或打开失败时会说明具体阶段；重试继续核对原草稿，
不会因为失败再提交一次已有背景。

带入会话的恢复说明分别给出完整合法的 `state`、`attention` 和条件性 `confirm`
调用。事项 ID 等参考信息只用来匹配结果，不混入工具参数；有效性由最新源端
状态和原生确认校验决定，旧问题展示期限不会被模型误当成新的任务授权边界。

有效委托内的结构化动作自动允许；范围变化询问具体差额；不完整动作先澄清；
无效或不支持的能力拒绝。当前支持候选时段、指定资料、会议提议、接受已登记
方案，以及每次确认确切全文的自由文本。工作日下午等限制必须编码成
`allowed_windows` 的具体时间区间，不能只写进目的描述。候选披露按收件人在
本事项累计；金额支付、日历写入及任意执行工具尚未接入。

让 agent 先读随包提供的 `agent_comm:personal-collaboration` skill，再调用
`state` 恢复状态。不同原生对话可恢复同一 profile 的联系人与任务；待决问题
需要在当前原生对话重新展示。未结束的原生问题在租约释放/到期前不能重开。
`sending` 使用同一个 `operation_id` 续发，单次最多处理四位尚未接收的收件人；
`accepted` 仅代表本机 helper 队列接受，不是对端接受或会议创建成功。

此桥已针对 Hermes `b6b53c69a6ed49cb099cf1bfe76b5e6edd718e5a` 的真实原生接口
做隔离测试；依赖内部原生会话接口，宿主升级后需要重新验证，缺少接口会拒绝
执行。组件只约束经由自身的路径，不能阻止具有任意 shell/文件/网络权限的
模型绕过本地工具；开启它也不是任意文本语义或第三方资料权限的自动证明。

通用内核与可扩展 Host / Memory / Interaction / Transport 合约见
[SDK Python Runtime](../../python/README.md)。宿主桥接位于
`hermes_platform_agent_comm/collaboration/hermes.py`，原生操作示例与完整
scope/payload 规格见随包的 `skills/personal-collaboration/SKILL.md`。测试仍使用
文末命令，涵盖策略、SQLite 恢复/并发/崩溃、原生 callback、helper HTTP 与
传统 connector 兼容性；不会修改真实 profile、调用模型或向真实对端发送消息。

## 协作待办与纯提醒 companion（N1）

随 Python wheel 提供 `agent-comm-attention` Hermes 插件包，安装配置工具默认部署并启用它，包含 Desktop
前端与 dashboard 认证 API。它每 15 秒读取当前连接/profile 的持久待办，不启动
LLM，不发送对端消息，不申请或消耗原生确认租约。它提供：

- 永久“协作待办”侧栏页和状态栏分别显示未读与待处理数量；普通来信和完成记录
  显示在“最新进展”。任何开放事项都可标为已读，授权卡已读后仍待处理；解决、
  撤销或过期后按新 revision 更新。
- 好友请求直接显示为待处理项，可进入本机对话接受或拒绝。普通消息的“标为已读”
  写回 agent 数据库；Web 或本机处理后的提醒会在另一端下一次同步时关闭。
- 前台应用内提示和后台 OS 原生提醒；只发送程序生成的计数摘要，不把来信正文
  或资料放进锁屏通知。首次同步历史普通消息不弹提醒，未解决的决定和恢复项仍展示。
- 按连接/profile/owner/item revision 持久保存**提醒尝试**水位，用 Web Locks
  序列化多个窗口的 claim。OS 偏好、焦点、重连基线可能抑制通知，claim 不代表送达。
  缺少 Web Locks 时保留中心和计数，明确显示系统提醒不可用；不静默假装已提醒。
- 点击仅导航。“复制指令并打开原生对话”会先重新同步，再复制恢复请求并打开
  同 profile 的既有原生会话；会话不可恢复时打开新对话。用户自行发送恢复请求，
  由现有原生 question callback 展示、收集确切决定。已读和关闭通知都不等于批准。

导出到一个**尚不存在**的目录进行检查：

```sh
python -m hermes_platform_agent_comm.companion_export --output ./agent-comm-attention
```

也可调用安装 wheel 后的 `agent-comm-hermes-companion --output ...`。导出不会
修改 Hermes 配置、启用插件、重启服务或发送提醒。包内 `desktop/plugin.js` 是
Hermes 的纯 ESM 运行时插件，`dashboard/manifest.json` 声明 profile 受控 API。

部署时将检查后的目录安装到 Hermes 的可信用户插件目录
`<Hermes root>/plugins/agent-comm-attention/`；保留已有插件和配置。通过 Hermes
的插件设置启用 `agent-comm-attention` 的后端；Desktop contribution 默认启用，保留用户
显式关闭的偏好。Gateway/dashboard 使用的 Python 环境均须
安装当前 runtime 与 connector wheel。重启 dashboard 加载 API；Desktop 重载
插件。当前 profile 需启用 `collaboration_enabled` 或 `remote_enabled`。

只读接口为 `GET /api/plugins/agent-comm-attention/attention?after=0&limit=100`；
可选 `profile` 由 Hermes 自身解析并限定到该 dashboard owner 管理的 profile。
后端使用 Hermes `_require_token` 和 `_config_profile_scope`，不接受 owner、
任意文件路径、审批答案或 token 参数；不增加自己的认证旁路。响应使用
`agent-comm-attention/v1` 并添加 `owner_key` 与只供导航的 `resume` 信息。

Desktop 必须运行且连接到对应 profile 才能轮询和触发系统提醒；关闭应用或
切换其他连接后，待办仍保存在 agent 数据库，重连后重新同步。独立网页或移动端
推送不由此 companion 提供。旧的 Hermes 内部接口缺失时 API 明确 unavailable，
不会降级为无认证访问或启动私人模型。
当前 Hermes SDK 未提供撤回已经投递的 OS 通知接口；跨端处理会关闭 agent 的待办状态、
本机未读计数和后续提醒，但操作系统通知中心已经展示的历史条目由宿主管理。

验证（全部使用临时数据，前端通知使用 fake host）：

```sh
python -m unittest discover -s connectors/hermes-platform/tests -p 'test_attention*.py' -v
node --experimental-vm-modules connectors/hermes-platform/tests/test_attention_desktop.mjs
```

## 导出加好友文案

在主人原生对话中说“导出我的 agent 加好友文案”或“导出小王的 agent 加好友文案”。
工具 `agent_comm_collaboration` 的 `action=export_contact` 返回简洁 `text`，包含
目标 URN、platform 地址及新人介绍/接入链接。导出本身无需额外确认，也不会自动发送。

```json
{"action":"export_contact","platform_url":"https://agent-communication.online"}
{"action":"export_contact","contact_id":"wang-work","platform_url":"https://agents.example.org"}
```

使用实际平台地址替换示例。自己的 `contact_id` 默认 `self`，身份取自配置 `urn`；
设置了 `public_platform_url` 后，本方导出可省略工具参数 `platform_url`。
**配置中的 `platform_url` 仍然只指向本机 helper**，不能将它用于对外文案。
当前 helper `/info` 不提供平台地址，须从 daemon 启动配置或用户提供的信息取得。
指定好友需要当前 profile 已确认的 `contact_id` 及其明确的 `platform_url`，
不会自动套用自己的平台。文案不代替联系人绑定确认、`allow_from` 或工作台配对。

## 宿主与记忆扩展

`describe` 返回当前注册的端口和支持能力；未安装的记忆、通知、唤醒等能力明确返回
`unsupported`。Hermes 默认使用真实桌面/Web 主人会话及原生问题卡；第三方宿主可以
独立实现通用端口，运行 `python -m agent_comm_runtime.reference --demo` 查看参考适配器。

Hermes 不默认读取整套记忆。若已有第三方 memory adapter 包，可由主人配置：

```yaml
collaboration_memory_adapter:
  name: my-graph
  options:
    selected_collection: collaboration
```

它对应 `agent_comm_runtime.adapters` 下明确安装的一个 entry point；提供有限查询和
指定快照，来源/版本随资源登记保存。未配置时工具不遍历记忆；仍可由宿主准备明确
资料并使用 `register_resource`。完整 adapter 开发方式见 [Runtime 文档](../../python/README.md)。

远程工作台采用独立 owner 配对，配置键为 `remote_enabled`（默认 false）和
`remote_state_path`；普通联系人的 URN/allow_from 不能代替主人工作台配对。
远程读写方法、安装命令及配对方式以 Runtime 的 remote CLI 和 agent 实际返回的
capabilities 为准。不要同时启动 standalone remote 消费器和同一 helper 的 Hermes
remote 消费器。

启用远程模式后，连接 helper 时即恢复持久会话队列，无需等待新的 RPC：尚未启动的
回合重新校验当前配对后继续执行；进程退出时已在运行的回合标记为 `interrupted`，
避免在无法判断工具副作用的情况下自动重做。

通过配对校验的远程回合附带固定的宿主会话说明，向模型提供已验证的来源和授权边界。
该说明允许主人或其控制的 agent 通过工作台对话；它不声称每条消息都由主人手工发送，
不授予原生审批或额外工具权限。普通对端消息及模型传入参数无法设置该可信说明。

配对包含 `collaboration.execute` 时，远程聊天中的 `agent_comm_collaboration`
与原生对话调用同一个 Runtime，支持联系人请求、消息、资料、委托和协作动作。
同名 RPC 的 params 就是工具参数，例如 `{"action":"describe"}`；其 `action_fields`
列出实际支持的动作及必需/可选字段，Web 可据此提供相同功能。权限只读的旧配对
仍然只读，升级不会隐式增加权限。需要确认的远程动作返回持久审批，用户从 Web
问题卡通过 `approval.respond` 决定；模型的 `confirm` 只能读取已作出的决定。

好友请求、响应与普通消息由同一 agent SQLite 持久 outbox 驱动；Gateway 定期重试，
不依赖下一封来信触发。已建立连接的联系人每约 30 秒更新在线观察，过期状态显示
unknown。远程回合的工具权限绑定到真实宿主回合，撤销配对或回合结束后不能继续调用。

## 安装与升级

要求 Python 3.11+、Hermes Gateway 提供 `connect(is_reconnect=...)`、`MessageEvent.allow_gateway_control` 和 `on_processing_complete` 钩子，以及包含持久 inbox/outbox API 的新版 helper。已用 Hermes `b6b53c69a6ed49cb099cf1bfe76b5e6edd718e5a` 的真实适配器基类进行隔离测试。

1. 先更新并启动 helper。保留原密钥目录及其 inbox/outbox 数据库。
2. 在 **运行 Hermes Gateway 的同一个 Python 环境**中安装此目录：

   ```sh
   python -m pip install /path/to/agent-comm/python /path/to/agent-comm/connectors/hermes-platform
   ```

   两个源码目录需要在同一命令安装；发行时同时提供 runtime 与 connector wheel。不要同时保留同名 pip entry point 和用户目录插件；新的独立 runtime 是必需依赖。

3. 先通过 Hermes 的 `hermes_constants.get_hermes_home()` 确认实际 profile 目录；它可能不是 `~/.hermes`，例如 Windows 上可位于 `%LOCALAPPDATA%\hermes`。按当前 Hermes 的配置流程合并以下内容，保留其他平台和插件配置：

   ```yaml
   plugins:
     enabled:
       - agent_comm
   platforms:
     agent_comm:
       enabled: true
       extra:
         platform_url: http://127.0.0.1:45042
         urn: urn:agent-comm:agent:YOUR_LOCAL_IDENTITY
         allow_from:
           - urn:agent-comm:agent:EXPLICITLY_ALLOWED_PEER
         # 可选；默认在当前 HERMES_HOME/agent-comm/ 下按 helper 区分。
         # state_path: /absolute/path/to/hermes-receipts.sqlite3
         reconcile_interval: 5
         retry_delay: 30
   ```

   `allow_from` 由 Hermes 原生授权流程读取。必须填写明确允许通信的对端 URN；联系人信任等级不会自动赋予 Hermes pairing、工具执行或控制权限。新插件不自动修改 pairing store。更新已有安装时，旧版本此前写入的 pairing 授权仍由 Hermes 保存，需由操作者按自己的授权意图检查、撤销。

4. 按当前 Hermes 的服务管理方式重启 gateway，确认真实 SSE 连接建立后状态为 connected。停止/重连会关闭 HTTP response、取消并回收 reader task。helper 未启动或返回非 SSE 时，插件不会报告连接成功。

升级插件时保留 `state_path` 对应数据库及其 WAL 文件；其中保存已经处理完成的 wire message ID 和回复路由。每个 helper inbox 只部署一个活跃消费插件，并为不同 Hermes profile 分配不同 helper 身份和数据目录。

## 消息契约

| 字段 | 语义 |
| --- | --- |
| `message_id` | helper/wire 的稳定 ID；重复消息和重连重放共用该 ID，禁止用接收时间代替 |
| `conversation_id` | 同一对端下的独立 Hermes thread；由带类型前缀的哈希映射，原值保存在事件 metadata |
| `task_id` | 关联任务；无 conversation_id 时作为独立 thread；不是执行权限 |
| `in_reply_to` | 回复的 wire message ID；自动回复继承会话、任务、deadline |
| `kind` | 保留给应用层的类型字符串；默认 `message`；自动任务回复使用 `result` |
| `deadline` | 带时区的 RFC3339 时间；到期入站记录 expired 并 ACK，不启动处理；到期自动回复被抑制 |
| `hop_limit` | 默认为 8；回复减 1；入站 0 仍可处理，但不会自动回复；回复不能提高继承预算 |

helper API 的 `message_id` 仅允许 1–128 个 ASCII 字母、数字、`.`、`_`、`:`、`-`；关联 ID 与 `kind` 最长 256 UTF-8 字节；`hop_limit` 为 0–64 的整数。

入站 `source.is_bot=True`、`internal=False`、`allow_gateway_control=False`。外部 `/approve`、`/restart` 等文本只作为对话内容，不能解析为 Gateway 控制命令或审批答复。`kind=cancel` 等仍是应用数据，本插件不会据此取消运行或改变权限。

事件 metadata 的 `agent_comm` 字典保留上述关联字段；不会把来自对端的任意字段混入 Hermes 控制 metadata。默认会话属于 `agent_comm` 平台，与桌面/CLI transcript 分离。同一 conversation_id 下不同 task_id 共用会话；没有这两个字段的旧消息继续使用该对端的 DM。

主动通知若没有 `reply_to`、也不属于当前正在处理的回合，宿主应在 `metadata.agent_comm` 中显式携带原始 `conversation_id` / `task_id`；仅传 Hermes 的哈希 `thread_id` 无法恢复原始 wire 会话。

## 可靠性与发送状态

helper 持久 inbox 是未消费消息的来源。插件同时通过 SSE 和 `GET /api/v1/mq/retrieve` 补拉 pending 消息，按 wire ID 阻止重复并发处理。消费串行进行，避免 Hermes 的忙碌会话合并多个消息时丢失独立 ID；模型回合较长时其他消息会继续保留在 helper inbox 中等待。

`handle_message()` 返回只证明内存调度，**不会触发 ACK**。只有真实 `on_processing_complete(SUCCESS)` 后先把完成 receipt 写入 SQLite，待 Hermes 清理该回合后才 `POST /api/v1/mq/ack`。没有 handler、拒绝调度、失败或取消均保持 pending，默认至少等待 30 秒再重试。若写入完成 receipt 后本机 ACK 失败，重放只补 ACK，不再执行模型。

这是至少一次处理。若工具副作用发生后、完成 receipt 落盘前崩溃，恢复时可能再次处理；任务执行者仍需要用 `task_id`/`message_id` 实现副作用幂等。Hermes 原生处理流程也可能正常完成一个被其授权或 bot-loop 规则拒绝的事件；这里的 processed 表示 Gateway 已完成该事件的处理，不表示业务任务成功。

出站请求为 `POST /api/v1/mq/store`，以明确的 `success:true` 和稳定 `message_id` 确认 helper 接受；HTTP 2xx 本身不代表成功。`SendResult.raw_response.status=accepted` 只表示本机持久出站队列接受，可通过 helper `GET /api/v1/mq/status?message_id=...` 查询后续状态，不能当作对端执行完成。

回复重试按触发 wire ID、路由与内容生成稳定出站 ID。主动发送的调用方可在 `metadata.agent_comm.message_id` 或 `metadata.message_id` 提供自己的重试键；同一次调用的网络重试始终复用它。主动发送需要跨调用重试时，应显式保留该 ID。

达到 hop/deadline 限制的自动回复不会向 helper 投递；对 Hermes 返回本地成功完成、`message_id=None`、`raw_response={status: suppressed, reason: hop_limit|deadline}`，避免 Hermes 将同一已执行任务反复重做。显式新发送过期消息仍返回失败。

## 验证

### 待办详情与原生处理会话

可选 companion 1.1.0 的「查看并处理」在当前 profile 下刷新事项、恢复关联会话或创建新的原生处理会话，然后只提交核对上下文和展示问题的请求。点击不等于同意，不调用 `dispatch`。联系人审批、远端工作台发起的事项和无 task 的来信都有独立映射；同一 task 的后续事项共用处理会话。只有宿主明确证实会话不存在才允许建立替代映射；网络错误不会创建替代会话。

所有 companion API 都经过 Hermes dashboard token 和 profile 解析，客户端不能提供 owner。`Store.attention` / Web 远端 `attention.list` 仍只返回固定安全摘要；仅本机已鉴权详情含范围、原生问题和对端原文。系统通知不使用这些详情字段。

| API（相对 `/api/plugins/agent-comm-attention`） | 作用 |
| --- | --- |
| `GET /attention` | 增量安全事项及当前 owner 可读详情、原生会话导航信息 |
| `GET /attention/{attention_id}/detail` | 展开时刷新当前详情与 worker 预算，不改变通知水位 |
| `POST /attention/prepare-resume` | `{attention_id,revision}`；核对仍开放，产生幂等处理请求 |
| `POST /attention/bind-session` | `{resume_id,stored_session_id}`；验证真实 profile 原生会话，返回持久绑定胜出者 |
| `POST /attention/claim-submit` | `{resume_id,stored_session_id}`；只能一次占用恢复请求提交，返回 `claim_token` |
| `POST /attention/finish-submit` | `{resume_id,claim_token,outcome}`；记录 `submitted` 或 `uncertain`，没有授权含义 |

前端使用 Hermes 受支持的 `session.create`、`session.title`、`host.openSession` 和 `prompt.submit`。恢复请求提交超时或崩溃后只重开已绑定会话，不自动重发背景消息。源 revision 改变或到期时 prepare/claim 返回 409，要求先刷新。处理请求账本与原生确认 lease 分开保存；后者仍必须由当前真实 `native_context` 发起和消费。

### 有限后台会议程序

runtime 0.1.2 增加 `prepare_worker_policy`、`pause_worker`、`revoke_worker`。一项已生效 task 和一个双方已加入的 collaboration 可以先准备具体 policy，再由本人通过现有 `confirm` 原生问题启用。启用后 Gateway 每轮 reconcile 最多运行一个 task step；无新消息时也能推进。无需唤醒私人模型。

policy 明确绑定 `collaboration_id`，并包含 `allow_propose`、`allow_accept`、`proposal`、`max_runs`（1–100）、`max_sends`（1–32）、`interval_seconds`（15–3600）、`expires_at`。提议仅能发送原生卡展示的固定第一版方案；自动接受只处理符合原 task 范围的当前结构化方案。`allow_accept=true` 的含义会写进原生确认卡，不能从先前排会授权默默推导。预算涵盖固定协议回执，且仍受已有维护许可限制。

每次发送先持久保留一个运行和发送预算，再检查 policy revision、task revision、有效期与暂停/撤销状态；实际投递前再次检查。超范围会产生具体原生审批待办并暂停；未知发送结果会暂停且不自动重发。恢复或修改策略需要新的原生确认，不能仅改配置复活旧许可。程序不自动邀请或加入新协作，不发任意文本，不访问记忆、模型或外部工具，不创建日历。

`state.tasks[].worker` 和 owner 详情显示策略、使用量、状态及等待原因。单独暂停 worker 不改变业务委托；撤销 task 会阻止后续业务发送。

在安装了 Hermes 与本插件依赖的 Python 环境运行：

```sh
PYTHONPATH=/path/to/hermes-agent:/path/to/agent-comm/python:/path/to/agent-comm/connectors/hermes-platform python -m unittest discover -s connectors/hermes-platform/tests -v
```

PowerShell：

```powershell
$env:PYTHONPATH = 'C:/path/to/hermes-agent;C:/path/to/agent-comm/python;C:/path/to/agent-comm/connectors/hermes-platform'
$env:PYTHONDONTWRITEBYTECODE = '1'
& 'C:/path/to/hermes-agent/venv/Scripts/python.exe' -m unittest discover -s connectors/hermes-platform/tests -v
```

测试在临时 HERMES_HOME 下使用真实 Hermes 适配器基类、原生后台处理生命周期及本机模拟 HTTP/SSE helper；不启动真实 Gateway，不调用模型，也不修改真实 Hermes 配置。测试涵盖连接失败、关闭 reader、重连、处理完成前不 ACK、稳定 ID 去重、取消恢复、ACK 失败恢复、串行接收、授权标记、会话隔离及关联字段回复。
