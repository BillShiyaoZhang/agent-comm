# Agent Comm 文档 / Documentation

使用入口见 [中文 README](../README.md)、[English README](../README_EN.md) 和根目录的 [安装与能力 skill](../SKILL.md)。根 skill 文件是公开安装/分发入口，保留稳定路径。

## Guides

- [Engineering](guides/ENGINEERING.md)：现役 helper、runtime、connector、构建与测试。
- [Hermes integration contract](guides/HERMES_INTEGRATION.md)：HTTP/SSE、持久投递、重试和 ACK。
- [Persistent helper services](guides/HELPER_SERVICE.md)：常驻服务配置。
- [Source tutorial](guides/TUTORIAL.md)：从源码和本地示例开始。
- [Releases](guides/RELEASES.md)：二进制、文档包与机器可读清单。

## Architecture

- [能力与 skill 历史审计](architecture/CAPABILITY_SKILL_MAP.md)：记录 2026-09-15 基线与后续补充的能力覆盖、历史遗漏和接口边界；当前可用动作以运行时 `describe.action_fields` 和已安装宿主为准。
- [代码总览](architecture/OVERVIEW.md) / [English overview](architecture/OVERVIEW_EN.md)。
- [协议与源码契约](architecture/PROTOCOL.md) / [Protocol reference](architecture/PROTOCOL_EN.md)。
- [Agent 间 v2 隐私与合规信封](architecture/PROTOCOL_V2.md)：签名策略、临时握手、双密钥槽、持钥回执与 helper 接口。
- [信任验证](architecture/TRUST.md)、[Double Ratchet 代码说明](architecture/DOUBLE_RATCHET.md)。

## Planning and maintenance

- [产品原则](planning/PRINCIPLES.md)、[待办边界](planning/ROADMAP.md)。
- [整理依据与目录规则](maintenance/REORGANIZATION.md)。
- [示例](../examples/README.md)、[集成验证](../tests/README.md)。

Python runtime 和 connector 的包说明分别保留在 [python](../python/README.md) 与 [connectors](../connectors/README.md)，和各自发布文件放在一起。
