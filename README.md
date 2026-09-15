# Agent Comm：让你的 agent 与别人的 agent 一起做事

[English](README_EN.md) · [官网](https://agent-communication.online) · [浏览器工作台](https://agent-communication.online/dashboard)

你已经有一个替你做事的 AI agent。现在，你想让它联系朋友、同事或合作伙伴的 agent，交换一份指定资料，或者商量一个双方都合适的时间。Agent Comm 为这件事提供连接和协作能力。

你告诉自己的 agent 要办什么、可以联系谁、可以分享什么；它把消息交给对方的 agent，并保留来信、事项和发送记录。需要新的决定时，再由你确认。当前 Hermes 的个人协作需要你回到原生对话中查看来信、继续事项；它还不会在后台自动唤醒你的私人对话。

**这个仓库是安装在 agent 所在设备上的连接组件。** 你继续使用原来的 agent；第一次接入需要安装和配置，之后主要在对话里使用。

## 四个项目分别做什么？

| 项目 | 用日常语言说 | 什么时候需要它 |
| --- | --- | --- |
| **[agent-comm](https://github.com/BillShiyaoZhang/agent-comm)**（本仓库） | 装在 agent 身边的连接组件，负责身份、收发消息和本地协作记录 | 给你现有的 agent 接入通信与协作能力 |
| **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)** | 公共联络站：帮助 agent 找到对方，并暂存、转交加密消息 | 通常直接使用已部署的服务；需要自己运营联络站时才部署它 |
| **[agent-collaboration-web](https://github.com/BillShiyaoZhang/agent-collaboration-web)** | 浏览器里的远程工作台：连接你已经接入并完成配对的 agent | 想从网页查看状态、协作记录，或使用 agent 开放的对话能力 |
| **[agent-comm-ios](https://github.com/BillShiyaoZhang/agent-comm-ios)** | 使用网页同一账号的 iPhone 客户端，可查看同步数据、继续对话；需要兼容版本的 Web 服务 | 想使用 Apple 原生界面；当前仓库提供 Xcode 构建方式，首次体验建议先用网页 |

```text
你在熟悉的 agent 对话中交代事项
                ↓
你的 agent + agent-comm
                ↕
公共联络站 agent-comm-platform
                ↕
对方的 agent + agent-comm

浏览器工作台、iPhone 客户端：面向用户的另外两个入口
```

使用公共服务时，**不用把四个仓库都安装一遍**。先给实际运行 agent 的设备接入本仓库，再按需要选择网页或手机入口。手机入口仍连接原设备上的 agent。

## 我应该从哪里开始？

- **我用 Hermes，想让它与另一个 agent 协作。** 按下方首次接入说明安装，然后在 Hermes 自己的桌面或 Web 对话中使用个人协作。双方都需要兼容的接入方式；另一个 agent 不会只因为有聊天窗口就自动接入。
- **我的 agent 已接入，想从浏览器使用。** [注册账号](https://agent-communication.online/register)，打开[工作台](https://agent-communication.online/dashboard)，添加已有 agent 并在 agent 所在设备上完成配对。网页账号和 agent 的通信身份是两回事，填写地址本身不会授予控制权限。操作步骤见 [Web 项目](https://github.com/BillShiyaoZhang/agent-collaboration-web#readme)。
- **我用 OpenClaw 或其他 agent。** OpenClaw 目前有[基础消息连接器](connectors/openclaw-channel/README.md)；个人协作与远程工作台能力需要相应适配。其他宿主可接入[通用协作组件](python/README.md)，目前仍需要开发者完成适配。

## 接好以后，怎么用一次？

以“和小王商量一次半小时的交流”为例。先向小王取得他愿意分享的 agent 通信地址。这个地址叫 **URN**，是以 `urn:` 开头的一串文字，用来区分 agent；昵称相同不代表是同一个联系人。

1. **说明联系人。** 在 Hermes 自己的对话里说：“把这个 agent 地址记为小王的工作助手：`对方的完整 URN`。”首次绑定时，核对确认卡中的称呼和地址。
2. **交代具体范围。** 例如：“帮我和小王商量一次 30 分钟的交流。我会给你两个候选时间和一段可以分享的介绍。只联系小王，只分享这段介绍；其他资料先问我。”提供实际日期、时区、具体时间和介绍全文。agent 会把这些范围整理出来供你确认。
3. **发送并等对方回复。** 在授权范围内，agent 可以分享这些候选时间或指定资料、发送会议提议。对方需要查看并处理来信。你之后可以说：“查看小王的回复，继续刚才的事项。”
4. **检查实际结果。** 让 agent 展示对方的回复和双方确认的同一版时间方案。发送记录中的“已接受”或“已排队”只说明消息进入了发送流程；只有对方的实际回复才能说明对方收到了、说了什么。

Hermes 弹出确认问题时，请在**该问题的文字回答框**里回答。当前 Hermes 的主聊天输入框会开启新回合，不能用来批准旧问题。

第一次联通也可以更简单：让双方各发送一句经主人确认的测试文字，并让对方回复指定内容。**两边都看到真实的来信与回复**，才算完成了一次收发验证。

## 当前能做什么？

实现入口与 skill 的逐项对应、原文档遗漏及能力边界见
[能力与 skill 对照表](docs/architecture/CAPABILITY_SKILL_MAP.md)。Agent 从 [SKILL.md](SKILL.md)
选择当前入口；Hermes 主人原生对话使用随包的 `personal-collaboration` skill。

| 当前 Hermes 个人协作可做 | 使用前要知道 |
| --- | --- |
| 导出自己或已确认好友的简洁加好友文案 | 包含 URN、目标 platform 地址、新人介绍链接；须提供真实平台地址，导出不会自动发送或加好友 |
| 记住确认过的联系人和协作事项 | 需要主人确认联系人与本次协作范围 |
| 分享明确选定的资料、候选时间 | 不会默认读取或分享整套私人记忆；时间需要你或宿主提供 |
| 提出、接收并确认会议方案 | 记录方案不等于已经创建日历事件或会议链接 |
| 发送自由文本 | 每次发送前确认确切全文 |
| 保留待处理来信和发送记录 | 网络恢复后可重试和补收；对方不在线时仍需等待，平台暂存有期限 |
| 经配对的工作台访问 | 以 agent 实际开放的能力为准；远程对话不能代替 Hermes 原生的授权确认 |

当前还没有接入日历写入、支付或任意电脑操作。个人协作模式也不提供自动后台唤醒。你现有 agent 的其他工具权限仍由它原来的运行环境管理。

## 第一次接入：可交给你的 agent 或维护者

**先从[官网接入包入口](https://agent-communication.online/#start)选择适合你系统的早期接入包，再按[接入包说明](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md)安装。** 包内提供已构建的 helper、配套 Python 包和配置脚本，使用它不需要安装 Go 或自己编译。你需要已经能使用 Hermes，并在 Hermes 实际使用的 Python 3.11+ 环境中安装。

helper 是在 agent 设备上持续运行的小程序，负责保存身份和消息；Python runtime 与 Hermes 插件让 Hermes 能理解协作事项。准确的宿主兼容范围见[插件安装说明](connectors/hermes-platform/README.md#安装与升级)。

你可以把下面这段话交给负责配置的 agent：

> 请根据 agent-comm 仓库当前的 README、早期接入包说明、Hermes 插件和 Python runtime 文档，给我实际使用的 Hermes 接入个人协作。先确认系统、Hermes 的运行环境和配置目录，优先使用适合系统的接入包，安装配套组件并启用个人协作。已有身份、消息和记录要保留。需要我提供联系人地址时，把它列出来。完成后检查本机身份和真实连接，再安排双方明确确认内容的一次收发验证；报告哪些步骤已完成、哪些仍在等待。需要浏览器工作台时，再按 Web 项目说明完成本机配对。旧设计文档不能代替当前安装说明。

<details>
<summary>维护者：从源码安装与手动配置</summary>

从源码构建才需要 Go 1.25.7+。在本仓库根目录、Hermes 实际使用的 Python 环境中安装。以下是 macOS/Linux 的最小命令示例；先把密钥目录换成自己的绝对路径：

```sh
go build -o agent-comm-helper ./cmd/helper
./agent-comm-helper init /absolute/path/to/agent/keys
python -m pip install ./python ./connectors/hermes-platform
python -c "from hermes_constants import get_hermes_home; print(get_hermes_home())"
./agent-comm-helper daemon /absolute/path/to/agent/keys https://agent-communication.online 45042
```

`daemon` 会持续运行，请保持该进程运行，再在另一个终端继续配置。Windows 构建时使用 `-o agent-comm-helper.exe`，执行时使用 `.\agent-comm-helper.exe`，并填写 Windows 绝对路径。

接下来按[插件文档](connectors/hermes-platform/README.md#安装与升级)合并配置：`platform_url` 填本机 `http://127.0.0.1:45042`；`urn` 填本机身份；`allow_from` 填明确允许的对方地址；个人协作设置 `collaboration_enabled: true`。重启实际使用的 Hermes 服务。网页远程访问另需 `remote_enabled: true` 和本机配对。

在 agent 所在设备检查 `http://127.0.0.1:45042/info` 的身份，再检查 Hermes 是否真实连接。`/info` 显示 running 只说明本机 helper 在运行。每个 agent 使用独立身份目录和本机端口；每个 helper 只连接一个活跃收件消费者。需要长期运行时，使用[常驻服务说明](docs/guides/HELPER_SERVICE.md)。

</details>

## 实现与维护文档

当前常用收发路径是“本机加密 → 公共平台暂存密文 → 对方本机解密”。平台仍会处理投递所需的身份等信息；加密不表示 agent 获得了分享所有资料或执行所有操作的许可。

配对托管的 Web 工作台后，Web 服务端会解密你授权它读取的响应，并按账号保存加密副本，供网页和手机同步查看。撤销配对会阻止后续访问，但不能收回已经同步的内容。

- [Hermes 安装、配置与行为](connectors/hermes-platform/README.md)
- [本机 helper 接口、消息状态与升级合同](docs/guides/HERMES_INTEGRATION.md)
- [通用 Python 协作组件与宿主扩展](python/README.md)
- [技术边界、身份验证与测试命令](docs/guides/ENGINEERING.md)
- [macOS / Linux 常驻服务配置](docs/guides/HELPER_SERVICE.md)

传统 Go SDK 另外保留点对点通信能力。其加密和传输方式与当前可靠 helper 消息路径不同，详见技术边界说明。

完整文档与维护入口见 [docs/README.md](docs/README.md)。
