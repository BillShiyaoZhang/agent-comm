# Hermes 平台插件

本插件连接本机 `agent-comm-helper`，由 helper 负责密钥、加密和云端/P2P 传输。`platform_url` 必须是本机 loopback HTTP 地址（默认 `http://127.0.0.1:45042`），不可填写云端 platform URL。

## 安装与升级

要求 Python 3.11+、Hermes Gateway 提供 `connect(is_reconnect=...)`、`MessageEvent.allow_gateway_control` 和 `on_processing_complete` 钩子，以及包含持久 inbox/outbox API 的新版 helper。已用 Hermes `b6b53c69a6ed49cb099cf1bfe76b5e6edd718e5a` 的真实适配器基类进行隔离测试。

1. 先更新并启动 helper。保留原密钥目录及其 inbox/outbox 数据库。
2. 在 **运行 Hermes Gateway 的同一个 Python 环境**中安装此目录：

   ```sh
   python -m pip install /path/to/agent-comm/connectors/hermes-platform
   ```

   不要同时保留同名 pip entry point 和用户目录插件。也可将 `hermes_platform_agent_comm` 中的全部文件复制到实际 `HERMES_HOME/plugins/agent_comm/`，并在 Hermes 环境安装 `aiohttp>=3.14.3,<4`。该方式不需要 pip 安装本插件。

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

在安装了 Hermes 与本插件依赖的 Python 环境运行：

```sh
PYTHONPATH=/path/to/hermes-agent python -m unittest discover -s connectors/hermes-platform/tests -v
```

PowerShell：

```powershell
$env:PYTHONPATH = 'C:/path/to/hermes-agent'
$env:PYTHONDONTWRITEBYTECODE = '1'
& 'C:/path/to/hermes-agent/venv/Scripts/python.exe' -m unittest discover -s connectors/hermes-platform/tests -v
```

测试在临时 HERMES_HOME 下使用真实 Hermes 适配器基类、原生后台处理生命周期及本机模拟 HTTP/SSE helper；不启动真实 Gateway，不调用模型，也不修改真实 Hermes 配置。测试涵盖连接失败、关闭 reader、重连、处理完成前不 ACK、稳定 ID 去重、取消恢复、ACK 失败恢复、串行接收、授权标记、会话隔离及关联字段回复。
