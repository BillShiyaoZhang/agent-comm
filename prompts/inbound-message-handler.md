# agent-comm 入站消息处理 Prompt

当 OpenClaw 通过 `POST /hooks/agent` 调用这个 isolated turn 时，以下是你的任务。

## 0. 准备工作

```bash
AGENT_COMM_DIR="/home/shiyao/.openclaw/workspace/skills/agent-comm"
cd "$AGENT_COMM_DIR/scripts"
```

## 1. 获取最新未读消息

```bash
python3 receive_and_process.py
```

输出格式：

- **有消息**：→ `{"id": "...", "from": "fingerprint", "peerId": "alice", "displayName": "...", "decrypted": "..."}`
- **解密失败**：→ `{"id": "...", "from": "fingerprint", "peerId": "alice", "decrypt_error": "..."}`
- **队列为空**：→ `{"empty": true}`

看到 `"empty": true` → 直接结束，无需处理。

## 2. 判断是否需要回复

- 若有 `decrypt_error` → 记录 KG（错误类型）→ 跳过
- 若 `decrypted` 内容是纯通知/ACK → 记录 KG → 跳过（无需回复）
- 若需要回复 → 构造回复内容，继续下一步

## 3. 发送加密回复（如需回复）

```bash
cd "$AGENT_COMM_DIR"
python3 scripts/send_message.py --peer-id <peerId> --encrypt "你的回复内容"
```

## 4. 记录到 KG

```bash
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/knowledge-graph/scripts/manage_entity.py \
  --entity-type event \
  --name "agent-comm 收到 peer 消息" \
  --properties "source=agent-comm,peer=<peerId>,timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

## 5. 通知 Bill（通过飞书）

通过飞书发送一条简要通知，告知有新消息到达及其处理结果：

**必填参数：**
- `channel`: `feishu`
- `target`: `user:ou_949a455ce41a4a92cb574a9d4e5f2867`（Bill 的飞书 open_id）
- `message`: 通知内容（简洁明了）

---

## 注意事项

- **每次调用只处理一条消息**（最新的一条，已在步骤 1 中自动标记已读）。
- 记录 KG 时用步骤 1 输出的 `peerId` 字段替换 `<peerId>`。
- 所有脚本路径基于 `$AGENT_COMM_DIR/scripts/`。
