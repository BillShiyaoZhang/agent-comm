---
name: agent-comm
description: >
  混合 P2P 加密智能体消息 SDK：libp2p + DHT + 双棘轮 + 平台。
  项目路径: ~/.openclaw/workspace/skills/agent-comm/
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
* **操作方式**：定义 [Config](file:///c:/Users/zhang/Developer/agent-comm/agent/config.go) 结构体中的配置参数（如身份密钥目录 `KeysDir`、数据库路径 `DBPath`、监听端口等），然后调用 [InitIdentity](file:///c:/Users/zhang/Developer/agent-comm/agent/agent.go#L42-L110)。这完全在本地运行，不依赖云端。

### 目标 2：交换通信名片与互加好友 (纯 Skill 本地功能)
要与另一个智能体通信，你需要它的**联系人卡片（Contact Card）**，它也同样需要你的卡片。
> [!NOTE]
> 智能体在网络中的唯一数字身份称为 **URN (Uniform Resource Name，统一资源名称)**，其格式为 `urn:hermes:agent:<fingerprint>`。
* **分享名片**：调用 [GenerateContactCard](file:///c:/Users/zhang/Developer/agent-comm/agent/contact_card.go#L223-L225) 导出你的名片文本，完全基于本地生成，无需向服务器注册。
* **导入名片**：获取对方名片的文本块，然后调用 [ImportContactCard](file:///c:/Users/zhang/Developer/agent-comm/agent/contact_card.go#L228-L230)。这会自动将对方的物理网络地址写入本地 peerstore，并在 `contacts` 数据库中保存其静态公钥。

### 目标 3：发送加密消息 (自适应降级，按需依赖 Platform)
* **操作方式**：调用 [SendMessage](file:///c:/Users/zhang/Developer/agent-comm/agent/agent.go#L116-L201) 并传入目标的 URN 和纯文本。
* **执行与路由降级逻辑**：
  1. **寻址阶段**：并发查询本地 Kademlia DHT（**纯 Skill 运行**）与 平台超级 Registry（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。
  2. **直连尝试**：首选直接向目标节点建立 TCP/QUIC 直连（**纯 Skill 运行**），并在此直连上跑本地双棘轮加密流进行投递。
  3. **中继中转**：若直连失败，尝试通过平台的 Relay v2 中继打洞转发（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。
  4. **离线盲存**：若目标离线，自动将消息在本地用双棘轮加密封入信封，发送至平台的离线消息队列 MQ 存储（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。

### 目标 4：持续监听并消费来信 (按需依赖 Platform)
* **操作方式**：调用 [OnMessage](file:///c:/Users/zhang/Developer/agent-comm/agent/agent.go#L205-L208) 并挂载你的回调函数。
* **底层机制**：
  - 本地监听实时入站连接与双棘轮加密流（**纯 Skill 运行**）。
  - 后台拉起周期轮询，从平台的离线邮箱中拉取密文信封，本地解密后发送 Ack 销毁（**依赖 [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**）。

---

## 💻 智能体代码参考速查表 (Code Reference)

在生成或编写集成代码时，请直接参考以下 Go 语言调用模式。

### 1. 初始化智能体身份与网络
```go
import (
    "context"
    "fmt"
    "github.com/nousresearch/hermes-agent/agent-comm/agent"
    "github.com/libp2p/go-libp2p/core/peer"
)

func StartAgent(ctx context.Context, bootstrapAddrStr string) (*agent.Agent, error) {
    // 1. 解析引导节点的 Multiaddr 物理地址 (属于 Platform 节点)
    var bootstrapNodes []peer.AddrInfo
    if bootstrapAddrStr != "" {
        addr, err := multiaddr.NewMultiaddr(bootstrapAddrStr)
        if err == nil {
            info, err := peer.AddrInfoFromP2pAddr(addr)
            if err == nil {
                bootstrapNodes = []peer.AddrInfo{*info}
            }
        }
    }

    // 2. 初始化 URN 身份、P2P 节点与持久化状态机 (本地 Skill 初始化)
    a, err := agent.InitIdentity(ctx, agent.Config{
        KeysDir:        "./my_identity_keys",      // 存储身份密钥的文件夹
        DBPath:         "./my_dr_sessions.db",    // 双棘轮 SQLite 持久化路径
        ListenAddrs:    []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
        BootstrapNodes: bootstrapNodes,
    })
    if err != nil {
        return nil, fmt.Errorf("failed to init: %w", err)
    }

    return a, nil
}
```

### 2. 名片的生成与导入 (本地 Skill 功能)
```go
// 导出并打印我自己的名片文本，以便分享给他人
cardText, err := a.GenerateContactCard()
if err != nil {
    // 处理错误
}
fmt.Printf("我的通信名片:\n%s\n", cardText)

// 导入收到的对方名片文本，保存并信任该联系人
partnerCard := `-----BEGIN AGENT-COMM CONTACT CARD-----
Ed25519PK: ...
X25519PK: ...
Addrs: /ip4/...
Bootstrap: /ip4/...
-----END AGENT-COMM CONTACT CARD-----`

contact, err := a.ImportContactCard(partnerCard, "合作辅助智能体")
if err != nil {
    // 处理错误
}
fmt.Println("成功添加联系人，其 URN 为:", contact.URN)
```

### 3. 发送消息与开启监听 (自动决定是否连接平台)
```go
// 开启接收监听（包含实时连接和离线信箱轮询）
a.OnMessage(ctx, func(senderURN string, msg string) {
    fmt.Printf("收到 [%s] 的加密来信: %s\n", senderURN, msg)
})

// 向目标的 URN 标识发送加密消息
err := a.SendMessage(ctx, contact.URN, "你好，这是专属我们的加密消息！")
if err != nil {
    fmt.Println("加密发送失败:", err)
}
```

---

## ⚠️ 智能体必须牢记的踩坑点 (Gotchas)

1. **唯一身份目录限制**：如果你在同一台物理机器上测试运行多个智能体节点，你**必须**为它们指定不同的 `KeysDir` 目录以及不同的 `DBPath` 数据库路径。若路径相同，节点会因加载相同的私钥而导致 PeerID 冲突，产生网络协商异常。
2. **数据包显式转换**：在 [agent/handler.go](file:///c:/Users/zhang/Developer/agent-comm/agent/handler.go) 中解析传入数据时，解密出的二进制 payload 必须显式地进行类型转换 `string(plaintext)` 之后才能投递给上层业务逻辑，否则会导致类型解析失败。
3. **离线信箱销毁 (Ack - 平台依赖)**：当通过后台 MQ 轮询接收来自平台的离线加密信封时（详见 [agent/handler.go](file:///c:/Users/zhang/Developer/agent-comm/agent/handler.go#L48-L77)），在本地解密成功并触发业务回调后，必须调用 `a.MQClient.Ack` 传入以切片形式封装的消息 MessageID（如 `[]string{env.MessageId}`）以命令中继服务器抹除该缓存。
4. **废弃的 `patch.go`**：不要尝试使用 `patch.go`。该补丁文件已被完全废弃并删除。直接通过核心包中的 [agent/agent.go](file:///c:/Users/zhang/Developer/agent-comm/agent/agent.go) 实现的所有高级 API 接口开展业务研发。