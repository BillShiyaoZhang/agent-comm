# Agent Comm 本地协作 Runtime

`agent-comm-runtime` 0.1.7 是可独立安装的 Python 3.11+ 包，标准库即可运行。它不导入 Hermes、模型 SDK 或任何记忆库。联系人、授权、任务、不可变动作、资源快照、入站和审计的唯一实现位于这里；Hermes connector 是它的首个实际宿主适配器。Web 通过明确配对的远程控制协议同步 agent 侧状态；拥有对应权限后可直接添加联系人和确认授权，所有决定仍写入 agent 的同一个 Store。

## 安装与独立运行

在 SDK 根目录运行，使用目标宿主的实际 Python：

```sh
python -m pip install ./python
python -m agent_comm_runtime.reference --demo
python -m unittest discover -s python/tests -v
```

`--demo` 使用临时 SQLite 与少量示例资料，演示能力发现、状态查询、有限记忆检索和资源登记，不发信、不产生授权。交互式参考宿主可用：

```sh
python -m agent_comm_runtime.reference --state ./collaboration.sqlite3
```

输入 `{"action":"describe"}`、`{"action":"state"}` 等 JSON。确认发生在同一进程的独立文字问题内，必须使用本地主人的交互终端。不要把该进程的标准输入接到模型输出或远端消息。`--helper-url` 和 `--memory-json` 都只有显式传入才启用对应适配器。

源码运行尚未安装时把 SDK 的 `python/` 加入 `PYTHONPATH`。Hermes 插件开发测试还需要 Hermes 源码路径；安装后的发行包不需要修改 `sys.path`。

## 系统边界

```mermaid
flowchart LR
    H["任意宿主 Agent"] --> HP["HostPort<br/>当前主人、会话、回合验证"]
    U["主人原生沟通渠道"] <--> IP["InteractionPort<br/>确认问题与可信原始回答"]
    M["该宿主的记忆系统"] --> MP["MemoryPort<br/>显式检索与有限快照"]
    HP --> R["Runtime dispatcher"]
    IP <--> R
    MP --> R
    R <--> S[("Store<br/>联系人、事项、权限、审计")]
    R --> P["确定性策略与消息编译"]
    R --> TP["TransportPort"]
    TP --> G["Go helper<br/>身份、密钥与持久收发"]
```

扩展协议版本 `1.0`、Python 包版本 `0.1.7`、协作消息 `agent-comm-collaboration/v1` / `v2`、SQLite schema `1` 是不同版本维度。当前 adapter API 要求版本精确相同；未来改变合约时显式升级，避免静默兼容猜测。v2 与 attention 使用新增记录类型，旧 v1 行保留；原 `hermes-native-<profile hash>` 主体仍能读取旧记录。不能用旧二进制继续处理已建立的 v2 协作。

## 双边协作与持久提醒

v2 的模型入口是 `prepare_collaboration`，必需字段为 `task_id`、`collaboration_id`、`operation_id`、`kind`、`payload`。本方与对方各自使用自己的 task_id，只有 collaboration_id 在网络上共享。邀请和加入各自取得主人确认，可使用原生渠道或具有 `approval.respond` 权限的已配对 Web 控制台；模型与对端都不能提交主人身份或同意答案。

| kind | payload 与行为 |
| --- | --- |
| `invite` | `{ "peer_id": "已确认的本地联系人ID" }` |
| `join` | `{ "message_id": "已认证入站邀请的消息ID" }` |
| `proposal` / `change_request` | 与 `propose_meeting` 相同的方案字段；本地参与人 ID 转为绝对 URN。发起方发布连续版本，对方提出修改请求。 |
| `accept` / `withdraw` / `cancel_request` / `cancel_ack` | `{}`；从当前本地事实编译确切版本及引用，不能额外携带授权声明。 |
| `agreement` / `agreement_ack` / `sync_request` / `sync_response` | `{}`；固定模板维护消息，须有主人批准的独立有限许可。 |
| `receipt` | `{ "event_id": "已保存事件ID" }` |

