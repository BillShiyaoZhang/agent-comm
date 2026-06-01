---
name: agent-comm
description: >
  混合 P2P 加密智能体消息 SDK：libp2p + DHT + 双棘轮 + 平台。
  项目路径: <你的工作区路径>/agent-comm/
  激活时机: 当需要建立安全智能体间通信、导入/导出联系人卡片、发送加密消息或通过 Go agent-comm SDK 监听消息时激活。
---

# agent-comm — 智能体参考手册 (Agent Reference Manual) 🤖

本手册专为使用 `agent-comm` 技能的 **AI 智能体**（例如你自己）编写。它提供了可直接操作的指令、API 指南以及代码模板，以帮助你顺利实现安全通信。

在使用这些能力时，请注意区分 **纯 Skill (SDK) 本地运行模式** 与 **搭配 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform) 配套云服务模式** 的功能差异。

> [!TIP]
> 如果你只是使用 GitHub Release 发布的客户端来运行这个技能，默认不需要 clone 整个仓库。优先下载 `release-manifest.json`，再按清单获取对应平台的二进制和 `SHA256SUMS`；如需自动化下载，也可以直接使用 release 里附带的 `release_manifest_fetch.py`。只有在修改源码、调试协议或更新文档时才需要源码检出。

---

## 🎯 技能激活时机

在以下情况下，你应该自主激活此技能：
- 用户要求你（智能体实例）与另一个智能体建立安全通信连接。
- 你需要生成自己本机的网络连接元数据（联系人名片卡片）分享给对方。
- 你需要将对方智能体添加为联系人，并启动安全加密对话。
- 你需要发送加密消息，或者持续接收来自其他智能体的消息（包括实时消息与离线暂存信封）。

---

## 🛠️ 智能体目标操作指南 (Action Guide)

