# 目录整理与删除依据

## 稳定接口

公共 Go 包路径、`cmd/helper`、`cmd/bootstrap`、根 README/SKILL，以及下游引用的 `tools/test_helper_platform.py` 保留。Python runtime 和宿主适配器沿用现有包路径；它们已有独立打包边界，目前无需新增仓库。

文档集中到 `docs/architecture`、`docs/guides`、`docs/planning`、`docs/maintenance`。`cmd` 只保留生产入口；学习程序放 `examples`；指定平台的手工检查放 `tests/integration`；包内自动测试继续与源码相邻。

## 删除依据

| 原内容 | 处理依据 |
| --- | --- |
| 根 `bootstrap` | 被跟踪的约 33 MB 编译产物；源码入口和 Release 构建保留 |
| `fix_dr.sh`、`update.sh` | 对同一 `DRStore` 字段做相反文本补丁的一次性脚本，当前实现已完成 |
| `scripts/commit-hardcoded-path-fixups.sh` | 固定旧文件集的自动提交脚本，引用已不存在的 CLI 路径，不是构建/发布入口 |
| `scratch/test_integration.py` | 硬编码临时目录、旧联系人请求与公网测试；由隔离的真实 helper/platform 工具替代 |
| `cmd/debug_dh`、`cmd/test_debug`、`cmd/test_hkdf_check`、`cmd/test_kdf` | 复制算法或打印中间密钥的开发实验，不验证现役包；保留调用实际 `dr` 的 ratchet/persistence 示例 |
| `cmd/test_dr_net` | 调用已禁用的无签名 Registry API；现役 `agent/integration_test.go` 验证真实本地通信 |
| `docs/idea.md`、`docs/PLATFORM_DESIGN.md`、`docs/AGENT_SKILL_DESIGN.md` | 与其他文档重复、包含已过时路由或未实现网关；由当前架构和协议索引替代 |
| `docs/FUTURE_PLANS*.md` | 已把存在的 CI/manifest 写成未来工作；合并为明确现有边界的 roadmap |

根 SKILL 中已明确失效的流程已用当前安装与消费合同替换；安装入口路径保持稳定。旧 SPEC/OVERVIEW/TUTORIAL 和 DR 批注中的过时算法、无签名注册、失效 CLI 和测试路径已经移除。详细 protobuf/签名规则链接源码，避免维护与实现漂移的副本。历史内容可从 Git 历史获取。

## 验证规则

`go test ./...` 覆盖 SDK 包和可编译示例；进程层检查使用 `tools/test_helper_platform.py`。文档移动同时维护相对链接、GitHub URL 和 Release 文档包。SDK 改动先提交/上传本仓库，再更新 Platform 子模块，最后更新部署仓库。
