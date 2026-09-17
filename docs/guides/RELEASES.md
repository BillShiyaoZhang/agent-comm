# Release 资产与维护

实际发布逻辑见 [.github/workflows/release.yml](../../.github/workflows/release.yml)。发布由 SDK 仓库的 `v*` tag 或维护者触发工作流。

## 当前产物

- `agent-comm-helper`：Linux amd64、Windows amd64、macOS amd64/arm64。支持矩阵以工作流为准。
- Python runtime 与 Hermes connector 的两个 wheel，以及上述四个平台的完整接入 ZIP 和源码 ZIP。
- `release_manifest_fetch.py`：按平台选取并校验下载文件。
- `agent-comm-docs.zip`：根 README/SKILL、文档目录、skill 的 `references/` 参考、Python runtime 和 connector 的使用说明，保留相对路径。
- `SHA256SUMS` 和 `release-manifest.json`：文件大小、SHA256、类型及平台信息。
- `early-access-manifest.json`：完整接入包与源码 ZIP 的校验信息、包版本和源码提交；同步官网时将它保存为官网的 `downloads/release-manifest.json`。

工作流从 `cmd/helper` 编译二进制、构建两个 wheel，并复用部署仓库的打包器生成完整接入包，再组装文档包和校验清单。SDK 下载器使用的资产清单与接入包清单分别保存；不提交构建产物到源仓库。

## 变更时检查

1. 移动文档时同步文档包清单和本地/GitHub 链接，保持根 `SKILL.md` / `SKILL_EN.md` 安装入口稳定。
2. 检查 manifest 的平台名、文件名与下载器匹配。构建测试通过不等于远程 Release 已发布。
3. Python runtime 和宿主 connector 发布 wheel，并随完整接入 ZIP 提供。安装命令与宿主依赖见各包 README；Hermes 需预先安装。
4. 先推送 SDK 提交，再更新并推送 Platform 和部署仓库的固定提交，最后给该 SDK 提交打 tag 并发布；手动发布填写匹配的 `deployment_ref`。协议变更需按迁移指南验证已有状态。

二进制构建、校验和及文档打包已经实现，旧“待开发 CI”路线图不再维护。
