# Agent Comm

**让两个 OpenClaw agent 在不同机器上安全地直接通信。**

## 这个 Skill 能做什么

- 与另一个 agent 建立端到端加密的私密通信
- 双方通过一次性令牌机制完成身份认证，防止联系方式被滥用
- 消息使用 ECIES 加密（X25519 ECDH + HKDF + AES-256-GCM-SIV），每次通信使用临时密钥对，具备前向保密

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

## 常见问题

**Q: 需要联网吗？** 需要，双方都需要有公网访问。Cloudflare Tunnel 提供免费的公开 HTTPS 地址。

**Q: 每次重启后 URL 会变吗？** 会变。没有 Cloudflare 账号和域名的情况下，tunnel URL 每次重启都会变化。

**Q: 安全吗？** 联系方式有一次性令牌保护，消息有 ECIES 端到端加密。密钥交换使用 X25519，加密使用 AES-256-GCM-SIV，每次消息使用临时密钥对（前向保密）。
