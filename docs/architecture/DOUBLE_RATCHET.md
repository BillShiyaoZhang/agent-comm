# Double Ratchet 源码导读

`dr` 提供 Go SDK 的状态化 P2P 加密路径。helper 的可靠 HTTPS MQ 使用独立信封合同，不能将本页的状态机性质直接套到它上面。

## 三个模块

| 模块 | 职责 |
| --- | --- |
| [ratchet.go](../../dr/ratchet.go) | `RatchetState`、初始化、消息/DH 链推进、加解密和序列化 |
| [session.go](../../dr/session.go) | libp2p stream 封装、发起/响应会话和消息读取 |
| [store.go](../../dr/store.go) | 按 peer URN 保存/恢复 SQLite 会话状态 |

## 状态与消息

`RatchetState` 保存本机/对端 DH 公钥、私钥、根密钥、收发链密钥和计数器，并保留 `origRootKey` 与 `firstDH` 处理初始化阶段。初始化分别使用 `InitAlice` / `InitBobWithSS`，首次接收与后续消息的推进有不同路径，阅读时从这些函数进入。

`kdfMessageKey` 通过 HKDF-SHA256 派生消息密钥，并通过 HMAC-SHA256 推进链密钥。`encryptWithKey` 使用 XChaCha20-Poly1305，随机 nonce 放在密文前。根链推导与 DH 更新见 `kdfRootChain` / `dhRatchet`，不维护另一份可运行算法副本。

`SerializeHeader` 写入 40 字节头：32 字节发送方 DH 公钥、4 字节 PN、4 字节消息编号，整数为大端。`DRSession` 在 `/agent/dr/1.0.0` stream 上处理帧，具体初始化和帧边界以源码为准。

这份实现不是完整 Signal 协议的兼容性承诺。旧稿中的伪结构、不存在的方法名、错误帧字段顺序和未经验证的安全保证已移除。

## 持久化

`RatchetState.Serialize()` 和 `DeserializeRatchetState()` 负责状态编码。`DRStore` 开启 SQLite WAL，公开接口为：

```go
SaveSession(peerURN, peerID string, state *RatchetState) error
LoadSession(peerURN string) (*RatchetState, bool, error)
DeleteSession(peerURN string) error
```

这些接口提供保存/恢复操作；具体通信路径什么时候提交、重启后怎样恢复，需要同时检查调用方，不能仅因存在数据库就假设任意崩溃点都已获得端到端持久保证。

## 本地观察

`go run ./examples/ratchet` 调用实际 `dr` 包，`go run ./examples/ratchet_persistence` 检查 SQLite 状态往返。包级网络验证在 `agent/integration_test.go`。构建命令和整体可靠消息边界见 [工程指南](../guides/ENGINEERING.md)。
