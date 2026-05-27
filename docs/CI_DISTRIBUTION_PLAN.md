# agent-comm 持续集成与二进制分发设计方案 🚀

为了极大降低用户与外部智能体（Agent）接入本通信技能的门槛，解决 `go run` 带来的环境依赖、编译耗时以及后台 TTY 标准输出（Stdout）缓冲延迟等问题，本项目设计了基于 **GitHub Actions** 的持续集成（CI）与多平台二进制自动化分发方案。

---

## 💡 为什么需要二进制分发？

在多智能体协作场景中，用户可能会为不同的 Agent（例如各类智能体、AI 编码助手等）配置并初始化本技能。如果使用原始的源码运行模式：
1. **环境摩擦大**：对端智能体所在的环境必须安装 Go 编译器（且版本需符合 Go 1.25+），配置 `GOPATH` 等环境变量，经常因环境缺失而失败。
2. **启动延迟高**：每次运行 `go run` 都需要消耗 5~10 秒进行即时编译，响应迟钝。
3. **输出重定向截断**：由于 `go run` 包装进程以及后台重定向，导致日志输出转为“块缓冲”，在没有分配 TTY 时无法实时观测状态。

通过 **GitHub CI** 自动化构建，我们将为多平台提供预编译好的单文件二进制包。智能体或用户只需直接下载对应系统的文件即可**毫秒级启动**，且彻底解决输出刷新延迟的问题。

## 📦 Release 资产契约

每个 GitHub Release 默认应当包含以下最小资产：
- 对应平台的预编译二进制文件。
- `SHA256SUMS` 校验文件，用于快速验证下载完整性。
- `release-manifest.json` 机器可读清单，告诉 agent 应该下载哪些资产。
- `agent-comm-docs.zip` 轻量文档包，包含 README 与 skill 手册，方便离线 agent 直接阅读运行说明。
- `release_manifest_fetch.py` 自动化辅助脚本，方便 agent 在只拿到 manifest 后继续下载其余资产。

正常运行时，agent 只需要先读取 `release-manifest.json`，再下载匹配平台的二进制和校验文件，不需要 clone 整个仓库。

其中二进制资产会带有 `kind` 与 `platform` 元数据，便于自动化脚本无需猜测文件名就能选中正确目标。

---

## 🛠️ GitHub Actions 自动化工作流配置

在项目根目录下创建 `.github/workflows/release.yml`，即可在每次推送版本 Tag（例如 `v1.0.0`）时，自动触发多平台交叉编译并创建 GitHub Release。实际发布时还会额外生成 `SHA256SUMS`、`release-manifest.json`，以及一个轻量的 `agent-comm-docs.zip` 文档包和 `release_manifest_fetch.py` 辅助脚本，让 release 资产可以被 agent 直接消费。

以下是完整的 workflow 配置模板：

```yaml
name: Release agent-comm CLI

on:
  push:
    tags:
      - 'v*' # 仅在推送版本 tag (例如 v1.0.0) 时触发

permissions:
  contents: write

jobs:
  build-and-release:
    name: Build and Release CLI Binaries
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          # Linux amd64
          - goos: linux
            goarch: amd64
            artifact_name: agent-comm-linux-amd64
          # Windows amd64
          - goos: windows
            goarch: amd64
            artifact_name: agent-comm-windows-amd64.exe
          # macOS amd64 (Intel)
          - goos: darwin
            goarch: amd64
            artifact_name: agent-comm-darwin-amd64
          # macOS arm64 (Apple Silicon M1/M2/M3)
          - goos: darwin
            goarch: arm64
            artifact_name: agent-comm-darwin-arm64

    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25.10'

      - name: Run Build
        working-directory: ./
        run: |
          GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} CGO_ENABLED=0 \
          go build -ldflags="-s -w" -o build/${{ matrix.artifact_name }} ./cmd/client/main.go

      - name: Upload Build Artifacts
        uses: actions/upload-artifact@v4
        with:
          name: ${{ matrix.artifact_name }}
          path: build/${{ matrix.artifact_name }}

  publish:
    name: Create GitHub Release
    needs: build-and-release
    runs-on: ubuntu-latest
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Download All Artifacts
        uses: actions/download-artifact@v4
        with:
          path: build-artifacts

      - name: Prepare Release Binaries
        run: |
          mkdir -p release-dist
          # 整理下载归档的文件并移动到统一发布目录
          find build-artifacts -type f -exec cp {} release-dist/ \;
          ls -la release-dist/

      - name: Create GitHub Release and Upload Assets
        uses: softprops/action-gh-release@v2
        with:
          files: release-dist/*
          draft: false
          prerelease: false
          generate_release_notes: true
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

---

## 🤖 优化后的智能体（Agent）极速初始化流程

启用预编译发布后，用户直接发给 Agent 的一键部署 Prompt 可以优化为更简易的指令（Agent 不需要下载或安装 Go 编译器）：

### 优化后的 Prompt 模板：

```text
你的目标是为我启用并初始化安全智能体通信（agent-comm）。请依次执行以下步骤：
1. 识别你当前的操作系统（Linux / macOS / Windows）及架构（amd64 / arm64）。
2. 从项目的 GitHub Release 页面先下载 `release-manifest.json`，根据其中的清单自动定位并下载对应平台的预编译二进制客户端和 `SHA256SUMS`。
3. 赋予该二进制文件可执行权限，并将其保存在你的工作目录中：`chmod +x ./agent-comm`。
4. 运行二进制文件初始化身份：运行 `./agent-comm share` 生成你的唯一身份公钥与通信名片（Contact Card），并将名片文本打印展示给我，以便我分享给其他合作智能体。
5. 后台启动长期监听：在后台拉起监听进程 `./agent-comm listen > ./agent-comm.log 2>&1 &`。由于这是原生编译的二进制，它将完美规约标准输出缓冲，实现毫秒级拉取与直连消息的响应。
6. 向我报告初始化结果，包括你的 URN 标识、PeerID 以及后台进程 PID。
```

---

在当前实现中，GitHub Release 上传的资产已经包含二进制、`SHA256SUMS` 和 `release-manifest.json`，因此 agent 可以完全依赖 release 资产完成初始化，不需要先检出源码。

## 📈 方案优势总结

1. **零安装负担**：无需安装 Go 环境，智能体可以直接运行 `agent-comm` 程序，极大提升冷启动成功率。
2. **解决重定向块缓冲延迟**：原生编译的进程不经过 `go run` 的包装管道，可以完美以行缓冲方式将日志流入外部文件，便于外部智能体实时 grep 观测。
3. **安全审计与一致性**：CI 系统编译的二进制带有代码哈希溯源，避免不同智能体环境因 Go 编译器小版本差异带来的加密库兼容性问题。
