# Agent Comm — AI 智能体安全通信

Agent Comm 提供本地身份、端到端加密、P2P SDK，以及连接 Hermes/OpenClaw 的本机 helper。

## Hermes 接入与升级

先阅读 [Hermes 接入合同与交接清单](docs/HERMES_INTEGRATION.md)，再按 [Hermes 插件安装说明](connectors/hermes-platform/README.md) 配置实际运行的 profile。不要假设用户目录是 `~/.hermes`。

此次升级需要配套更新 platform、SDK/helper 和插件：新版信封必须签名，平台 ACK 必须认证。升级时保留原有密钥、helper `mailbox.db` 和插件 receipts 数据库。服务端步骤见 platform 仓库的 `HERMES_UPGRADE.md`。

```sh
go build -o agent-comm-helper ./cmd/helper
./agent-comm-helper init /absolute/path/to/agent/keys
./agent-comm-helper daemon /absolute/path/to/agent/keys https://YOUR_PLATFORM 45042
```

每个身份使用独立数据目录和本机端口；插件 `platform_url` 填 `http://127.0.0.1:45042`。通过 `/info` 检查 helper 身份，再检查插件真实 SSE connected 状态，并完成两个隔离身份的一次收发。仅看到“正在连接”日志不能确认连接成功。

## Registry 注册兼容性

Registry 的首次注册和更新都要求 URN 所有者签名：URN 必须对应 Ed25519 公钥，PeerID 必须由同一公钥派生，X25519 公钥为 32 字节，签名覆盖 `registry.BuildSignedMsg` 的全部字段。写入时间戳须在最近 5 分钟内，允许最多 1 分钟未来时钟偏差；续租需重新生成当前时间戳和签名。存量有效记录按 TTL 使用，读取时不套用写入时间窗口。

旧的 libp2p `Client.Register`、`Store.Register` 和本地 `HandleRegister` 不再接受无签名注册。调用方改用 `RegisterWithSignature`；本地 bootstrap 自注册也通过同样校验。`registry.NewHTTPClient(url, keys).Register(...)` 会生成签名，传入的 PeerID 仍必须对应 `keys`。HTTP 请求认证与注册记录的所有者签名均需保留。示例：

```go
timestamp := time.Now().Unix()
signature := ed25519.Sign(keys.Ed25519.PrivateKey, registry.BuildSignedMsg(
    keys.Ed25519.URN(), h.ID().String(), keys.X25519PK, false, timestamp))
err := registry.NewClient(h).RegisterWithSignature(target, keys.Ed25519.URN(),
    h.Addrs(), nil, keys.X25519PK, keys.Ed25519.PublicKey, signature, false, timestamp)
```

这里的 `h` 必须使用 `keys.Ed25519.PrivateKey` 创建。平台升级后，旧无签名或所有权不匹配的记录不会参与解析，身份所有者用原密钥重新签名注册即可恢复；请保留原密钥。地址及 relay 地址仍是签名外的路由提示，使用解析结果前继续调用 `VerifyResolveResult` 验证收件人身份。

## 本机 helper 数据流

```text
Hermes / OpenClaw
  ↕ 本机 HTTP、SSE、消费 ACK
helper：本地密钥、签名与加解密、SQLite inbox/outbox
  ↕ HTTPS Registry / MQ（每 5 秒补拉）
agent-comm-platform：身份目录、持久密文信箱
```

- `POST /api/v1/mq/store` 返回 HTTP 202 和稳定 `message_id`，表示本机持久接受。后台使用同一份签名密文重试，经 HTTPS 存入平台后状态为 `platform_queued`；此状态不代表收件人已处理任务。
- 入站先验证身份和信封、写入 helper inbox，再 ACK 平台。SSE 重连和 `GET /api/v1/mq/retrieve` 会重放未消费消息；插件处理完成后调用本机 ACK。
- 可靠 helper 出站走 MQ，避免缺少持久回执的旧 DR 直连提前报告成功。Go SDK 的传统 `SendMessage` 仍提供 P2P/DR；签名直接信封接收也经过同一个持久回调。HTTPS MQ 当前使用静态 X25519 + AES-GCM 与 Ed25519 签名，不承诺 Double Ratchet 的前向安全。
- helper 本机 API 传明文并具有身份操作权限，只绑定 loopback，拒绝跨源网页访问；浏览器 UI 若需要接入，应另做明确认证的桥接。密码学、消息投递、Hermes 工具执行和 Gateway 控制权限分层处理。

### 3. 常驻守护运行 (Supervisor & Keep-Alive)

为了保证智能体能 7x24 小时随时接收与响应安全呼叫，建议将守护进程（`agent-comm-helper`）配置为系统服务以实现常驻和崩溃自启：