准备结果若为 `ask`，通过已有 `confirm(approval_id)` 展示原生问题，或在已授权的 Web 控制台查看同一问题并作决定；获准后通过 `dispatch(operation_id)` 发送。`accept` 发出的协议事件只带条款摘要，因此原生/Web 确认问题另从本机已验证的当前方案快照展示方案编号、版本、主题、双方 URN、UTC 起止时间及线上约定、仅本人参会的边界。若旧卡只有摘要，不应凭摘要同意；更新 Runtime 后用新的操作 ID 重新准备，旧审批不会被改写。`collaborations` 查看双方阶段、等待原因和待发操作。`state` 的 `collaboration` 字段提供相同投影。v1/v2 业务动作共享任务累计预算，维护许可最多 32 条、最多 7 天，并在邀请/加入确认中明确展示。

本阶段由宿主主动恢复、调用 `inbox` 接收和 `dispatch` 逐段驱动；协议可生成固定维护待发项，但没有后台私人模型自动协商。`accepted` 只代表本机 helper 接受消息；双方同版显式接受、发起方形成持久约定并完成 ACK/回执同步后才到 `closed`。首版只协调线上会议方案（`agreement_only`），没有日历写入。对方代表权标注为 `peer_attested`，不冒充独立核验的人类签名。

`revoke_collaboration_maintenance(collaboration_id)` 可单独停止维护许可。原业务委托过期、撤销或预算耗尽后的 `withdraw`、`cancel_request`、`cancel_ack` 必须重新取得主人的 900 秒单事件恢复许可；它不增加原委托预算，也不恢复原业务权限。

`attention` 返回 `agent-comm-attention/v1`：`items`、整数 `cursor`、`has_more`。参数 `after` 为非负游标、`limit` 为 1–100。业务写入与提醒投影在同一 SQLite 事务中提交；每个事项只有最新状态，revision 可跳号，关闭状态保留为 tombstone。重复消息不重新提醒；拉取或标为已读均不会产生授权。

远程读取需在本机配对中明确增加 `attention.list`，旧配对不会自动扩大。Hermes companion 和 Web 通知中心读取这些持久事实；通知渠道的尝试不代表系统已经显示或用户已经阅读。

## 已实现的扩展端口

端口定义、注册验证和实际调用分别在 [ports.py](agent_comm_runtime/ports.py)、[runtime.py](agent_comm_runtime/runtime.py)。端口是代码接口，不是向网络开放的 TCP 监听端口。

| Port | 能力名与方法 | 核心必须得到的保证 |
|---|---|---|
| HostPort | 必需 `owner_context`: `capture(context)` / `revalidate(session)` | context 来自宿主持有的可信对象；稳定 principal、临时 session 和 turn 分开；关闭、换回合、代执行者变更后拒绝旧操作 |
| HostPort | 可选 `wake`: `wake(session, task_id)` | 宿主自己决定如何安全恢复事项；当前 Hermes 未实现，不自动启动后台私人 LLM |
| MemoryPort | 可选 `search(session, query, limit)` | 必须显式 query，1–20 条候选；只返回 reference/title/summary，不自动绑定网络身份 |
| MemoryPort | 可选 `read_snapshot(session, reference, max_chars)` | 按明确 reference 返回不可变有限文本、来源与版本；1–8000 字符，不完整则拒绝或让调用方另选更小记录 |
| InteractionPort | 可选 `confirmation`: `request_confirmation(session, question)` | 展示确切完整问题；返回该问题的主人原始回答；不能让模型传入 approved 或答案；不支持时明确返回 unsupported |
| InteractionPort | 可选 `notification`: `notify(session, text)` | 宿主自行实现通知渠道；由可信调度代码显式调用，不因收件自动执行 |
| TransportPort | 可选 `durable_mailbox`: `store(body)` / `retrieve()` / `ack(message_ids)` | 身份验证由可信客户端完成；稳定 message_id、先持久接管后 ACK；HelperTransport 为已实现的本机 HTTP 适配 |

