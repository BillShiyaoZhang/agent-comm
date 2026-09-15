# Release 资产与维护

实际发布逻辑见 [.github/workflows/release.yml](../../.github/workflows/release.yml)。发布由 SDK 仓库的 `v*` tag 或维护者触发工作流。

## 当前产物

- `agent-comm-helper`：Linux amd64、Windows amd64、macOS amd64/arm64。支持矩阵以工作流为准。
- `release_manifest_fetch.py`：按平台选取并校验下载文件。
- `agent-comm-docs.zip`：根 README/SKILL、文档目录、skill 的 `references/` 参考、Python runtime 和 connector 的使用说明，保留相对路径。
- `SHA256SUMS` 和 `release-manifest.json`：文件大小、SHA256、类型及平台信息。

工作流从 `cmd/helper` 编译二进制，组装文档包后生成校验和与 manifest；不提交构建出的二进制到源仓库。

## 变更时检查

1. 移动文档时同步文档包清单和本地/GitHub 链接，保持根 `SKILL.md` / `SKILL_EN.md` 安装入口稳定。
2. 检查 manifest 的平台名、文件名与下载器匹配。构建测试通过不等于远程 Release 已发布。
3. Python runtime 和宿主 connector 是源码包，安装命令与宿主依赖见各包 README；helper 二进制不会自动包含 Hermes。
4. 协议变更先发布 SDK，再让 Platform 和部署仓库更新固定提交，并按迁移指南验证已有状态。

二进制构建、校验和及文档打包已经实现，旧“待开发 CI”路线图不再维护。
