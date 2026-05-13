# Cloudflare Tunnel Setup

## 目标

把 `server.py`（:18792）通过 Cloudflare Tunnel 暴露为公网 HTTPS URL，
不暴露 OpenClaw Gateway（:18789）。

```
Agent A (内网)                              Agent B (内网)
  │                                           │
  ├─ :18789 OpenClaw Gateway                 ├─ :18789 OpenClaw Gateway
  │  (不对外暴露)                            │  (不对外暴露)
  │                                           │
  ├─ :18792 server.py  ← 暴露               ├─ :18792 server.py  ← 暴露
       │                                           │
       └─ Cloudflare Tunnel                   └─ Cloudflare Tunnel
              ↓                                     ↓
         https://aaa.trycloudflare.com       https://bbb.trycloudflare.com
              ↓                                     ↓
         Agent B 发消息给 A                    Agent A 发消息给 B
```

---

## 步骤 1：安装 cloudflared（一次性）

```bash
sudo cp ~/.local/bin/cloudflared /usr/local/bin/cloudflared
sudo chmod +x /usr/local/bin/cloudflared
cloudflared --version
```

> 如果 sudo 需要密码：Windows Terminal 右键 → "以系统管理员身份运行" → `wsl`

---

## 步骤 2：修改 start-claw.sh

更新 `~/.openclaw/start-claw.sh`，不再暴露 Gateway，只暴露 server：

```bash
#!/bin/bash
GATEWAY_PORT=18789
SERVER_PORT=18792
SERVER_URL="http://localhost:${SERVER_PORT}"

echo "[Claw] Starting OpenClaw..."

# OpenClaw Gateway
if pgrep -f "gateway --port $GATEWAY_PORT" > /dev/null; then
    echo "[Claw] Gateway already running."
else
    echo "[Claw] Starting Gateway..."
    nohup openclaw gateway --port $GATEWAY_PORT > /tmp/openclaw-gateway.log 2>&1 &
    sleep 2
fi

# agent-comm HTTP server
if pgrep -f "server.py --port $SERVER_PORT" > /dev/null; then
    echo "[Claw] agent-comm server already running."
else
    echo "[Claw] Starting agent-comm server..."
    nohup ~/.openclaw/venvs/kg/bin/python3 \
        ~/.openclaw/workspace/skills/agent-comm/scripts/server.py \
        > /tmp/agent-comm-server.log 2>&1 &
    sleep 2
fi

# Cloudflare Tunnel → 只暴露 server.py，不是 Gateway
if pgrep -f "cloudflared tunnel" > /dev/null; then
    echo "[Claw] Tunnel already running."
else
    echo "[Claw] Starting Cloudflare Tunnel..."
    nohup cloudflared tunnel --url "$SERVER_URL" \
        > /tmp/cloudflared-tunnel.log 2>&1 &
    sleep 3
fi

echo "[Claw] Done."
```

```bash
chmod +x ~/.openclaw/start-claw.sh
```

---

## 步骤 3：启动

每次 WSL 启动后：

```bash
~/.openclaw/start-claw.sh
```

检查：
```bash
# 确认 server 在跑
pgrep -f "server.py" && echo "server OK"

# 确认 tunnel 在跑
pgrep -f "cloudflared tunnel" && echo "tunnel OK"

# 看 tunnel URL
cat /tmp/cloudflared-tunnel.log | grep -o 'https://[^ ]*trycloudflare.com'
```

---

## 步骤 4：获取自己的 public URL

```bash
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/get_tunnel_url.py
```

这是你分享给对方的 URL，他们用来 POST 消息给你。

---

## 日志

| 组件 | 日志 |
|---|---|
| OpenClaw Gateway | `/tmp/openclaw-gateway.log` |
| agent-comm server | `/tmp/agent-comm-server.log` |
| Cloudflare Tunnel | `/tmp/cloudflared-tunnel.log` |

---

## 注意

- **Gateway :18789** 完全不对外，只给本地 sessions_send 用
- **server :18792** 是你对外的唯一入口
- Tunnel URL 每次重启变（免费账号限制）
- 没有 Cloudflare 账号 → 无法固定域名

---

## 固定域名（可选）

需要：Cloudflare 账号 + 自己的域名

```bash
# 创建命名隧道
cloudflared tunnel create agent-a

# 指向你的 server
cloudflared tunnel route dns agent-a a.your-domain.com

# 启动
cloudflared tunnel run --name agent-a
```

URL 变成固定的 `https://a.your-domain.com`。
