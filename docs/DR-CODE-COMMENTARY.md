# Double Ratchet 代码详解

> 配合 [SPEC.md](../SPEC.md) Phase 4b 节阅读效果更佳。

## 包概述

`dr/` 实现 Double Ratchet（Signal Protocol），三个文件分工：

| 文件 | 职责 |
|------|------|
| `dr/ratchet.go` | 核心算法：`RatchetState`、DH ratchet、symmetric ratchet、密钥派生 |
| `dr/session.go` | libp2p stream 封装：`DRSession`（Initiator/Responder）、消息格式 |
| `dr/store.go` | SQLite 持久化：每个 peer 的 `RatchetState` 存储 |

典型流程：

```
Alice (initiator)                        Bob (responder)
─────────────────                        ─────────────────
1. ECDH(Alice_SK, Bob_PK) → 共享 secret
2. InitAlice(sharedSecret)               InitBobWithSS(sharedSecret)
3. Send() → DR message    → stream →   Receive() → decrypt
                                        FinishRatchet() after decrypt
4. ... subsequent messages ...
```

---

## dr/ratchet.go — 核心 Double Ratchet

### RatchetState 结构

```go
type RatchetState struct {
    rootKey        [32]byte    // 根密钥，派生 chain key
    sendChainKey   [32]byte    // 发送链密钥
    recvChainKey   [32]byte    // 接收链密钥
    DHKeyPair                   // 当前节点的 DH 密钥对（每轮 ratchet 换新）
    remoteDHPK    [32]byte      // 对方最新的 DH 公钥（用于下一轮 ECDH）
    origRootKey   [32]byte      // 最初的 root key（用于 firstDH 特殊处理）
    firstDH       bool          // 是否是第一个 DH ratchet step
    sendMsgNum    int           // 已发送消息数
    recvMsgNum    int           // 已接收消息数
}
```

### Symmetric Ratchet（发消息）

每发一条消息，从 chain key 派生 message key，然后 chain key 更新：

```
chain_key
  → HKDF("DoubleRatchetMessage") → (message_key, next_chain_key)
  → 用 message_key 加密明文
  → 更新 chain_key = next_chain_key
```

关键：message key 用完即删，chain key 泄露也只能推出后续 chain key，无法反推历史 message key。

### DH Ratchet Step（收到对方新公钥时）

当收到对方的 DH 公钥时，需要做一次 DH ratchet step：

```
new_DH_KEY = generate_fresh_keypair()
shared = ECDH(new_DH_SK, remote_DH_PK)
root_key, chain_key = HKDF(shared, "DoubleRatchet")
```

同时更新 `remoteDHPK = 对方的新公钥`，这样下一轮 ECDH 用新的共享 secret。

### First DH Step 的特殊处理

第一个 DH step 之前没有"旧"的 ratchet 状态，直接用 ECDH 共享 secret 作为初始 root key。代码中 `firstDH` flag 和 `origRootKey` 记录这个初始状态，用于对称 ratchet。

### 序列化

未导出字段不能直接序列化。`SerializeRatchetState()` 和 `DeserializeRatchetState()` 手动处理：

```go
// serialize: 按顺序写入所有字段（全部是固定长度 [32]byte 或 int）
// deserialize: 按顺序读出，构造回结构体
```

---

## dr/session.go — DRSession 与 Stream 协议

### DRSession 结构

```go
type DRSession struct {
    isInitiator   bool
    ratchetState  *RatchetState
    peerX25519PK  []byte                 // 对方的 X25519 公钥
    sharedSecret  []byte                 // 初始 ECDH 共享密钥
}
```

Initiator 和 Responder 区别：
- **Initiator**：主动发起，先调用 `InitAlice(sharedSecret)`
- **Responder**：被动响应，收到第一条消息后才初始化 `InitBobWithSS(sharedSecret)`

### 消息格式（wire format）

每条 DR 消息是 length-prefixed 二进制：

```
[4字节: 长度][40字节: header][变长: ciphertext][16字节: tag]
```

Header 40 字节：
```
[32字节: 对方的 DH 公钥] [4字节: 消息编号] [4字节: 上一条消息的 PN (previous chain length)]
```

### SendMessage 流程

```go
func (s *DRSession) SendMessage(plaintext []byte) error
```

1. `s.ratchetState.MakeMessageKey()` → 派生 message key
2. `s.ratchetState.Encrypt(plaintext, message_key)` → ChaCha20-Poly1305
3. 序列化 header（含当前 DH 公钥、消息编号）
4. 写流：`<长度><header><密文><tag>`
5. 更新 `sendMsgNum`，删除用过的 message key

### Receive 流程

```go
func (s *DRSession) Receive() ([]byte, error)
```

1. 读流：`<长度><header><密文><tag>`
2. 反序列化 header，取出 `dh_pubkey`、`msg_num`
3. 如果 `dh_pubkey != s.ratchetState.remoteDHPK` → 做 DH ratchet step
4. `s.ratchetState.MakeMessageKey()` → 派生 message key
5. `s.ratchetState.Decrypt(ciphertext, tag, message_key)` → 解密
6. 更新状态

### 协议 ID

DR 消息通过 libp2p stream 传输，协议 ID：`/agent/dr/1.0.0`

### 与 session.Manager 的关系

`sessions/session.go` 的 `Manager` 是 ECIES 层，负责初始 ECDH 密钥交换。DR 在此基础上继续——初始共享 secret 作为 DR 的 seed，后续全部用 Double Ratchet。

---

## dr/store.go — SQLite 持久化

### 存储 schema

```sql
CREATE TABLE dr_sessions (
    peer_urn   TEXT PRIMARY KEY,   -- peer's URN
    state      BLOB NOT NULL,       -- 序列化的 RatchetState
    updated_at INTEGER NOT NULL     -- Unix 时间戳
);
```

WAL mode：写入不阻塞读。

### 接口

```go
type DRStore struct { db *sql.DB }

func (s *DRStore) SaveSession(peerURN string, state []byte) error  // upsert
func (s *DRStore) LoadSession(peerURN string) ([]byte, error)      // 返回序列化数据
func (s *DRStore) DeleteSession(peerURN string) error
```

### 调用时机

- **发消息前**：SaveSession（保存当前 ratchet 状态）
- **收到消息后**：SaveSession（更新 ratchet 状态）
- **节点重启**：LoadSession（恢复所有 peer 的 ratchet 状态）
- **删除会话**：DeleteSession

### 为什么每条消息都要存？

如果只发不存，重启后就丢失了 ratchet 状态——对方发来的下一条消息无法解密（chain key 不连续）。所以每条消息处理完后立即持久化。

---

## 安全属性

| 属性 | 实现方式 |
|------|---------|
| 前向保密 | message key 用完即删；旧 chain key 发送后删除 |
| 泄露隔离 | 一条消息的 key 泄露只影响一条 |
| DH ratchet | 定期换 DH 密钥对，阻止长时效的主动攻击 |
| 状态分离 | 发起方和接收方各自维护独立的 ratchet 状态 |