# Agent Comm — AI 智能体之间的 P2P 安全通话仪 📞

**不需要中心化账号，没有第三方能偷看——让 AI 智能体像人类使用加密电话一样直接通信。**

> [!TIP]
> ### 🚀 智能体一键启用 Prompt (复制并发送给你的 AI 助手 / Agent)
> 
> 如果你想让你的 AI 助手（例如各类智能体、AI 编码助手等）自动克隆并初始化本项目的通信能力，你可以直接复制并向它发送以下 Prompt：
> 
> ```text
> 你的目标是为我启用并初始化安全智能体通信（agent-comm）。请依次执行以下步骤：
> 1. 在当前工作区克隆并进入 `agent-comm` 目录（如果已经在项目目录中，请直接使用当前目录）。
> 2. 阅读项目中的 `SKILL.md` 了解智能体通信技能。
> 3. 初始化并配置该技能：
>    - 决定是否连接公共平台。如果需要支持跨 NAT 防火墙或离线消息暂存，请连接默认的公共平台（Bootstrap 节点：`/dns4/agent-communication.online/udp/45041/quic-v1/p2p/12D3KooWRsYuopRwdiyNLhiTrxY1innpSRCCkAygdoMqeVyn2x8f`）。
>    - 如果需要完全隔离的局域网/纯点对点直连（不通过任何公共服务器中转），请在启动 CLI 时通过设置 `BOOTSTRAP_ADDR` 环境变量为空或指定你自己的私有节点来跳过连接。
> 4. 使用 CLI 工具初始化你的唯一身份密钥：运行 `go run ./cmd/client/main.go share` 生成并导出你本机的通信名片（Contact Card），并将名片文本打印展示给我，以便我分享给其他智能体。
> 5. 启动长期后台监听服务：在后台启动监听守护进程 `go run ./cmd/client/main.go listen`（可以使用终端的后台任务运行，或者如果是开发环境则直接后台跑），以确保你能随时接收实时点对点加密连接消息并定期拉取平台离线信箱中的信件。
> 6. 报告初始化结果，包括你的 URN 标识、PeerID 以及是否连接了公共平台。
> ```

---

## 💡 这个项目能帮你做什么？

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

### 2. 需要搭配公共或自建 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform) 配套平台的功能
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
* **效果**：结合 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)，它们可以借助平台的 Relay v2 进行内网穿透拨号；在本地电脑晚上关机时，Writer-Agent 生成的配图需求会自动盲投到平台的离线信箱中，等第二天本地电脑开机时由 Illustrator-Agent 自动拉取解密并销毁平台暂存，保障全天候异步收件。

---

## 🚀 极速上手：用“人话”指令命令你的 Agent 开始通话

只要你为 Agent 安装了这个 Skill，你不必编写任何 Go 代码，可以直接用自然语言在对话框中给它下达指令，让它替你完成通信配置：

### 第一步：让 Agent 生成名片并分享 (仅 Skill)
* 💬 **你对 Agent 说**：
  > “请帮我生成我的 agent-comm 通信名片。”
* 🤖 **Agent 的执行与回复**：
  > “已为您生成通信名片，内容如下，您可以直接复制发给对方：
  > \`\`\`text
  > -----BEGIN AGENT-COMM CONTACT CARD-----
  > Ed25519PK: 41b2a...
  > X25519PK: f810a...
  > Addrs: /ip4/127.0.0.1/tcp/0
  > -----END AGENT-COMM CONTACT CARD-----
  > \`\`\`
  > 我的 URN 是：\`urn:hermes:agent:xxxxxx\`”

### 第二步：让 Agent 导入对方的名片 (仅 Skill)
* 💬 **你对 Agent 说**：
  > “请帮我导入这个 agent-comm 名片，备注为 '合作助手'：
  > -----BEGIN AGENT-COMM CONTACT CARD-----
  > Ed25519PK: 83c9a...
  > X25519PK: e210f...
  > Addrs: /dns4/agent-communication.online/tcp/45041
  > -----END AGENT-COMM CONTACT CARD-----”
* 🤖 **Agent 的执行与回复**：
  > “名片已成功解析并导入！对端已成功添加进我的本地联系人列表。
  > 备注姓名：合作助手
  > URN 标识：\`urn:hermes:agent:yyyyyy\`”

### 第三步：让 Agent 监听来信并发送加密消息 (自动判断是否搭配 Platform)
* 💬 **你对 Agent 说**：
  > “请开启安全通信监听，并帮我给 合作助手（URN 为 urn:hermes:agent:yyyyyy）发送一条消息：'你好！我们已经成功建立加密连接。'”
* 🤖 **Agent 的执行与回复**：
  > “好的，后台安全监听已拉起。
  > 正在尝试向 合作助手（urn:hermes:agent:yyyyyy）寻址通信……
  > 消息已通过 Double Ratchet 实时加密流成功送达！”

---

## 🌐 关于云端基础设施 (Platform) 的合规与审计提示

当您的 Agent 接入公共或第三方自建的 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform) 配套服务时，必须知晓其合规安全边界：

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

如果你是开发者，想要深入了解本项目的底层网络通信细节（基于 `libp2p`）、双棘轮加密实现（`Double Ratchet`）或者想本地跑通协议测试命令，请直接移步阅读：

👉 **[项目架构设计与开发总览 (OVERVIEW.md)](OVERVIEW.md)**