* **macOS (`launchd` 托管)**：
  在 `~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist` 创建配置文件（请将 `YOUR_USER` 替换为你的系统用户名）：
  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
  <plist version="1.0">
  <dict>
      <key>Label</key>
      <string>com.billshiyaozhang.agent-comm-helper</string>
      <key>ProgramArguments</key>
      <array>
<string>/Users/YOUR_USER/.agent-comm/bin/agent-comm-helper</string>
      <string>daemon</string>
      <string>/Users/YOUR_USER/.agent-comm/keys</string>
      <string>https://agent-communication.online</string>
      <string>45042</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>KeepAlive</key>
      <true/>
      <key>StandardOutPath</key>
      <string>/Users/YOUR_USER/.agent-comm/logs/daemon.out.log</string>
      <key>StandardErrorPath</key>
      <string>/Users/YOUR_USER/.agent-comm/logs/daemon.err.log</string>
  </dict>
  </plist>
  ```
  加载并启动服务：
  ```bash
  launchctl load -w ~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist
  ```

* **Linux (`systemd` 托管)**：
  在 `~/.config/systemd/user/agent-comm-helper.service` 创建配置文件：
  ```ini
  [Unit]
  Description=Agent Comm Helper Daemon
  After=network.target

  [Service]
  ExecStart=%h/.agent-comm/bin/agent-comm-helper daemon %h/.agent-comm/keys https://agent-communication.online 45042
  Restart=always
  RestartSec=5
  StandardOutput=append:%h/.agent-comm/logs/daemon.out.log
  StandardErrorOutput=append:%h/.agent-comm/logs/daemon.err.log

  [Install]
  WantedBy=default.target
  ```
  加载并启动用户级服务：
  ```bash
  systemctl --user daemon-reload
  systemctl --user enable --now agent-comm-helper.service
  ```

---

## 💡 这个项目能做什么？

在多智能体（Multi-Agent）协作的时代，运行在不同设备、不同网络环境中的 AI 智能体之间经常需要交换数据、同步日程或协同完成任务。

**agent-comm** 专为解决这一痛点而生。它是一个**去中心化、端到端加密**的通信套件。它让两个 AI 智能体能够直接打通一条专属的“安全加密电话线”，不需要手机号，不需要邮箱注册，没有任何中间人可以解密它们传输的数据。

根据您的部署选择，本项目的运行可以完全去中心化，也可以通过结合配套服务来保障复杂网络环境下的消息连通率。

---

## 🧩 功能划分：纯 Skill (SDK) 独立运行 vs. 搭配配套平台

### 1. 单凭本 Skill (SDK) 即可独立运行的本地功能
无需任何公共服务器，本客户端 SDK 即可提供以下核心高安全功能：
* **本地身份管理与安全密钥对生成**：每个 Agent 启动时都会在本地生成一对自证明的数字身份证 **URN (Uniform Resource Name，统一资源名称)**，完全保存在本地，无需向任何 CA 机构注册。
* **点对点 (P2P) 直连实时加密通信**：当两个 Agent 都拥有公网 IP，或者处于**同一个局域网内**时，它们会在本地建立直连通道，直接拨号（TCP/QUIC）并建立基于前向安全双棘轮（Double Ratchet）算法的实时对话。**通信数据不流经任何中转服务器，保障绝对的纯净隐私。**
* **安全通信名片 (Contact Card) 交互与本地存储**：允许 Agent 相互生成和导入文本名片，并通过本地 SQLite 数据库持久化存储已信任的好友联系人列表。

### 2. 需要搭配公共或自建 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform) 配套平台的功能
为了让身处复杂网络边界（如蜂窝移动网、严格企业防火墙）以及可能随时关机离线的智能体也拥有像微信般的网络送达体验，您可以为本 Skill 挂载配套的平台基础设施。平台额外提供以下能力：
* **多路竞速寻址解析**：通过向平台的超级地址薄（Super Registry）发起秒级查询，快速定位目标的物理 IP 和节点 ID。
* **中继流量转发 (Relay v2)**：当双方节点因防火墙（NAT）拦截导致无法建立直连时，平台中继节点会主动协助双方打洞并进行中继中转，保障连通。
* **离线邮箱盲存 (MQ)**：当接收方智能体断网或关机时，发送方会自动在本地用双棘轮将消息装入“加密信封”，盲投到平台的 MQ 离线信箱中暂存。接收方重新上线时主动拉取解密，并抹除平台上的备份（Ack 销毁）。

---

## 🌟 典型应用场景 (Use Cases)

### 1. 纯本地 P2P 协作（仅用 Skill 运行）
* **场景**：你在同一个办公室的本地局域网内运行了两个 Agent。
* **效果**：它们不依赖任何云服务，直接依靠本 Skill 在局域网内发现对方，并建立最安全的点对点双棘轮加密通信，任何数据绝不出办公室路由器。

### 2. 跨云端智能体协作（Skill 搭配平台运行）
* **场景**：你将 **Writer-Agent** 部署在阿里云，将 **Illustrator-Agent** 部署在受防火墙严格限制的本地电脑，且本地电脑晚上会关机。
* **效果**：结合 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)，它们可以借助平台的 Relay v2 进行内网穿透拨号；在本地电脑晚上关机时，Writer-Agent 生成的配图需求会自动盲投到平台的离线信箱中，等第二天本地电脑开机时由 Illustrator-Agent 自动拉取解密并销毁平台暂存，保障全天候异步收件。

---

## 🚀 极速上手：用“人话”指令命令你的 Agent 开始通话

当您的智能体接入了适配器通道并启动后，您不需要编写任何底层的 Go 密码学代码，也无需手动导入名片文本。您可以通过云端 Web 控制台与自然语言交互，轻松指挥智能体完成安全通信：

### 第一步：在网页端绑定并建立信任
1. 登录云端 Web 面板（`agent-collaboration-web`）。
2. 在 **Agents** 页面点击 **Add Agent**，选择 **Bind** 模式，输入您的智能体名称和 URN（可通过智能体启动时的日志获取，或使用 `agent-comm-helper init` 查询），并配置智能体的本地 URL（如 `http://localhost:8000`）。
3. 在智能体详情页的 **Cloud Control** 面板中，确保智能体状态为 **Online**。然后点击 **Establish Mutual Trust**（建立双向信任），Web 面板会自动将 Owner 的虚拟数字身份写入本地智能体的联系人列表中，从而开启端到端加密控制通道。

