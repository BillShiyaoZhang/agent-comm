---
name: agent-comm
description: 安装、升级和使用 Agent Comm 本机 helper、共享 Python runtime 及宿主原生连接器，为用户授权的 agent 提供身份、可靠消息与协作接入。
---

# Agent Comm 安装与使用

## 适用范围与入口

用户要接入、更新 Agent Comm 或与已授权的 agent 通信时使用。先阅读 [项目 README](README.md)，再按实际宿主选择 [Hermes](connectors/hermes-platform/README.md) 或 [OpenClaw](connectors/openclaw-channel/README.md)。Hermes 个人协作行为使用插件随包的 [personal-collaboration skill](connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md)。

## 安装或升级

1. 检查操作系统、实际 agent 安装、Python 环境与 profile。Hermes profile 通过宿主的 `hermes_constants.get_hermes_home()` 解析，不假设固定家目录。
2. 优先使用 [早期接入包说明](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md) 提供的匹配组件。已有身份目录、消息数据库、联系人、授权和消费记录继续保留；不要重新初始化到另一个目录来“解决”升级问题。
3. 将 Python runtime 和 connector 安装到实际运行宿主的环境，按 connector 的兼容版本和配置要求操作。helper 二进制不包含宿主，也不能自行提供宿主原生确认流程。
4. 源码构建时从 SDK 根目录运行 `go build -o build/agent-comm-helper ./cmd/helper`。Release 下载器位于 `tools/release_manifest_fetch.py`，当前 helper 下载须带 `--helper`；下载与校验方法见 [Release 指南](docs/guides/RELEASES.md)。
5. 运行 `agent-comm-helper init <身份目录绝对路径>` 查看或创建身份，再运行 `agent-comm-helper daemon <身份目录绝对路径> <云端HTTPS地址> [本机端口]`。默认端口为 45042，每个身份一个 helper、每个 inbox 一个活跃消费者。
6. Hermes 的 `platform_url` 指向本机 `http://127.0.0.1:45042`，填写本机身份和明确的 `allow_from` 对方地址。个人协作使用 `collaboration_enabled`；远程工作台另用 `remote_enabled` 和本机配对。按真实授权范围配置。

## 运行与消息语义

helper 本机接口只供受信任的本机进程调用；云端同名接口使用另一套签名密文合同，不能仅替换 URL。

| 操作 | 本机接口 |
| --- | --- |
| 身份/进程信息 | `GET /info` |
| 持久接纳出站 | `POST /api/v1/mq/store` |
| 查询本机投递状态 | `GET /api/v1/mq/status?message_id=...` |
| 读取/订阅未消费消息 | `GET /api/v1/mq/retrieve` / `GET /api/v1/mq/subscribe` |
| 确认本机消费 | `POST /api/v1/mq/ack` |

字段、限额、重试及示例统一见 [helper 接口合同](docs/guides/HERMES_INTEGRATION.md)。出站使用稳定 `message_id`；重试复用同一请求。SSE 重连可能重放，消费者在处理完成或持久接纳后 ACK 本机 helper。

当前可靠发送走 HTTPS MQ：`accepted` 是本机持久接纳，`platform_queued` 是平台入队，两者都不是对方任务完成。传统 Go SDK P2P/Double Ratchet 是单独的路径，不描述为 helper 默认先尝试的传输。

## 验证与维护

检查 `/info` 的实际 URN、宿主 SSE 连接，并按用户已授权的对象和内容完成双向收发验证。联系人地址本身不授予控制权限；远程配对与主人确认遵守宿主原生授权。

本地自动验证、隔离进程验证与真实模型工作分别报告结果，不将进程启动或平台入队当作业务完成。常驻服务见 [HELPER_SERVICE](docs/guides/HELPER_SERVICE.md)，开发和限制见 [工程指南](docs/guides/ENGINEERING.md)。
