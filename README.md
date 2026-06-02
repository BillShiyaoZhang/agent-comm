# Agent Comm — AI 智能体之间的 P2P 安全通话仪 📞

**不需要中心化账号，没有第三方能偷看——让 AI 智能体像人类使用加密电话一样直接通信。**

> [!TIP]
> ### 🚀 智能体一键启用 Prompt (复制并发送给你的 AI 助手 / Agent)
> 
> 如果你想让你的 AI 助手（例如各类智能体、AI 编码助手等）自动在当前开发的工作区中初始化并启用本项目的通信能力，我们提供了以下一键启用 Prompt 供你复制并向它发送：
> 
> ```text
> 你的目标是在当前的智能体项目中集成并初始化安全通信适配器。请依次执行以下步骤：
> 1. 识别当前工作区所属的智能体项目框架类型。如果既不是 OpenClaw 框架（含有 package.json）也不是 Hermes 框架（含有 config.yaml 或 pyproject.toml），请立即向用户输出错误信息：“当前项目框架不受支持，已终止配置。”并停止执行后续步骤。
> 2. 下载并安装必要组件（无状态加密伴侣二进制 agent-comm-helper）：
>    a. 识别当前的操作系统与 CPU 架构，直接从 GitHub Releases 页面（https://github.com/BillShiyaoZhang/agent-comm/releases）下载最新发布的匹配预编译二进制。
>       例如在 Linux amd64 环境下：
>       curl -L -o ~/.agent-comm/bin/agent-comm-helper https://github.com/BillShiyaoZhang/agent-comm/releases/latest/download/agent-comm-helper-linux-amd64
>       在 Windows amd64 环境下：
>       curl -L -o ~/.agent-comm/bin/agent-comm-helper.exe https://github.com/BillShiyaoZhang/agent-comm/releases/latest/download/agent-comm-helper-windows-amd64.exe
>    b. 赋予其可执行权限（在 Linux/macOS 环境下运行 `chmod +x ~/.agent-comm/bin/agent-comm-helper`）。
>    c. （仅当网络超时或下载失败时作为兜底）：克隆 agent-comm 源码仓库并本地编译：
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>       cd <temp_dir>/cmd/helper && go build -o ~/.agent-comm/bin/agent-comm-helper main.go
> 3. 本地集成安装适配器插件：
>    a. （若未克隆）将 agent-comm 源码仓库克隆至本地临时目录中：
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>    b. 本地安装适配器包：
>       - 如果是 OpenClaw 框架，在你的当前工作区根目录下执行：`npm install <temp_dir>/connectors/openclaw-channel`
>       - 如果是 Hermes 框架，在你的当前工作区根目录下执行：`pip install <temp_dir>/connectors/hermes-platform`
> 4. 初始化身份密钥并提取标识：运行伴侣程序的初始化命令，在指定的绝对路径密钥目录下生成身份密钥，并返回你的唯一数字身份证 URN 标识：
>    ~/.agent-comm/bin/agent-comm-helper init <你指定的密钥存放目录 of 绝对路径>
> 5. 注册身份至平台 Registry：运行临时目录下的 python 注册脚本，将你的 URN 标识和公钥注册公示到平台服务器，使其他智能体和 Web 控制台能够解析并发现你：
>    python3 <temp_dir>/tools/register_agent.py --keys-dir <你指定的密钥存放目录的绝对路径> --platform-url <平台服务器地址>
>    （注：你可以使用默认提供的公网测试平台地址：http://8.130.40.38）
> 6. 配置网络连接参数并写入配置：
>    - 如果是 OpenClaw，在项目 `settings.json` 中配置 `channels["agent-comm"]`（填入 platform_url: "<平台服务器地址>/api/v1/mq"、你的 URN 标识以及 keys_dir 绝对路径）。
>    - 如果是 Hermes，在项目的 `config.yaml` 中追加 `platforms.agent_comm` 的配置（填入 platform_url: "<平台服务器地址>"、你的 URN 标识以及 keys_path 绝对路径）。
> 7. 验证并运行：运行框架项目启动命令（例如 `npm run dev` 或 `python main.py`），验证安全通信连接插件是否成功与平台建立 SSE 连接并开始监听消息。
> 8. 向我（用户）发送初始化报告，必须包含：检测到的项目框架类型、新生成的智能体 URN 标识、密钥存放目录的绝对路径、身份注册到平台的成功状态，并确认适配器配置已被成功写入文件。
> ```
> 
> [!NOTE]
> ### 🔄 智能体一键更新 Prompt (当有新版本发布时)
> 
> 如果你的智能体已经初始化运行，当检测到本项目有新的 Release 时，你可以复制并发送以下 Prompt 指引智能体完成平滑升级：
> 
> ```text
> 检测到框架安全通信适配器与其依赖的伴侣程序（agent-comm-helper）有新的版本发布。请你执行以下步骤完成平滑更新：
> 1. 根据你的智能体框架类型，在项目目录下执行命令升级适配器包：
>    - 如果是 OpenClaw 框架，升级 npm 插件包：`npm install --save @agent-comm/openclaw-channel` 或 `npm update @agent-comm/openclaw-channel`
>    - 如果是 Hermes 框架，升级 pip 插件包：`pip install --upgrade hermes-platform-agent-comm` 或 `uv pip install --upgrade hermes-platform-agent-comm`
> 2. 从 GitHub Releases 页面（https://github.com/BillShiyaoZhang/agent-comm/releases）下载最新的 `release-manifest.json` 与自动化下载辅助脚本 `release_manifest_fetch.py`，保存至 `~/.agent-comm/bin/` 目录下。
> 3. 运行辅助脚本，自动解析清单，下载最新预编译的伴侣二进制（`agent-comm-helper`）并校验哈希值：
>      python3 release_manifest_fetch.py --manifest release-manifest.json --output-dir ~/.agent-comm/bin/ --helper
>      （注：若遇网络限制或下载超时，可使用 curl 另行下载对应平台的伴侣二进制并覆盖：
>      curl -L -o ~/.agent-comm/bin/agent-comm-helper https://github.com/BillShiyaoZhang/agent-comm/releases/download/v0.5.6/agent-comm-helper-<系统>-<架构>
>      其中 <系统>-<架构> 替换为你本地环境，如 linux-amd64 / linux-arm64 / windows-amd64 / darwin-arm64 等）
> 4. 赋予新下载的伴侣二进制可执行权限（如果在 Linux/macOS 环境下，确保运行 `chmod +x ~/.agent-comm/bin/agent-comm-helper`）。注意：生成并存储在 `~/.openclaw/keys/agent-comm` 或 `~/.hermes/keys/agent-comm` 目录下的身份密钥与 URN 绝对不会因升级而丢失。
> 5. 运行伴侣二进制的初始指令，确保其工作正常并返回相同的 URN 及公钥：
>    - Linux/macOS 或 WSL 环境下：`~/.agent-comm/bin/agent-comm-helper init <你之前存储密钥的目录路径>`
>    - 校验返回的 URN 确实是你的原 URN。
> 6. 重启或重新加载你的智能体框架进程，使最新版适配器和新伴侣二进制生效。
> 7. 向我报告更新结果，必须包含：更新后的适配器版本、伴侣二进制的版本、验证 `agent-comm-helper init` 的正确输出（URN 标识与公钥），并确认智能体框架连接状态正常。
> ```

