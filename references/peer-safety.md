# 对端屏蔽与本人内容审核

[English](peer-safety-en.md) · [远程配对与方法](remote-control.md)

以下描述当前源码的 Python Runtime 与 Hermes connector。安装旧包不会自动得到这些能力；应安装匹配版本、保留原身份和状态库，并通过真实 `capabilities` 回执检查支持。只读配对和 `collaboration.execute` 均不自动授予本人审核或屏蔽权限。

Store 将旧 SQLite schema 1 单向升级为 2，保留记录与 owner/pairing 命名空间。旧 Runtime 会拒绝打开 schema 2，避免回滚旧投影绕过历史内容审核。Gateway 与桌面后台须一并使用匹配版本；不要用旧 wheel 继续处理已升级数据库。

升级前停止该 helper 的所有消费者并备份协作库、remote 库、receipt 库及关联 WAL 文件。schema 更新是一次 SQLite 写事务提交；审核记录随后按 owner 在读取时增补，正常投影始终拒绝未批准内容。不能仅换回旧 wheel 回滚：旧库备份缺少升级后的屏蔽/审核决定，恢复它会丢失安全状态，应在停机下评估并保留新状态，不允许旧组件与新组件同时处理同一库。

## 真实屏蔽

本机主人或明确配对的主人界面调用：

| RPC | 权限 | params | 回执 |
| --- | --- | --- | --- |
| `contacts.block` | 独立 WRITE | `{urn}` | `{urn,status:"blocked",blocked:true,connection_status:"blocked",safety_revision}` |
| `contacts.unblock` | 独立 WRITE | `{urn}` | `{urn,status:"unblocked",blocked:false,connection_status,safety_revision}` |

URN 必须为真实对端 URN，不能屏蔽本机 Agent。状态保存在 owner+URN 的持久 ACL；helper 的公钥缓存、`trusted` 或本机列表隐藏不是此屏蔽。

`contacts.list` 与 `collaboration.state` 顶层带整数 `safety_revision`，初始 0；同一 owner 的屏蔽布尔状态真正改变时事务内递增。`contacts[]` 含 `blocked`，屏蔽联系人为 `connection_status:"blocked"`。`blocked_peers[]` 为 `{urn,blocked:true,connection_status:"blocked",blocked_at}`，也覆盖尚未存为联系人的发送方。

同一 RPC 重放保留原回执及原 revision。客户端只按不低于当前值的 revision 合并安全状态，不能让迟到旧快照或历史回执覆盖较新的解除/屏蔽结果。新的配对必须逐项明确加入 `--allow contacts.block --allow contacts.unblock`；不要给旧配对偷偷增权。

屏蔽使好友请求、普通来信、排队外发、未完成协作动作、相关待决审批及后台 worker 停止。入站在持久隔离后允许 ACK；不触发私人模型。实际 helper 提交前在同一 SQLite 写事务内检查，已完成屏蔽之后不会沿用旧 ACL 继续提交排队发送。已经被 helper 接受的消息或已经发生的外部效果无法收回。

旧消息和未完成发送有持久终止记录。解除屏蔽只允许未来新内容按连接和审核规则进入，不恢复旧消息、旧审批、worker、维护许可或发送队列；需要新的主人意图。屏蔽不是远程撤销配对、全网删除或阻止对端继续向平台投递。

## 完整预览后决定本机内容使用

普通 `inbox.list` 和 `collaboration.state` 仅给待审核内容元数据：

```json
{"pending_review":[{"message_id":"真实消息ID","sender_urn":"真实发送方URN","kind":"chat.message","received_at":2000000000,"status":"pending"}],"review_policy":{"version":1,"approval_scope":"local_content_use_only"}}
```

每次返回最多 100 项待审核元数据；处理后再次读取可发现其余待审核项。这里没有正文，也不表示 Web/App 显示安全已核对。

| RPC | 权限 | params | 结果 |
| --- | --- | --- | --- |
| `inbox.review_preview` | 独立 READ | `{message_id}` | `{message_id,sender_urn,kind,text,received_at,status,fingerprint,text_truncated:false}` |
| `inbox.review` | 独立 WRITE | `{message_id,decision:"approve"\|"reject"}` | `{message_id,sender_urn,status:"approved"\|"rejected",fingerprint}` |

