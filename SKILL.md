---
name: agent-comm
description: >
  P2P encrypted agent messaging: libp2p + DHT + Double Ratchet.
  Project: ~/.hermes/agent-comm/
  Activate when: setting up agent-to-agent comm, exchanging contacts via libp2p,
  sending DR-encrypted messages, or doing libp2p/DR development.
---

# agent-comm — Agent Reference

## 调用触发条件

遇到以下情况时激活此 skill：
- 用户要求两个 agent 之间建立通信
- 交换 contact 信息、通过 libp2p 发送加密消息
- 开发 libp2p / Double Ratchet 相关功能

## 项目路径

```
~/.hermes/agent-comm/
Go binary: ~/.local/go/bin/go (Go 1.25.10)
```

## 当前实现状态

| Phase | 状态 | 验证命令 |
|-------|------|---------|
| 1 | ✅ | `go run ./cmd/test_host/` |
| 2 | ✅ | `go run ./cmd/test_session/` |
| 3 | ✅ | `go run ./cmd/test_mq/` |
| 4b | ✅ | `go run ./cmd/test_dr/` + `./cmd/test_dr_net/` |
| 5 | ✅ | `go run ./cmd/test_dr_persist/` |
| 6 | ✅ | `go run ./cmd/test_dr_net/` |
| 4a WoT | ⚠️ | 未集成，无测试命令 |

## Double Ratchet 容易踩的坑

### 1. 两边都要注册 DR stream handler

如果只注册一个方向的 DR handler，反方向的消息会报 "protocols not supported"。

```go
// 正确：每个节点都注册 DR 协议
hostA.SetStreamHandler(dr.ProtoID, handlerA) // /agent/dr/1.0.0
hostB.SetStreamHandler(dr.ProtoID, handlerB)
```

### 2. `mgr` 必须在 `SetPeerX25519PK` 之前创建

`SetPeerX25519PK` 写入 `Manager.peerX25519PK` map。如果 manager 还没创建，缓存就丢了。

```go
// ✅ 正确顺序
mgrB := session.NewManager(hostB, keysB)
mgrB.SetPeerX25519PK(aPeerID, aX25519PK)

// ❌ 错误：mgrB 此时还是 nil
mgrB.SetPeerX25519PK(...)
mgrB := session.NewManager(hostB, keysB)
```

### 3. Simplex 模式下 EOF 是正常的

每条消息一个独立的 stream，接收方关闭流后发送方读 response 会看到 `io.EOF`。这不是错误，要宽容处理：

```go
respSize, err := readUint32BE(stream)
if err != nil {
    if err == io.EOF {
        return nil // simplex: 对端不回复，正常
    }
    return fmt.Errorf("read response size: %w", err)
}
```

### 4. DR responder 需要知道发送方的 X25519 PK

`Receive()` 调用 `mgr.PeerStaticX25519PK(senderPeerID)` 查找。找不到就报 "peer static X25519 PK not found"。解决方案：在 DR handler 执行前，用 `mgr.SetPeerX25519PK()` 缓存发送方的公钥。

## ECIES 会话设计要点（Phase 2）

**AAD 常量**：双方都用 `SHA256("agent-comm-v1")[:16]`。不要用 PeerID 或 URN 作为 AAD——双向加密时两者不对称，会导致 GCM 认证失败。

**加密流程**：
```
sender:  ECDH(sender_SK, recipient_PK) → shared_secret
         HKDF(shared_secret, "agent-comm-ephemeral-v1") → ephemeral (32B)
         HKDF(shared_secret, ephemeral) → encKey (32B)
         AES-GCM(encKey, nonce, payload, AAD=ProtoAAD) → ciphertext+tag

recipient: ECDH(recipient_SK, sender_PK) → 同样的 shared_secret（ECDH 交换性）
           AES-GCM-Decrypt(encKey, ciphertext, nonce, tag, AAD=ProtoAAD) → plaintext
```

**关键接口**：
```go
NewManager(host, keys) *Manager
mgr.SendMessage(ctx, target, recipientPubKey, plaintext) (reply string, err)
mgr.SendReply(stream, recipientStaticPubKey, recipientURN, plaintext) error
mgr.BuildEnvelope(recipientPubKey, plaintext) (*proto.EncryptedEnvelope, error)
mgr.DecryptEnvelope(env) (string, error)
mgr.SetPeerX25519PK(peerID, pk []byte)
mgr.PeerStaticX25519PK(peerID) ([]byte, error)
```

## libp2p v0.36+ API 变更

旧代码迁移时注意：
- `h.ID().Pretty()` → `h.ID().String()`
- `h.Connect(ctx, peerID)` → `h.Connect(ctx, peer.AddrInfo)`
- import `github.com/libp2p/go-libp2p/core/host`（不是 `go-libp2p-core/host`）
- import `github.com/libp2p/go-libp2p/core/peerstore`（不是 `go-libp2p-core/peerstore`）
- `peerstore.TempAddrTTL` 通过 `core/peerstore` 访问

## 关键文件索引

| 文件 | 作用 | 关键类型/函数 |
|------|------|--------------|
| `dr/session.go` | DR 会话封装 | `DRSession`, `NewDRSessionInitiator`, `NewDRSessionResponder`, `SendMessage`, `Receive` |
| `dr/ratchet.go` | DR 核心算法 | `RatchetState`, `InitAlice`, `InitBobWithSS`, `MakeMessageKey`, `SerializeRatchetState` |
| `dr/store.go` | 持久化存储 | `DRStore`, `SaveSession`, `LoadSession`, `DeleteSession` |
| `session/session.go` | ECIES 会话管理 | `Manager`, `SetPeerX25519PK`, `PeerStaticX25519PK`, `SendMessage`, `SendReply` |
| `registry/client.go` | URN 解析/注册 | `Resolve`, `Register` |
| `mq/client.go` | 离线消息 | `Store`, `Retrieve`, `Ack` |
| `crypto/keys.go` | 密钥管理 | `IdentityKeys`, `LoadOrCreateIdentity`, `URN()` |
| `libp2p/host.go` | 主机创建 | `NewHost`, `Config` |
| `SPEC.md` | 完整架构规格 | 各 Phase 详细设计 |

## 依赖版本（已验证可用）

```
Go: 1.25.10
go-libp2p: v0.36.3
go-libp2p-core: v0.20.1（用 core/ 子路径 import）
go-libp2p-kad-dht: v0.39.2
quic-go: v0.45.2 (transitive)
```

## 编译与测试

```bash
cd ~/.hermes/agent-comm
~/.local/go/bin/go build ./...         # 全量编译
~/.local/go/bin/go run ./cmd/test_dr_net/  # DR 网络测试（最终验证）
```

## 已知限制

- **Phase 4a WoT**：`wot/` 包存在但未集成网络测试，stream handler 和 `cmd/test_wot/` 待建
- **Phase 4b DR**：当前为 simplex（每条消息一个 stream），非 full-duplex
- **Identity key 冲突**：同一机器多节点测试时必须用不同 `keysDir`，否则 PeerID 冲突