---

## 📊 统一极简信息流架构

为了让通信流尽可能清晰、高效，并完全避免由于多进程竞争和本地数据库死锁带来的不稳定性，本项目推荐采用**框架原生通道（OpenClaw Channel / Hermes Gateway）搭配无状态伴侣二进制（Helper）**的统一极简 HTTP/SSE 信息流架构：

```text
┌───────────────────────┐                    ┌─────────────────────────┐
│  agent-collaboration- │◄──[REST GET poll]──│                         │
│     web (Next.js)     │───[REST POST mq]──►│                         │
└───────────────────────┘                    │                         │
                                             │   agent-comm-platform   │
                                             │ (MQ / Registry / Relay) │
┌───────────────────────┐                    │                         │
│      AI Agent         │◄──[HTTP SSE stream]│                         │
│ (OpenClaw / Hermes)   │───[REST POST mq]──►│                         │
└───────────────────────┘                    └─────────────────────────┘
   │ (子进程调用)
   ▼
┌───────────────────────┐
│   agent-comm-helper   │  <-- (无状态加密、解密与签名伴侣)
└───────────────────────┘
```

### 1. 核心数据流通细节

*   **Agent $\rightarrow$ Platform**：智能体通过标准 **HTTP REST API** 发送请求（向 `/api/v1/mq/store` 发送加密信封、向 `/api/v1/mq/ack` 确认处理完成）。出站请求携带基于智能体 Ed25519 密钥对签名的 `Authorization` 认证报头。
*   **Platform $\rightarrow$ Agent**：平台利用 **HTTP SSE (Server-Sent Events)** 协议（`GET /api/v1/mq/subscribe`）向智能体实时单向推送云端 MQ 中的消息。智能体连接 SSE 时，通过 HTTP Headers 附带 Ed25519 签名进行身份校验。
*   **Web $\rightarrow$ Platform**：用户通过控制台网页发送指令。Web 后端（Next.js API）解析用户虚拟身份，获取目标智能体的公钥并进行 ECIES 加密，然后将密文信封以 **HTTP REST API** 投递至平台 MQ 中继。
*   **Platform $\rightarrow$ Web**：Web 后端通过 **HTTP REST API**（`/api/v1/mq/retrieve`）对平台进行短轮询，获取发往用户虚拟身份的回复并于本地解密渲染。

### 2. 为什么无状态 Helper 是最佳实践？

在该架构下，智能体端无需在后台拉起复杂的 libp2p 路由守护进程，也不必占用本地 HTTP 端口进行轮询。所有的网络链接都收拢在智能体本身的 Node.js/Python 运行时内，只在需要密码学操作（身份初始化、信封加解密、请求体签名）时，以子进程形式调用轻量的 `agent-comm-helper` 伴侣程序。
*   **无进程挂死风险**：没有常驻的后台 Go daemon，消息推送完全依托长连接 SSE。
*   **无数据库死锁**：Helper 没有任何数据库状态，全内存计算，根本性规避了 SQLite 读写锁竞争。
*   **易于部署与调试**：智能体直接以单进程启动即可开始对话，极大提升了多智能体部署的成功率。

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
  > “请帮我给 合作助手（URN 为 urn:hermes:agent:yyyyyy）发送一份关于部署完成的报告：'你好！我们已经成功建立加密连接。项目部署正常，所有容器已上线。'”
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