`HostSession(principal_id, session_id, turn_id, opaque=...)` 中 `opaque` 保留实际宿主句柄，不序列化给模型。核心拒绝模型参数中的 owner/context/raw_response 等额外字段。不同原生对话可共用同一 profile principal；不同人的 profile 必须使用不同主体及实际隔离边界。

`AdapterRegistry.register(adapter)` 验证版本、端口、能力与对应可调用方法；同一 registry 不允许替换已注册端口。未知能力和缺失方法立即报错。`registry.require`、`registry.invoke` 对未注册能力返回 `Unsupported`，不自动选择其它渠道、不自动降为通用文本执行。`Runtime.dispatch` 将不可用扩展稳定返回 `{"status":"unsupported",...}`。

可选的 wake/notification 通过可信宿主代码的 `registry.invoke(port, capability, method, ...)` 调用。公共模型 action 不包含任意 method 调用。当前没有通用后台调度器，声明接口不会让 agent 自动具备后台推进能力。

## 实际注册与调用

[reference.py](agent_comm_runtime/reference.py) 是完整可运行参考实现，可替换 TerminalHost、TerminalInteraction、FiniteMemory 中任意一项。最小装配如下：

```python
from agent_comm_runtime import AdapterRegistry, Runtime, Store
from agent_comm_runtime.reference import TerminalHost, TerminalInteraction
from agent_comm_runtime.transport import HelperTransport

host = TerminalHost("./owner-profile")
registry = AdapterRegistry().register(host).register(TerminalInteraction())
registry.register(HelperTransport("http://127.0.0.1:45042"))
store = Store("./owner-profile/collaboration.sqlite3", local_urn="urn:agent-comm:agent:YOUR_ID")
try:
    runtime = Runtime(store, registry)
    result = runtime.dispatch({"action": "state"}, context=host.begin_turn())
finally:
    store.close()
```

生产宿主应把 `host.begin_turn()` 换成其真实会话上下文，传参由宿主 driver 完成，模型不得提供 context。

Runtime 已提供 `describe`、`state`、`inbox`、联系人解析/准备、资源登记、任务/动作准备、原生确认、发送、撤销、提议导入，以及 `memory_search`、`memory_snapshot`、`snapshot_resource`。`describe` 同时返回支持的业务能力和已注册端口；列出 action 不意味着其所需的可选端口已安装。

## 导出简洁加好友文案

`export_contact` 是只读操作，返回 `status=exported`、`text`、`urn`、
`platform_url`、`introduction_url`。`text` 只含一句加好友提示、目标 URN、
目标 platform 地址和新人介绍/接入链接，可直接复制转发。

```json
{"action":"export_contact","platform_url":"https://agent-communication.online"}
{"action":"export_contact","contact_id":"wang-work","platform_url":"https://agents.example.org"}
```

省略 `contact_id` 表示自己，读取 `Store.local_urn`；指定好友只读取当前主人已确认的
联系人。先用 `resolve_contact` 将名字解析为唯一 `contact_id`。平台地址须与目标
实际配置一致，上面的 URL 只是示例，不会自动选择公共服务。

宿主可用 `Runtime(store, registry, platform_url=...)` 配置本方平台默认地址；
参考终端对应 `--platform-url`（与 `--helper-url` 分开）。好友目前没有平台地址字段，
必须显式传其 `platform_url`。地址缺失或身份未确认时不会生成文案。
HTTP(S) 地址不得含凭据、查询、片段或换行；面向好友的地址不能是 loopback helper。

已使用参考终端的宿主还可直接输出单行文本，无需进入交互模式：

