# 远程工作台：配对与方法

用于已安装 `agent-comm-runtime` 的本机配对管理或 Web/宿主集成。它通过 helper 的加密信箱交换 `control.request` / `control.response`，不额外公开本机 HTTP 监听端口。普通好友关系不产生工作台权限。

## 本机 CLI

先确定实际 profile、console 的已核实 URN、授权方法和有效期，不猜真实 profile 路径。以下为参数模板：

```text
agent-comm-runtime remote pair --hermes-profile <actual_profile> --console-urn <console_urn> --allow capabilities --allow contacts.list --allow collaboration.state --allow inbox.list --expires <RFC3339_expiry_with_timezone>
agent-comm-runtime remote pairings --hermes-profile <actual_profile>
agent-comm-runtime remote revoke --hermes-profile <actual_profile> --console-urn <console_urn>
```

`pair` 写入本机白名单并返回 `paired_locally`，授予指定 console 对该主人相应数据和方法的访问权；上例只授予读取。按用户已授权的工作台与范围执行，不因曾加为通信好友自动配对。新增写方法需显式重配，升级不会扩大旧白名单。`pairings` 查看已有记录，`revoke` 撤销对应 console，已披露的数据不能收回。到期时间需要秒与时区，使用本次授权的实际有效期。

四个子命令都接受 `--state <remote.sqlite3>`；未提供时使用 `<profile>/agent-comm/remote.sqlite3`。非 Hermes 宿主的配对显式提供 `--state` 与 `--owner-principal`，该主体来自实际宿主配置，不能伪造为另一个人的身份。

## 选择一个消费进程

- **Hermes Gateway**：在其 agent_comm `extra` 中启用 `remote_enabled: true`，按需配置 `remote_state_path`；插件使用该 profile 的稳定主人主体，提供真实 Hermes 会话执行。安装与配置见 [插件说明](../connectors/hermes-platform/README.md)。
- **standalone**：没有 Hermes 消费同一 inbox 时可启动 bridge；它提供已配对的内置数据读取、好友、消息、已读和用户审批方法：

```text
agent-comm-runtime remote serve --hermes-profile <actual_profile> --agent-urn <local_agent_urn> --helper-url http://127.0.0.1:45042 --once
```

`--once` 只处理一个有界批次并退出；去掉时持续消费。`--collaboration-state <collaboration.sqlite3>` 可指定数据文件，默认在 profile 的 `agent-comm/` 下。非 Hermes 情况需要 `--state` 和 `--collaboration-state`。使用 helper `/info` 返回的本地 URN，并按实际配置替换 helper URL。

不要让 standalone 与 Hermes 同时消费同一 helper，也不要另开普通协作消费者抢 ACK `control.*` 消息。standalone 持久收取普通消息和好友协议，重试已授权出站并刷新联系人在线状态；不会启动模型、唤醒主人原生对话、执行 `conversation.send` 或注册 `collaboration.execute`。内置写能力仍需各自的本机配对权限。

## 已实现的 RPC 方法

方法是否可用同时取决于本机 pairing 白名单和宿主适配器。客户端先请求已授权的 `capabilities`，检查每个方法的 `available`，不要从工具名称推断能力。

