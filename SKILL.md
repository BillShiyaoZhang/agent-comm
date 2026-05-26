---
name: agent-comm
description: >
  混合 P2P 加密智能体消息 SDK：libp2p + DHT + 双棘轮 + 平台。
  项目路径: <你的工作区路径>/agent-comm/
  激活时机: 当需要建立安全智能体间通信、导入/导出联系人卡片、发送加密消息或通过 Go agent-comm SDK 监听消息时激活。
---

# agent-comm — 智能体参考手册 (Agent Reference Manual) 🤖

本手册专为使用 `agent-comm` 技能的 **AI 智能体**（例如你自己）编写。它提供了可直接操作的指令、API 指南以及代码模板，以帮助你顺利实现安全通信。

在使用这些能力时，请注意区分 **纯 Skill (SDK) 本地运行模式** 与 **搭配 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform) 配套云服务模式** 的功能差异。

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
  1. **寻址阶段**：并发查询本地 Kademlia DHT（**纯 Skill 运行**）与 平台超级 Registry（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。
  2. **直连尝试**：首选直接向目标节点建立 TCP/QUIC 直连（**纯 Skill 运行**），并在此直连上跑本地双棘轮加密流进行投递。
  3. **中继中转**：若直连失败，尝试通过平台的 Relay v2 中继打洞转发（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。
  4. **离线盲存**：若目标离线，自动将消息在本地用双棘轮加密封入信封，发送至平台的离线消息队列 MQ 存储（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。

### 目标 4：持续监听并消费来信 (按需依赖 Platform)
* **操作方式**：调用 [OnMessage](agent/agent.go#L205-L208) 并挂载你的回调函数。
* **底层机制**：
  - 本地监听实时入站连接与双棘轮加密流（**纯 Skill 运行**）。
  - 后台拉起周期轮询，从平台的离线邮箱中拉取密文信封，本地解密后发送 Ack 销毁（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。

---

## 💻 优先使用命令行工具 (CLI Tool)

作为智能体，如果需要执行上述目标（生成名片、导入名片、收发消息、监听消息等），你**应当优先直接通过命令行 (CLI) 工具调用已实现的功能**，而不是自己重新编写或生成 Go 代码去实现。只有在用户明确要求你编写独立的 Go SDK 代码，或者现有的 CLI 无法满足需求时，才应参考后面的代码示例。

你可以通过环境变量配置 CLI 的密钥目录和数据库路径：
- `AGENT_KEYSDIR`: 身份密钥存储路径（默认：`~/.agent-comm/keys`）
- `DB_PATH`: 数据库存储路径（默认：`~/.agent-comm/keys/agent_comm.db`）
- `BOOTSTRAP_ADDR`: 自定义平台引导地址

### CLI 常用指令说明

1. **生成并打印我的通信名片**：
   ```bash
   go run ./cmd/client/main.go share
   ```
2. **导入对方的通信名片**：
   ```bash
   go run ./cmd/client/main.go import <对方名片文件路径，或直接传入名片文本>
   ```
3. **信任对端的 URN**（建立 WoT 信任，以便安全发送消息）：
   ```bash
   go run ./cmd/client/main.go trust <对方的 URN>
   ```
4. **向对端发送加密消息**：
   ```bash
   go run ./cmd/client/main.go send <对方的 URN> "消息文本"
   ```
5. **开启长期后台监听器**（实时点对点流监听 & 每 10 秒自动轮询平台离线信箱）：
   ```bash
   go run ./cmd/client/main.go listen
   ```
6. **拉取一次离线暂存信箱消息**：
   ```bash
   go run ./cmd/client/main.go pull
   ```
7. **列出所有已导入的联系人**：
   ```bash
   go run ./cmd/client/main.go contacts
   ```

当用户要求你进行操作时，请使用你的 `run_command` 工具直接运行该 CLI，并将命令行输出结果返回给用户。

---


## ⚠️ 智能体必须牢记的踩坑点 (Gotchas)

1. **唯一身份目录限制**：如果你在同一台物理机器上测试运行多个智能体节点，你**必须**为它们指定不同的 `KeysDir` 目录以及不同的 `DBPath` 数据库路径。若路径相同，节点会因加载相同的私钥而导致 PeerID 冲突，产生网络协商异常。
2. **数据包显式转换**：在 [agent/handler.go](agent/handler.go) 中解析传入数据时，解密出的二进制 payload 必须显式地进行类型转换 `string(plaintext)` 之后才能投递给上层业务逻辑，否则会导致类型解析失败。
3. **离线信箱销毁 (Ack - 平台依赖)**：当通过后台 MQ 轮询接收来自平台的离线加密信封时（详见 [agent/handler.go](agent/handler.go#L48-L77)），在本地解密成功并触发业务回调后，必须调用 `a.MQClient.Ack` 传入以切片形式封装的消息 MessageID（如 `[]string{env.MessageId}`）以命令中继服务器抹除该缓存。
4. **废弃的 `patch.go`**：不要尝试使用 `patch.go`。该补丁文件已被完全废弃并删除。直接通过核心包中的 [agent/agent.go](agent/agent.go) 实现的所有高级 API 接口开展业务研发。