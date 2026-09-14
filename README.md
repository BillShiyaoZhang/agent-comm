# Agent Comm

Agent Comm 提供本地身份、签名加密、持久消息、Go SDK/helper，以及可适配不同宿主的 Python 协作 runtime。

## 组件与入口

| 组件 | 路径 | 职责 |
|---|---|---|
| Go SDK/helper | `cmd/helper/`、`agent/` 等 | 身份、加密、Registry/MQ、持久 inbox/outbox |
| 通用 Python runtime | [python/](python/README.md) | 联系人、委托、审批、受控动作、四类适配端口、远程配对/RPC |
| Hermes 插件 | [connectors/hermes-platform/](connectors/hermes-platform/README.md) | 原生 Host/Interaction 适配、Gateway 会话、收件生命周期 |
| OpenClaw 基础 connector | [connectors/openclaw-channel/](connectors/openclaw-channel/README.md) | 既有宿主基础收发；不默认宣称支持新协作 runtime |

## Hermes 接入

使用当前工作区配套 runtime 与 connector，不能只安装旧插件。完整合同见 [HERMES_INTEGRATION](docs/HERMES_INTEGRATION.md)，新扩展接口和远程配对见 [runtime README](python/README.md)。使用实际 Hermes Gateway/桌面后端的 Python 环境：

```sh
go build -o agent-comm-helper ./cmd/helper
./agent-comm-helper init /absolute/path/to/agent/keys
./agent-comm-helper daemon /absolute/path/to/agent/keys https://YOUR_PLATFORM 45042
python -m pip install ./python ./connectors/hermes-platform
python -c "from hermes_constants import get_hermes_home; print(get_hermes_home())"
```

helper 常驻运行；云 URL 传给 daemon，插件 `platform_url` 填 `http://127.0.0.1:45042`。每个身份使用独立密钥/数据目录和本机端口。保留已有密钥、mailbox 与插件数据库；通过 `/info` 检查身份，再确认真实 SSE connected 状态。

## 本机 helper 数据流

```text
Hermes / 其它受支持客户端
  ↕ 本机 HTTP、SSE、消费 ACK
helper：密钥、签名与加解密、SQLite inbox/outbox
  ↕ HTTPS Registry / MQ
agent-comm-platform：身份目录、持久密文信箱
```

- `POST /api/v1/mq/store` 返回本机持久接受及稳定 message_id。后台投递同一签名密文，成功后 helper 标记 `platform_queued`。这不代表收件人已执行任务。
- 入站先验证并落盘，再 ACK 云 MQ。SSE 重连及 retrieve 重放未消费消息；消费者成功持久处理后 ACK 本机 helper。
- 可靠 helper 出站使用 MQ。传统 Go SDK `SendMessage` 另有 P2P/Double Ratchet 能力；不能将它画成 durable helper 的默认先行路径。HTTPS MQ 使用静态 X25519、AES-GCM 与 Ed25519，不承诺该路径具有前向安全。
- 本机 API 传明文并具有身份操作权限，仅绑定 loopback，拒绝跨源网页访问。Web 控制台需通过 agent 侧显式配对的 RPC，不直接改本机信任。

## Registry 与消息认证

注册必须由 URN 对应的 Ed25519 所有者签名，PeerID 由同一公钥派生，X25519 公钥为 32 字节。签名覆盖 `registry.BuildSignedMsg`；首次注册和更新都校验，旧无签名入口不可用。HTTP client 自动生成签名；写入时间戳接受最近 5 分钟与最多 1 分钟未来偏差。

信封签名绑定发送者、收件人、稳定消息 ID 及全部加密字段。云 MQ retrieve/ACK 要求目标身份认证；相同 ID 的不同内容被拒绝。组件还需自己的操作幂等与持久回执，不能用密码学认证代替业务授权。

## 本地协作与扩展

通用 runtime 支持 HostPort、MemoryPort、InteractionPort、TransportPort，提供版本化能力注册、真实 dispatcher 与可运行参考适配器。默认不装入用户记忆；资料快照必须明确选取，登记不等于允许对外披露。

Hermes 的个人协作和远程工作台分别通过 `collaboration_enabled`、`remote_enabled` 显式启用。远程配对按控制台 URN、方法范围和期限约束；远程消息不能伪装成原生审批。配置、CLI、扩展示例与验收见 [python/README.md](python/README.md)。

## 验证

```sh
go test ./mq ./crypto ./session
python -m unittest discover -s python/tests -q
python -m unittest discover -s connectors/hermes-platform/tests -q
```

Python 测试需要将本地 `python/` 放入 PYTHONPATH 或先安装 runtime；Hermes 原生集成测试需要该宿主的实际依赖。真实本地 helper/platform 测试入口为 `tools/test_helper_platform.py`。不存在仅凭模拟或消息入队就证明远端业务完成的检查。