| 方法 | params | 结果/边界 |
| --- | --- | --- |
| `capabilities` | `{}` | 协议、各方法可用性/原因、配对到期时间 |
| `contacts.list` | `{}` | 当前配对主人 profile 的 runtime 联系人、连接状态和 `presence`；不是 helper 公钥缓存 |
| `contacts.requests` | `{}` | 双向好友请求及 `pending` / `accepted` / `rejected` 状态 |
| `contacts.add` | 必需 `contact_id`、`aliases`（字符串数组）、`urn` | 确认用户提交的本地联系人并持久发出好友请求；对方接受后才建立连接 |
| `contacts.respond` | 必需 `request_id`、`decision=accept\|reject`；可选 `contact_id`、`aliases` | 处理收到的好友请求，接受时保存联系人并建立通讯关系；不授予信任或协作权限，向对方发送响应并同步双方连接状态 |
| `messages.send` | 必需 `recipient_urn`、`text`；可选稳定 `message_id` | 仅向已 `connected` 的联系人持久提交用户输入的确切正文，断线重试复用消息 ID；不代表对方已读 |
| `collaboration.state` | 可选 `task_id` | 联系人、请求、资料、事项、动作、待确认项、已作决定、入站、发送记录及提议；读取不授予审批权限 |
| `inbox.list` | 可选 `task_id` | 已存入 runtime 的内容及 `read` / `read_at`；不是新一次 helper 拉取 |
| `inbox.mark_read` | 必需 `message_id` | 在 agent 保存已读，两端下一次同步时关闭此消息提醒；不接受好友或批准动作 |
| `attention.list` | 可选 `after`、`limit`（1–100） | 同一 agent 的持久待办增量；跟随 `cursor` / `has_more`，已处理事项更新为 resolved |
| `approval.respond` | 必需 `approval_id`、`decision=approve\|deny` | 用户在可信 Web 审批卡对具体问题作决定；写入同一 Store 并使迟到的原生回答失效。不得作为模型代答工具 |
| `collaboration.execute` | Runtime 工具参数对象，必需 `action` | Hermes 注册的完整 Runtime 入口；`describe` 返回 `actions`、`action_fields`，其它字段由所选动作决定；standalone 不提供 |
| `conversation.send` | 必需 `text`；可选 `conversation_id` | Hermes 启用且明确授权时提交回合，返回 `submitted`、conversation ID、turn ID；不代表回答已生成 |
| `conversation.get` | 必需 `conversation_id` | 该 console 与主人对应会话最近最多 100 个回合的状态、正文、回答或错误 |

需要远程会话时，对该 console 的明确配对增加 `--allow conversation.send --allow conversation.get`；standalone 即使白名单包含它们也没有对应宿主执行能力。`conversation.send` 和 `messages.send` 的正文上限均为 24000 UTF-8 字节；同一 console 最多 100 个未完成会话回合。

Web 控件需要按用户授权追加各自方法，例如 `--allow contacts.add --allow contacts.respond --allow messages.send --allow inbox.mark_read --allow approval.respond`，读取请求/提醒再允许 `contacts.requests` / `attention.list`。要在 Web 聊天或完整能力表单中使用全部已注册 Runtime 动作，追加 `--allow collaboration.execute`，然后调用：

```json
{"action":"describe"}
```

这是 `collaboration.execute` 的 params，不是完整信封。`action_fields` 给出每个动作的必需/可选字段。原生与配对聊天的 `agent_comm_collaboration` 调用同一个 Runtime/Store，好友可用 `prepare_contact`、`contact_requests`、`prepare_contact_response`，消息可用 `prepare_message`、`inbox`、`mark_read`，同时保留资料、任务及协作等原有能力。写工具需要 `collaboration.execute`；只读工具仍按对应读取方法校验权限。

`prepare_*` 返回待决审批时，原生 `confirm` 可以显示问题卡；远程 `confirm` 返回 `approval_required`，直到用户在 Web 审批卡或原生对话明确决定。随后 `confirm` 读取已记录的结果。模型不能调用 `approval.respond` 替主人回答。好友/普通消息审批会将确切内容放入持久 outbox；委托下的协作动作按原 `dispatch` 流程执行。联系人信任、聊天正文与对端请求都不能绕过这条权限边界。

RPC 使用稳定 `request_id` 和最长 300 秒的 deadline，重试复用同一请求，不改同 ID 的内容。通用执行若在提交与响应之间中断，重放可能返回 `uncertain`；先查看 agent 状态、发送记录和待决审批，不自动换新 ID 重做。传输 `accepted`、好友的 `connected`、RPC `submitted` 与模型回合完成是不同状态。所有客户端显示 agent 保存的结果；联系人在线观察过期后为 `unknown`，不把平台曾注册视为当前在线。

自定义控制客户端的消息关联、重放、响应持久化与撤销检查见 [remote.py](../python/agent_comm_runtime/remote.py)；CLI 参数以 [daemon.py](../python/agent_comm_runtime/daemon.py) 为准。不要为未注册的方法降级执行任意 shell 或通用文本动作。