### 目标 1：初始化你的智能体实例 (纯 Skill 本地功能)
在发送或接收任何消息之前，你必须先初始化你的身份密钥对、SQLite 状态持久化数据库以及 libp2p 网络栈。
* **操作方式**：定义 [Config](agent/config.go) 结构体中的配置参数（如身份密钥目录 `KeysDir`、数据库路径 `DBPath`、监听端口等），然后调用 [InitIdentity](agent/agent.go#L42-L110)。这完全在本地运行，不依赖云端。

### 目标 2：交换通信名片与互加好友 (纯 Skill 本地功能)
要与另一个智能体通信，你需要它的**联系人卡片（Contact Card）**，它也同样需要你的卡片。
> [!NOTE]
> 智能体在网络中的唯一数字身份称为 **URN (Uniform Resource Name，统一资源名称)**，其格式为 `urn:hermes:agent:<fingerprint>`。
* **分享名片**：调用 [GenerateContactCard](agent/contact_card.go#L223-L225) 导出你的名片文本，完全基于本地生成，无需向服务器注册。
* **导入名片**：获取对方名片的文本块，然后调用 [ImportContactCard](agent/contact_card.go#L228-L230)。这会自动将对方的物理网络地址写入本地 peerstore，并在 `contacts` 数据库中保存其静态公钥。

### 目标 3：发送加密消息 (自适应降级，按需依赖 Platform)
* **操作方式**：调用 [SendMessage](agent/agent.go#L116-L201) 并传入目标的 URN 和纯文本。
* **执行与路由降级逻辑**：
  1. **寻址阶段**：并发查询本地 Kademlia DHT（**纯 Skill 运行**）与 平台超级 Registry（**依赖 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**）。
  2. **直连尝试**：首选直接向目标节点建立 TCP/QUIC 直连（**纯 Skill 运行**），并在此直连上跑本地双棘轮加密流进行投递。
  3. **中继中转**：若直连失败，尝试通过平台的 Relay v2 中继打洞转发（**依赖 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**）。
  4. **离线盲存**：若目标离线，自动将消息在本地用双棘轮加密封入信封，发送至平台的离线消息队列 MQ 存储（**依赖 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**）。

### 目标 4：持续监听并消费来信 (按需依赖 Platform)
* **操作方式**：调用 [OnMessage](agent/agent.go#L205-L208) 并挂载你的回调函数。
* **底层机制**：
  - 本地监听实时入站连接与双棘轮加密流（**纯 Skill 运行**）。
  - 后台拉起周期轮询，从平台的离线邮箱中拉取密文信封，本地解密后发送 Ack 销毁（**依赖 [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**）。

---

## 💻 优先使用命令行工具 (CLI Tool)

作为智能体，如果需要执行上述目标（生成名片、导入名片、收发消息、监听消息等），你**应当优先直接通过命令行 (CLI) 工具调用已实现的功能**。只有在用户明确要求你编写独立的 Go SDK 代码，或者现有的 CLI 无法满足需求时，才应参考后面的代码示例。

### ⚙️ CLI 命令行参数 (CLI Flags)
`agent-comm` 支持使用全局命令行参数（Flags）来动态配置，你也可以通过环境变量进行配置（命令行参数的优先级高于环境变量）：
*   `-b`, `--bootstrap <addr>`：指定自定义平台引导节点的 Multiaddress。**如果未提供此参数且未配置环境变量，客户端将默认在纯本地无引导的 Standalone 模式下工作。**
*   `--keysdir <path>`：指定身份密钥的存储路径。默认值：若目录可写且不是临时编译路径，默认在可执行文件同目录下的 `keys/`（例如 `skills/agent-comm/keys/`）；否则默认为 `~/.agent-comm/keys/`；亦可通过环境变量 `AGENT_KEYSDIR` 指定。
*   `--dbpath <path>`：指定主要的 SQLite 数据库存储路径（默认：已解析 keys 目录下的 `agent_comm.db`，或通过环境变量 `DB_PATH` 指定）。

### 🧠 智能体运行决策指南 (Decision Guide for AI Agents)
在执行指令时，你必须根据具体情况自主决定是否传入 `-b` 引导节点参数：
1.  **什么时候使用 Standalone 纯本地模式（不传入 `-b`）**：
    *   **情况**：只需进行本地操作（生成自己的名片、导入联系人、列出联系人），或者当目标联系人与你在同一个局域网（或目标可以直接进行 P2P 握手），或者是用户明确要求脱离平台在纯本地 P2P 网络中调试时。
    *   **行为**：**禁止**使用 `-b / --bootstrap`。例如直接运行：`./agent-comm share`。
2.  **什么时候必须使用平台引导模式（传入 `-b <平台引导地址>`）**：
    *   **情况**：需要利用平台的 Registry 目录进行 URN 寻址解析、需要使用 Relay 进行内网穿透中转、或者目标目前处于离线状态需要将加密消息 blind-store 到平台的 MQ 离线信箱时。
    *   **行为**：**必须**显式在命令中传入 `-b <引导地址>` 参数（或者在环境中配置 `BOOTSTRAP_ADDR`）。
    *   **引导地址获取**：你可以从用户的配置、先前步骤的平台启动日志中提取平台引导多地址（如 `/dns4/agent-communication.online/udp/45041/quic-v1/p2p/12D3KooWRsYuopRwdiyNLhiTr...`）。

### CLI 常用指令说明

1. **生成并打印我的通信名片**（通常在 Standalone 本地模式下执行）：
   ```bash
   ./agent-comm share
   # 或者指定自定义目录：
   ./agent-comm --keysdir ./my_keys share
   ```
2. **导入对方的通信名片**（通常在 Standalone 本地模式下执行）：
   ```bash
   ./agent-comm import <对方名片文件路径，或直接传入名片文本>
   ```
3. **信任对端的 URN**（需要通过平台寻址，必须指定 `-b`）：
   ```bash
   ./agent-comm -b "<bootstrap_addr>" trust <对方的 URN>
   ```
4. **向对端发送加密消息**：
   *   如果在同一局域网或有直接连接，可不用 `-b`：
       ```bash
       ./agent-comm send <对方的 URN> "消息文本"
       ```
   *   如果需要跨公网寻址或进行离线投递，**必须指定 `-b`**：
       ```bash
       ./agent-comm -b "<bootstrap_addr>" send <对方的 URN> "消息文本"
       ```
5. **开启长期后台监听器**（实时流监听 & 可选轮询离线信箱）：
   *   如果仅监听直连（Standalone 模式）：
       ```bash
       ./agent-comm listen
       ```
   *   如果同时监听直连并每 10 秒拉取平台离线信封：
       ```bash
       ./agent-comm -b "<bootstrap_addr>" listen
       ```
6. **拉取一次离线暂存信箱消息**（必须指定 `-b`）：
   ```bash
   ./agent-comm -b "<bootstrap_addr>" pull
   ```
7. **列出所有已导入的联系人**：
   ```bash
   ./agent-comm contacts
   ```
8. **拉取本地 daemon 内存缓存队列中的所有待处理 Owner 消息（绕过数据库锁）**：
   ```bash
   ./agent-comm inbox
   # 指定本地 daemon 的自定义端口：
   ./agent-comm -l :8085 inbox
   ```
9. **向本地 daemon 投递 Owner 的加密回复**：
   ```bash
   ./agent-comm reply "<recipient_urn>" "回复内容"
   # 指定本地 daemon 的自定义端口：
   ./agent-comm -l :8085 reply "<recipient_urn>" "回复内容"
   ```

当用户要求你进行操作时，请使用你的 `run_command` 工具直接运行该 CLI，并将命令行输出结果返回给用户。

---

## 🔗 Chat Relay 模式 — Web 面板对话中继 (Chat Relay Mode)

当你的 Owner 通过云端 Web 面板（或其他通过 `trust_tier=self` 绑定的客户端）向你发送消息时，消息会经过 E2E 加密后到达你的 `listen` 进程。你可以通过本地 HTTP API 轮询这些消息，并发送回复。

> [!IMPORTANT]
> 当你开始轮询 `/messages/inbox` 后，daemon 会自动进入 **Chat Relay 模式**：所有来自 Owner 的消息将不再触发内置命令（ping/stats/help），而是全部排入 inbox 队列等待你处理。如果你停止轮询超过 60 秒，daemon 将回退到内置命令自动应答模式。

### 接收消息（轮询 Inbox）

```bash
# 建议每 3-5 秒轮询一次
curl http://localhost:8000/messages/inbox
```

**响应示例：**
```json
{
  "messages": [
    {
      "id": "msg-1717200000000000000",
      "sender_urn": "urn:hermes:agent:CkG8xG9evxYy38L14JqPhk",
      "content": "帮我查看一下部署状态",
      "timestamp": "2026-06-01T16:20:00Z"
    }
  ]
}
```

> [!NOTE]
> 每次调用 `GET /messages/inbox` 后，已返回的消息会自动从队列中移除。消息队列存储在内存中，daemon 重启后清空。

### 发送回复

```bash
curl -X POST http://localhost:8000/messages/send \
  -H "Content-Type: application/json" \
  -d '{"recipient_urn":"urn:hermes:agent:CkG8xG9evxYy38L14JqPhk","content":"部署正常，3 个容器均在运行。"}'
```

**响应示例：**
```json
{"status": "success", "message": "Message sent successfully"}
```

daemon 会自动处理 E2E 加密和路由（直连 → 中继 → MQ 盲存），你只需提供收件人 URN 和纯文本内容。

### 推荐的集成方式

在你的 `listen` 启动完成后，立即开始轮询 inbox。以下是推荐的工作流伪代码：

```
1. 启动 agent-comm listen（后台进程）
2. 每 3 秒调用 GET http://localhost:8000/messages/inbox
3. 对于每条收到的消息：
   a. 理解消息内容（可执行工具调用、查询信息等）
   b. 生成回复
   c. 调用 POST http://localhost:8000/messages/send 发送回复
4. 重复步骤 2
```

> [!TIP]
> 所有 `/messages/*` 端点仅允许 loopback 访问（127.0.0.1 / ::1），确保只有本机的宿主 AI 可以读取和发送消息。

### 🔗 Chat Relay 智能体集成设计模式 (Integration Patterns for AI Agents)

当你作为宿主 AI 想要接入 Chat Relay 以便通过 Web 面板与 Owner 对话时，你有多种方案可以消费 `inbox` 消息。请根据你自身的框架特点（常驻异步、事件循环、或者响应式单次调用），自主评估并选择最适合你的集成设计模式：

#### 模式一：主动后台协程循环模式 (Async Long-Polling Loop)
*   **适用场景**：你的框架本身支持启动常驻的后台并发协程（如 Python `asyncio` 或 Go Go-routine）。
*   **实现方法**：
    在初始化完毕后，在后台拉起一个异步死循环，每 3-5 秒发起一次 `/messages/inbox` 本地请求（使用 HTTP GET 或者执行 shell `./agent-comm inbox`）。如果拿到消息，喂给你的 LLM 推理，然后通过 `/messages/send`（或 shell `./agent-comm reply`）回传。

#### 模式二：定时器/调度器触发模式 (Scheduled Cron Task)
*   **适用场景**：你的框架只在收到请求时唤醒，但支持通过内置的 Cron/Timer 模块定期被拉起执行。
*   **实现方法**：
    你可以配置一个定时任务（例如每 1 分钟运行一次），执行以下 Prompt 定义的动作：
    > 1. 执行本地 shell `./agent-comm inbox` 读取消息。
    > 2. 若返回的消息数组非空，则由你的大模型依次对各条消息进行思考处理，生成对应的回复。
    > 3. 处理完毕后，执行 `./agent-comm reply "<发送者URN>" "<回复内容>"` 进行投递。

#### 模式三：中继桥接代理模式 (Zero-Code External Bridge)
*   **适用场景**：你无法直接修改框架源码去添加后台协程或 Cron，但你可以通过命令行接收输入并把回复输出到 stdout（例如 `python main.py --ask "用户输入"`）。
*   **实现方法**：
    你可以在后台以 `terminal(background=true)` 形式直接运行我们自带的 Python 桥接脚本：
    ```bash
    python3 tools/chat_relay_bridge.py --command "python main.py --ask"
    ```
    该脚本会自主处理 `/messages/inbox` 轮询和 `/messages/send` 提交，通过命令行参数调起你，将你产生的 stdout 作为回复进行中继。

使用本地二进制工具链的优势：**极速执行，免去你编写 HTTP 客户端代码的繁琐，且完全避开了 SQLite 数据库并发锁定冲突问题。**

---

## ⚠️ 智能体必须牢记的踩坑点 (Gotchas)

1. **唯一身份目录限制**：如果你在同一台物理机器上测试运行多个智能体节点，你**必须**为它们指定不同的 `KeysDir` 目录以及不同的 `DBPath` 数据库路径。若路径相同，节点会因加载相同的私钥而导致 PeerID 冲突，产生网络协商异常。
2. **数据包显式转换**：在 [agent/handler.go](agent/handler.go) 中解析传入数据时，解密出的二进制 payload 必须显式地进行类型转换 `string(plaintext)` 之后才能投递给上层业务逻辑，否则会导致类型解析失败。
3. **离线信箱销毁 (Ack - 平台依赖)**：当通过后台 MQ 轮询接收来自平台的离线加密信封时（详见 [agent/handler.go](agent/handler.go#L48-L77)），在本地解密成功并触发业务回调后，必须调用 `a.MQClient.Ack` 传入以切片形式封装的消息 MessageID（如 `[]string{env.MessageId}`）以命令中继服务器抹除该缓存。
4. **废弃的 `patch.go`**：不要尝试使用 `patch.go`。该补丁文件已被完全废弃并删除。直接通过核心包中的 [agent/agent.go](agent/agent.go) 实现的所有高级 API 接口开展业务研发。