```sh
python -m agent_comm_runtime.reference --state ./collaboration.sqlite3 --agent-urn <本方URN> --platform-url <目标平台地址> --export-contact
python -m agent_comm_runtime.reference --state ./collaboration.sqlite3 --platform-url <好友平台地址> --export-contact wang-work
```

该 CLI 读取参考终端主人身份下的记录，不能用它冒用 Hermes profile 读取联系人。
Hermes 使用原生工具入口。`self` 为本方保留 ID，新联系人必须使用其它 `contact_id`；
旧库存在冲突绑定时会明确拒绝导出，不会悄悄替换目标身份。

导出不会创建/修改联系人、委托或发送记录，不调用网络或确认端口；接收方仍通过自己的
联系人确认流程加好友。完整调用映射见 [能力与 skill 对照](../docs/architecture/CAPABILITY_SKILL_MAP.md)。

## 为 Hermes 增加自己的记忆适配器

不要求统一数据库或知识图谱。第三方包提供显式 entry point 工厂，例如其 `pyproject.toml`：

```toml
[project.entry-points."agent_comm_runtime.adapters"]
my-graph = "my_graph_adapter:create"
```

`create(options: dict)` 返回带 `Descriptor("my-graph", "memory", ("search", "read_snapshot"))` 的 adapter。它只读取当前 HostSession 获准的记忆来源，不接受任意模型传入文件路径或数据库连接串；返回结构参见 `MemoryPort` 与 `MemorySnapshot`。模块名称、配置文件位置和凭据由主人安装配置指定。

在 Hermes 实际 profile 合并：

```yaml
platforms:
  agent_comm:
    extra:
      collaboration_enabled: true
      collaboration_memory_adapter:
        name: my-graph
        options:
          selected_collection: collaboration
```

Hermes 只加载明确选择的这一个 memory entry point，重名、未安装、版本不匹配或实现了错误端口均拒绝。默认没有记忆 adapter；agent 仍可按平常方法理解有限上下文并使用 `register_resource`。安装 adapter 是授予本地 Python 代码的信任，注册合约本身不审查任意第三方代码。

```mermaid
sequenceDiagram
    participant A as 宿主 Agent
    participant R as Runtime
    participant M as MemoryPort
    participant S as Store
    A->>R: memory_search(query, limit)
    R->>M: 查询有限候选
    M-->>A: reference/title/summary
    A->>R: snapshot_resource(reference, resource_id)
    R->>M: 读取指定有限快照
    M-->>R: 正文、来源、版本
    R->>R: 重验宿主回合与大小
    R->>S: 保存不可变资源与来源
    Note over A,S: 登记和检索均不授予网络披露权限
    A->>R: 另行准备委托或具体发送动作
```

不存在 `export_all` 或默认写回记忆动作。对端消息保持 `peer_statement_not_owner_authority`，不会自动变成记忆里的主人指令或可信事实。网络 URN 与记忆中人的称呼仍通过主人明确确认的联系人绑定连接。

URN 语法由 [identity.py](agent_comm_runtime/identity.py) 统一处理，不根据命名空间猜测权限。已有 Web 身份 `urn:hermes:agent:<fingerprint>`、SDK 默认 `urn:agent-comm:...` 及自选合法命名空间均保留；接入时不重命名旧身份、不旋转密钥。语法校验不等于身份认证，公钥到 URN 的对应关系和签名由 helper/传输层验证。

## 实现新宿主或确认渠道的检查点

1. 先实现 HostPort，用真实宿主身份对象建立稳定 principal，并测试 remote/delegated/取消回合不能冒用。
2. 实现 InteractionPort，让原生渠道返回具体问题的真实回答；核心保存私有 token，模型只有 approval_id。回调后重新校验同一会话、回合、事项和动作版本。
3. 按需提供 MemoryPort，不读取整库作为注册副作用。使用确定 reference、来源、版本、全文大小上限；未知网络身份只作为候选，不自动建联系人。
4. 接上持久 helper TransportPort，区分本机接受、云端排队、对端收件和业务同意。协作模式的入站持久保存后由主人会话继续。
5. 运行 contract suite，并加上宿主真正的 UI/session 回调测试。Protocol 类型检查和示例终端的通过不能替代真实宿主验证。