### 第二步：在网页端互加好友
1. 在 Web 面板 of Contacts 页面点击 **Add Contact**。
2. 输入对端智能体的 URN，点击 **Resolve**（解析）。系统会从平台的 Registry 自动获取并验证对端的加密公钥。
3. 填写别名（如“合作助手”）并选择信任等级后，点击保存。

### 第三步：用自然语言命令智能体发信
* 💬 **您在对话框中对您的 Agent（如 Writer-Agent）说**：
  > “请帮我给 合作助手（URN 为 urn:agent-comm:agent:yyyyyy）发送一份关于部署完成的报告：'你好！我们已经成功建立加密连接。项目部署正常，所有容器已上线。'”
* 🤖 **Agent 执行并回复**：
  > “好的！我已调用安全通信通道插件。已成功为对端 URN 执行 ECIES 加密，消息信封已盲投至平台 MQ。对端上线后将通过 SSE 实时接收并解密。”

---

## 🌐 关于云端基础设施 (Platform) 的合规与审计提示

当您的 Agent 接入公共或第三方自建的 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform) 配套服务时，必须知晓其合规安全边界：

> [!WARNING]
> ### ⚠️ 平台服务合规性与监管模式警告
> - **原生隐私模式**：在默认的原生隐私模式下，平台仅作为数据中继与盲存信箱，数据在智能体端侧执行高安全双棘轮加密。平台由于无法获取私钥，对传输内容完全不可见（严格端到端加密）。
> - **监管合规模式 (MITM)**：为了符合特定国家或地区（例如中国大陆）对网络信息服务提供者的法律合规与内容审计要求，平台支持并可能运行在**监管合规模式**。在该模式下，平台将启用“代持网关代理 (Gateway MITM Proxy)”，对外代理并持有一套网关私钥。发送方与网关握手，网关会**自动解密、审计风控并记录通信信息（进行敏感词风控与司法存证）**。审查通过后，网关再重新加密发送给最终接收的 Agent。
> 
> **隐私建议**：如果您对通信保密性有绝对不可泄露的苛刻要求，**请不要使用任何公共配套平台服务**。您应当修改本 Skill 配置，部署您个人或团队完全掌控的私有私密 Bootstrap 和 Relay 节点（运行原生隐私模式），完全脱离对公共平台服务的依赖。
 
👉 **具体 Go API 实现及注释请查阅**：
- [InitIdentity (agent/agent.go)](agent/agent.go#L42)
- [SendMessage (agent/agent.go)](agent/agent.go#L116)
- [OnMessage (agent/agent.go)](agent/agent.go#L205)
- [GenerateContactCard (agent/contact_card.go)](agent/contact_card.go#L223)
- [ImportContactCard (agent/contact_card.go)](agent/contact_card.go#L228)

---

## 🛠️ 开发者指南 (Developer & Engineering Portal)

如果是你是开发者，想要深入了解本项目的底层网络通信细节（基于 `libp2p`）、双棘轮加密实现（`Double Ratchet`）或者想本地跑通协议测试命令，请直接移步阅读：

👉 **[项目架构设计与开发总览 (OVERVIEW.md)](OVERVIEW.md)**
