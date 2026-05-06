# Agent Comm

**让两个 OpenClaw agent 在不同机器上安全地直接通信。**

## 这个 Skill 能做什么

- 与另一个 agent 建立端到端加密的私密通信
- 双方通过一次性令牌机制完成身份认证，防止联系方式被滥用
- 消息使用 ECIES 加密（X25519 ECDH + HKDF + AES-256-GCM-SIV），每次通信使用临时密钥对，具备前向保密
- 入站消息经过多层安全检查（peer 白名单、replay 检测、schema 验证）后，由 KG 路由策略驱动处理

## 安全机制

### 入站消息处理流程

```
外部消息 → server.py → 3 层安全检查
                    ├─ Peer 白名单（不在 contacts → HTTP 403）
                    ├─ Replay 检测（10 分钟内的重复消息 → HTTP 409）
                    └─ Timestamp 验证（5 分钟窗口）

              ↓ 全部通过后入队

receive_and_process.py → Schema 验证（JSON type 字段）
                      → KG 路由查询（routing-policy + Bill 偏好 + peer-policy）
                      → 路由决策（log-only / skip / kg-query / require-fix）
                      → KG 记录（容错）
                      → 飞书通知（模板，不发内容原文）
```

### 消息类型

| type | 处理方式 |
|---|---|
| `notification` | 仅记录 KG → 结束 |
| `request` | 查询 KG 上下文 → 构造回复 → 发送加密回复 |
| `ack` | 直接跳过 |
| `unknown` | 发固定模板提示，要求对方用正确格式重发 |

### KG 路由策略

通过 `knowledge-graph` skill 管理三个实体：

- `kg:concept-routing-policy-agent-comm` — type → handleType 映射
- `kg:concept-prefs-agent-comm-bill` — Bill 的个人偏好（回复语言、是否查 KG、通知模板）
- `kg:concept-policy-peer-bill` — Bill 的 peer 信任策略（trustLevel、allowedTypes、blockedTypes）

### Trust 级别

- `blocked` → 不处理，直接忽略
- `low` → 只记录 KG，不回复
- `medium` / `high` → 正常流程

## 你需要做什么

### 第一次安装（你）

把以下内容完整粘贴给你的 agent 执行：

```
安装 skill: https://github.com/BillShiyaoZhang/agent-comm-skill

初始化 agent-comm skill。运行以下步骤：
1. 启动 `server.py`（后台 Flask 服务，监听 localhost:18792）
2. 启动 Cloudflare Tunnel，将 server 暴露为 HTTPS 公开地址
3. 生成 Ed25519 + X25519 身份密钥对
4. 输出你的 contact JSON（包含公开密钥和 tunnel URL）
```

### 对方也需要安装

让对方也执行同样的初始化流程，然后你们互换 contact JSON。

### 互换联系方式并建立连接

1. **你的 agent** 会生成一个指令和一个 contact JSON 文件（文件名类似 `my-contact.json`）
2. **你把这个指令和文件发给对方**（通过任何渠道：飞书、微信、邮件等）
3. **对方可以根据你的指令安装此 skill** 并 **把他们生成的 contact JSON 发给你**
4. **你把对方的 contact JSON 文件路径粘贴给你的 agent**，让你的 agent 注册对方

完成后，你们就可以互相发送加密消息了。

## 测试与调试

### Dry-run 模式

不实际处理消息，只模拟决策：

```bash
cd ~/.openclaw/workspace/skills/agent-comm/scripts
python3 receive_and_process_dryrun.py
```

### 查看队列

```bash
curl -s http://localhost:18792/agent-comm/messages \
  -H "Authorization: Bearer $(cat ~/.openclaw/agent-comm/auth_token.json | jq -r '.token')"
```

## 常见问题

**Q: 需要联网吗？** 需要，双方都需要有公网访问。Cloudflare Tunnel 提供免费的公开 HTTPS 地址。

**Q: 每次重启后 URL 会变吗？** 会变。没有 Cloudflare 账号和域名的情况下，tunnel URL 每次重启都会变化。

**Q: 安全吗？** 联系方式有一次性令牌保护，消息有 ECIES 端到端加密。密钥交换使用 X25519，加密使用 AES-256-GCM-SIV，每次消息使用临时密钥对（前向保密）。入站有 peer 白名单、replay 检测、schema 验证三重保护。

**Q: 如何更新 KG 路由策略？** 通过 `knowledge-graph` skill 的 `manage_entity.py` 修改对应实体。路由逻辑由 KG 驱动，无需改代码。