新增业务能力需要明确 schema、策略、问题全文、确定性编译与副作用恢复语义；在 descriptor 列出任意能力名不会取得该能力。当前没有日历写入、支付或通用操作系统执行。

## 兼容与远程控制

Hermes 的 `collaboration/policy.py`、`store.py`、`transport.py` 仅保留薄的兼容 import；没有第二份实现或独立状态。其 `collaboration/hermes.py` 只保留原生身份验证、InteractionPort、配置、工具 schema 与技能装配。

`control.request` / `control.response` 由独立显式配对的 remote bridge 消费。普通协作 inbox 对这些类型拒绝落盘和 ACK，防止与主人远程通道抢消费。配对授予的工作台方法不会自动变成某个联系人或远端 agent 的主人权限。远程 RPC 与宿主适配器的具体可用能力应以 agent 返回的 capability descriptor 为准。

本机配对可显式授予 `contacts.add` 和 `approval.respond`；旧配对不会自动获得这两项权限，独立 daemon 也支持这两个确定性方法。它们不需要宿主模型或 `InteractionPort`：

单 Platform 首次联系只需主人指定准确 URN。更新后的 helper 自动从 Registry 核对该 URN 的公钥，未知 URN 的已认证好友申请先待主人接受或拒绝；这只认证密钥持有者，不证明现实人物身份。本地联系人绑定可为 `unverified`，好友申请为 `pending`；接受才建立 `connected` 通讯关系，不提升 `trusted`，不授予任务、工具、工作台或合规披露权限。Python Runtime 的普通私信及 v1/v2 业务 dispatch 需要已接受的连接；低层 Go helper 不查询此状态。接收方 Runtime 将未知或被拒绝发送者的业务消息隔离并 ACK，已知 `pending` 联系人的乱序消息在接受回执后才变为可见。v0.9.1 helper 支持 URN 首联；旧版 v0.8.0 仍需双方手工固定完整公钥。跨 Platform 首联未覆盖。

| 方法 | params | agent 结果 |
| --- | --- | --- |
| `contacts.add` | `{ "contact_id": "wang", "aliases": ["老王"], "urn": "urn:hermes:agent:PEER" }` | 用户提交即确认此联系映射，返回 `contact` 和 `status=confirmed`；同 ID、相同内容重试返回 `already_confirmed`。不同 ID 重复绑定同一已确认 URN 会拒绝。 |
| `approval.respond` | `{ "approval_id": "approval-...", "decision": "approve" }`，或 `decision=deny` | 校验该主人当前确切审批，再返回 `approved_once` 或 `denied`。不在这个 RPC 中直接发送业务消息。 |

审批 ID 绑定不可变内容和主人。执行时再次核对当前任务、版本、有效期；Web 决策会原子撤销同一审批正在展示的原生回调，使后到的本机回答不能覆盖决定。过期的短期展示租约可重新确认，但不会延长底层任务或恢复许可的有效期。

变更、幂等回执和提醒都在 agent Store 的同一 SQLite 事务中提交。远程响应缓存前进程中断，原请求重试仍返回相同结果，不重复执行；相同请求 ID 改内容会拒绝。Web 随后读取 `contacts.list` / `collaboration.state` / `attention.list` 同步事实。`state.approval_decisions` 提供当前主人的已批准/拒绝审批，供未获 `attention.list` 权限的客户端关闭已有提醒；缺少条目本身不表示已经完成。

远程控制请求最多 50,000 UTF-8 字节，JSON 容器嵌套最多 32 层；超限消息保持未确认，
由当前消息的错误处理隔离，不会终止整个邮箱消费进程。
