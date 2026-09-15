# 从源码开始

本教程面向维护者。接入现有 agent 从 [README](../../README.md) 和 [工程指南](ENGINEERING.md) 开始；这里的示例用于理解协议组件。

## 构建

在 SDK 仓库根目录运行，Go 版本要求见 `go.mod`：

```sh
go build -o build/agent-comm-helper ./cmd/helper
go test ./...
```

Windows 可为输出文件添加 `.exe`。`cmd/bootstrap` 是独立节点入口；生产服务端使用 Platform 仓库。

## 阅读顺序

1. [身份和密钥](../../crypto/keys.go)：区分用于签名的 Ed25519 与用于加密协商的 X25519。
2. [Registry](../../registry/client.go)：了解 URN 查询与签名记录验证。
3. [信封](../../crypto/envelope.go) 和 [session](../../session/session.go)：从消息到经过验证的加密数据。
4. [可靠消息](../../agent/reliable.go) 和 [helper mailbox](../../cmd/helper/mailbox.go)：理解本机持久接纳、平台入队和消费确认。
5. [dr](../architecture/DOUBLE_RATCHET.md)：单独研究有状态的 P2P 双棘轮实现。
6. [Python runtime](../../python/README.md) 和 [连接器](../../connectors/README.md)：理解宿主授权、原生工具执行与远程访问。

## 本地示例与集成检查

```sh
go run ./examples/ratchet
go run ./examples/ratchet_persistence
go run ./examples/mailbox
```

其余程序及运行条件见 [examples](../../examples/README.md)。示例用于观察行为；自动回归以 `go test` 和 [隔离进程测试](../../tests/README.md) 为准，不将示例最后一行日志当作系统完成证明。
