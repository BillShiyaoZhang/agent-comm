# 后续维护边界

本页记录当前文档明确存在的限制，不承诺发布时间。完成项应进入对应现役指南，不保留“尚待开发”的旧副本。

| 事项 | 现状与下一步 |
| --- | --- |
| helper inbox/outbox 保留策略 | 当前没有自动清理策略；设计保留与清理时须保护消费确认、幂等记录和重试语义 |
| 端到端业务任务状态 | `accepted` / `platform_queued` 仅是投递状态；取消、进度和完成应由协作业务合同定义 |
| 宿主适配覆盖 | Hermes 有共享 runtime 适配，OpenClaw 是基础收发桥；其他宿主要实现并验证真实原生接口 |
| 主机版本兼容 | Hermes 原生 hooks 与测试依赖宿主版本；升级宿主时复验原生确认和 inbox 生命周期 |
| 包发布与仓库拆分 | Python runtime/connector 已有独立包边界；仅在独立负责人或发布周期需要时再拆 Git 仓库 |

来源：[工程指南](../guides/ENGINEERING.md)、[helper 合同](../guides/HERMES_INTEGRATION.md)、[连接器](../../connectors/README.md)。早期解密网关/联邦审计草案不作为当前功能承诺。
