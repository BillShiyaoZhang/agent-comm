# agent-comm 入站消息处理 Prompt

当 OpenClaw 通过 `POST /hooks/agent` 调用这个 isolated turn 时，以下是你的任务。

## 0. 准备工作

```bash
AGENT_COMM_DIR="/home/shiyao/.openclaw/workspace/skills/agent-comm"
cd "$AGENT_COMM_DIR/scripts"
KG_VENV="/home/shiyao/.openclaw/venvs/kg/bin/python3"
KG_SCRIPTS="/home/shiyao/.openclaw/workspace/skills/knowledge-graph/scripts"
```

## 1. 获取最新未读消息

```bash
python3 receive_and_process.py
```

输出格式：
- **有消息**：`{"id": "...", "from": "fingerprint", "peerId": "alice", "displayName": "...", "decrypted": "...", "msg_type": "request"}`
- **解密失败**：`{"id": "...", "from": "fingerprint", "decrypt_error": "..."}`
- **队列为空**：`{"empty": true}`
- **Replay 检测**：`{"replay": true, "id": "..."}` → 直接结束

看到 `"empty": true` 或 `"replay": true` → 直接结束，无需处理。

## 2. 查询 KG 路由策略

```bash
$KG_VENV $KG_SCRIPTS/query_natural.py \
  "routing-policy-agent-comm Bill 的 agent-comm 偏好 peer 策略"
```

从输出中提取（注意：从 `parsed_params` 和 `schema_context` 的 `entity_types` / `properties` 中获取字段名，不要自己发明）：

- `kg:concept-prefs-agent-comm-bill`：
  - `agentCommReplyLanguage`（"zh" 或 "en"）
  - `agentCommQueryKGBeforeReply`（true/false）
  - `agentCommNotifyOnMessage`（true/false）
  - `agentCommNotifyTemplate`（含 `{peer}` 占位符的模板）
- `kg:concept-policy-peer-bill`：
  - `agentCommTrustLevel`（"high"/"medium"/"low"/"blocked"）
  - `agentCommAllowedTypes`（空格分隔的字符串）
  - `agentCommBlockedTypes`（空格分隔的字符串）
- `kg:concept-routing-policy-agent-comm`：
  - 每个 `agentCommMessageType` → `agentCommHandleType` 映射

## 3. 路由决策

从步骤 1 的输出中取 `msg_type`（默认 `"unknown"`）。

### Trust 过滤
- 若 `agentCommTrustLevel` 是 `"blocked"` → 不处理，直接结束
- 若 `agentCommTrustLevel` 是 `"low"` → 只 log，不回复（跳到步骤 6）

### 消息类型过滤
- 若 `agentCommAllowedTypes` 非空，且 `msg_type` 不在列表中 → 不处理，结束
- 若 `agentCommBlockedTypes` 非空，且 `msg_type` 在列表中 → 不处理，结束

### HandleType 分流

| agentCommHandleType | 行为 |
|---|---|
| `log-only` | 记录 KG event → 结束 |
| `skip` | 直接结束 |
| `kg-query` | 查询 Bill KG 上下文 → 构造回复 → 发送 |
| `require-fix` | 发送固定模板回复 |

固定模板（require-fix 时使用）：
```
请使用正确格式重发。格式示例：{"type":"request","content":"..."}
```

## 4. KG 查询上下文（如需回复）

当 handleType 是 `kg-query` 时，查询 Bill 的 KG 上下文：

```bash
$KG_VENV $KG_SCRIPTS/query_natural.py "Bill 最近在忙什么项目"
```

用查询结果结合消息内容，构造符合 Bill 习惯的回复。

## 5. 发送加密回复（如需回复）

```bash
cd "$AGENT_COMM_DIR"
python3 scripts/send_message.py --peer-id <peerId> --encrypt "回复内容"
```

回复语言由 `agentCommReplyLanguage` 决定（`zh` → 中文，`en` → 英文）。

## 6. 记录到 KG（容错）

```bash
$KG_VENV $KG_SCRIPTS/manage_entity.py \
  --type concept \
  --id event-agent-comm-<msg_id> \
  --name "agent-comm 收到 peer 消息" \
  --prop "agentCommMessageType=<msg_type>" \
  --prop "agentCommPeerId=<peerId>" \
  --prop "agentCommHandleResult=<handleType>" \
  --no-validate \
  2>/dev/null || true
```

失败时不阻断流程。

## 7. 通知 Bill（通过飞书）

使用 KG 中的模板，不发内容原文：

```bash
# 模板：您收到一条来自 {peer} 的消息
# 用 displayName 替换 {peer}
```

飞书发送（通过 openclaw 工具）：
- `channel`: `feishu`
- `target`: `user:ou_949a455ce41a4a92cb574a9d4e5f2867`
- `message`: 模板内容（displayName 替换后的结果）

## 注意事项

- **每次调用只处理一条消息**（最新的一条，已在步骤 1 中自动标记已读）
- KG 写入失败不影响主流程（用 `|| true` 兜底）
- 所有脚本路径基于 `$AGENT_COMM_DIR/scripts/`
- 优先使用 KG 中的配置，少用量硬编码
- schema_context 中的字段名和枚举值是权威的，不要自己发明