# 当前信任验证边界

## 身份记录

URN 的指纹来自 Ed25519 公钥。Registry 注册和续期通过 `ValidateRegistration` 验证身份公钥、X25519 公钥、PeerID、记录签名和写入时间窗口。签名消息由 [BuildSignedMsg](../../registry/client.go) 构造，覆盖 URN、PeerID、X25519 公钥、`stores_user_data` 和时间戳。

SDK 解析通过 `VerifyResolveResult` 验证返回的所有权证明；网络地址不在该记录签名覆盖范围内，连接仍必须验证 libp2p 对端身份。记录所有权证明与地址可达性、当前在线状态及业务授权是不同的事实。

更新后的单 Platform v2 helper 可以准确 URN 为输入自动解析并缓存已验证公钥；已有手工固定的不同公钥不得被静默替换。陌生 URN 的握手和好友申请可证明申请者持有该 URN 的密钥，不代表现实人物身份已经核实，也不建立通讯录关系。另一方接受申请后才是 `connected`；Python Runtime、Hermes 协作工具和受管 Web 以此限制普通发件。Go helper 的低层 v2 API 不查询好友状态，接收方 Runtime 会隔离未连接的业务消息。接受不自动提高 `trusted` 或授予协作、工具、工作台和合规披露权限。跨 Platform 的信任与路由不在此流程内。

## 消息信封

[VerifyEnvelope](../../crypto/envelope.go) 校验发送者身份、目标收件人和签名；签名覆盖所有加密字段与稳定消息 ID。HTTP MQ 与 libp2p MQ 还验证实际调用方。收件人权限约束 Retrieve 和 ACK，其他有效身份不能据此读取或确认别人的信箱。

这能约束当前组件可接受的身份与消息，不能证明任意运行环境、远程平台或宿主没有其他数据访问途径。Web 为提供远程工作台会处理用户授权的返回内容，其数据边界应阅读 Web 的技术说明。

## 联系与控制

联系人、信任声明、可联系地址不等于主人授权。Python runtime 的配对与方法权限、Hermes 的原生确认流程另外决定能读取和执行什么，见 [runtime](../../python/README.md) 与 [Hermes connector](../../connectors/hermes-platform/README.md)。

旧稿中的自动联邦审计和网关公钥替换判定没有相应完整实现，不能作为已支持能力。现役验证依据为 Registry、MQ、session 和 runtime 的测试。
