# 远程工作台：配对与方法

用于已安装 `agent-comm-runtime` 的本机配对管理或 Web/宿主集成。它通过 helper 的加密信箱交换 `control.request` / `control.response`，不额外公开本机 HTTP 监听端口。普通好友关系不产生工作台权限。

## 本机 CLI

先确定实际 profile、console 的已核实 URN、授权方法和有效期，不猜真实 profile 路径。以下为参数模板：

```text
agent-comm-runtime remote pair --hermes-profile <actual_profile> --console-urn <console_urn> --allow capabilities --allow contacts.list --allow collaboration.state --allow inbox.list --expires <RFC3339_expiry_with_timezone>
agent-comm-runtime remote pairings --hermes-profile <actual_profile>
agent-comm-runtime remote revoke --hermes-profile <actual_profile> --console-urn <console_urn>
```

`pair` 写入本机白名单并返回 `paired_locally`，会授予指定 console 读取该主人的相应数据；按用户已授权的工作台与范围执行，不因曾加为通信好友自动配对。`pairings` 查看已有记录，`revoke` 撤销对应 console，已披露的数据不能收回。到期时间需要秒与时区，使用本次授权的实际有效期。

四个子命令都接受 `--state <remote.sqlite3>`；未提供时使用 `<profile>/agent-comm/remote.sqlite3`。非 Hermes 宿主的配对显式提供 `--state` 与 `--owner-principal`，该主体来自实际宿主配置，不能伪造为另一个人的身份。

## 选择一个消费进程

- **Hermes Gateway**：在其 agent_comm `extra` 中启用 `remote_enabled: true`，按需配置 `remote_state_path`；插件使用该 profile 的稳定主人主体，提供真实 Hermes 会话执行。安装与配置见 [插件说明](../connectors/hermes-platform/README.md)。
- **standalone**：没有 Hermes 消费同一 inbox 时可启动只读 bridge：

```text
agent-comm-runtime remote serve --hermes-profile <actual_profile> --agent-urn <local_agent_urn> --helper-url http://127.0.0.1:45042 --once
```

`--once` 只处理一个有界批次并退出；去掉时持续消费。`--collaboration-state <collaboration.sqlite3>` 可指定数据文件，默认在 profile 的 `agent-comm/` 下。非 Hermes 情况需要 `--state` 和 `--collaboration-state`。使用 helper `/info` 返回的本地 URN，并按实际配置替换 helper URL。

不要让 standalone 与 Hermes 同时消费同一 helper，也不要另开普通协作消费者抢 ACK `control.*` 消息。standalone 持久收取普通消息，但不会启动模型、唤醒主人原生对话或执行 `conversation.send`。

## 已实现的 RPC 方法

方法是否可用同时取决于本机 pairing 白名单和宿主适配器。客户端先请求已授权的 `capabilities`，检查每个方法的 `available`，不要从工具名称推断能力。

| 方法 | params | 结果/边界 |
| --- | --- | --- |
| `capabilities` | `{}` | 协议、各方法可用性/原因、配对到期时间 |
| `contacts.list` | `{}` | 当前配对主人 profile 的 runtime 联系人；不是 helper 公钥缓存 |
| `collaboration.state` | 可选 `task_id` | 联系人、事项、动作、待确认项、入站及提议；没有审批权限 |
| `inbox.list` | 可选 `task_id` | 已存入 runtime 的入站；不是新一次 helper 拉取 |
| `conversation.send` | 必需 `text`；可选 `conversation_id` | Hermes 启用且明确授权时提交回合，返回 `submitted`、conversation ID、turn ID；不代表回答已生成 |
| `conversation.get` | 必需 `conversation_id` | 该 console 与主人对应会话最近最多 100 个回合的状态、正文、回答或错误 |
| `approval.respond` | 不可用 | 始终不支持；主人确认必须经原生可信交互 |

需要远程会话时，对该 console 的明确配对增加 `--allow conversation.send --allow conversation.get`；standalone 即使白名单包含它们也没有对应宿主执行能力。`conversation.send` 文本上限 24000 UTF-8 字节；同一 console 最多 100 个未完成回合。

RPC 使用稳定 `request_id` 和最长 300 秒的 deadline，重试复用同一请求，不改同 ID 的内容。传输 `accepted`、RPC `submitted` 与模型回合完成是不同状态。对端消息、配对和远程会话均不会成为 runtime 原生主人审批上下文。

自定义控制客户端的消息关联、重放、响应持久化与撤销检查见 [remote.py](../python/agent_comm_runtime/remote.py)；CLI 参数以 [daemon.py](../python/agent_comm_runtime/daemon.py) 为准。不要为未注册的方法降级执行任意 shell 或通用文本动作。