`fingerprint` 是不可变完整 wire 记录规范 JSON 的 SHA-256；正文、ID、发送方或关联变化不能复用旧记录。批准前必须在同一实际 owner session/配对 console 成功获取完整预览；截断或超过控制响应限制时拒绝，不写入可批准标记。界面应明确让本人查看、核对并决定，不能由模型、服务运营者或 `collaboration.execute` 代答。

拒绝无需先预览。批准/拒绝都是终态，同决策重复返回相同结果，相反决策拒绝。屏蔽内容不能获准。已拒绝或屏蔽旧内容不自动重放；批准也不会自动创建模型回合或向对端发信。审核允许的是本机内容读取/后续处理，不能替代好友接受、真实身份核验、任务范围、具体业务审批、资料共享或第三方 AI 许可。

默认隔离普通自由文本及 v2 `invite`、`proposal`、`change_request`。恢复 `sync_response` 内历史文本事件使用相同本人门禁，不能从正常收件箱、方案投影、操作正文、通知详情或后台 worker 绕过。历史已应用记录在读取时补加审核门禁。类型化 receipt、ACK、同步恢复证据继续进入协议状态机，其 raw wire 正文不进入普通收件箱/模型。固定好友请求与响应仍按独立接受流程处理。

## 宿主与显示边界

当前 `capabilities` 返回真实声明：

```json
{"peer_content_safety":{"version":1,"mode":"owner_review","automatic_peer_model_execution":false}}
```

两种 Hermes 模式均先持久接管业务入站并 ACK，不再把对端消息直接交给 Gateway 模型。本人随后可通过获准的原生或已配对主人回合处理已批准内容。启用个人协作模式时直接发送仍受禁；兼容旧主动发送路径同样检查持久屏蔽 ACL。工作台可要求上述能力后才允许新的私人助手会话，不能为旧 Agent 或低层 helper 伪造声明。

对话宿主须在可信构造代码中向 `RemoteBridge(..., conversations=True, peer_content_safety=True)` 明确声明已采用更新的入站路径；更新 Hermes adapter 已完成此绑定。旧 adapter 未提供标记时，新 Runtime 也不替它宣称安全。无模型消费的 standalone 可声明其真实边界；配置文字或模型参数不能充当宿主验证。

Bridge 会在新 `conversation.send`、请求重放和排队回合启动前检查当前宿主门禁，不能只依赖客户端缓存的能力。未受理的新请求返回 `peer_content_safety_required`，没有创建回合；已受理请求在宿主降级后重放返回 `peer_content_safety_changed`，须读取既有会话确认状态，不能推断它从未执行。未启动的旧队列回合标记失败且不派发；`conversation.get` 仍可读取既有记录。

宿主开发者可使用 `Store.set_peer_block`、`review_preview`、`review_peer`，但主人身份与决定必须来自可信宿主界面；这些不是模型 Runtime actions。`describe.owner_content_safety` 明确披露此边界。

Web 运营审核/举报与本机本人审核是不同权限。`review_policy` 不证明 Web/App 的公开投影已过滤；运营者不能通过展示审核替本人允许模型消费。举报系统亦不属于这四个 RPC。

ACL/审核保护的是更新后的 Runtime 和 connector 支持的收发入口。低层 Go helper 是身份、加密和持久传输层，不查询 Python owner ACL；直接原始 retrieve、任意本机 shell、其它宿主插件或已复制内容不在此过滤边界。接入其它宿主时，必须在任何模型消费和正常 UI 投影之前采用同一持久门禁，不能以移除 helper 公钥或关键词列表冒充屏蔽。

验证涵盖临时 SQLite、两个 owner、并发事务、重放、嵌套恢复以及真实 connector 源码的隔离导入。后者使用 host/aiohttp 测试替身，不代表已经在真实 Hermes 安装上验证；发布前仍须在明确的匹配宿主环境复核。
