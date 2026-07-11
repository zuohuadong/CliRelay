# Codex OAuth 固定 SOCKS 出口

CliRelay 为每个 Codex OAuth 账号保存一条稳定绑定：

```text
codex:sha256(account_id) -> endpoint -> auth.ProxyURL
```

运行时先按 `account_id` 解析绑定，再克隆当前 auth 并覆盖 `ProxyURL`。OAuth code exchange、token refresh、Responses HTTP/stream、compact、images、quota probe 和 WebSocket 都使用同一个严格代理传输。绑定缺失、端点禁用、健康状态过期、出口 IP 不匹配或代理不可用时，当前账号立即 fail-closed，不会改用全局代理、环境代理、宿主机直连或另一个端点。请求尚未产生响应内容时，调度器可跳过该故障账号，继续尝试另一个拥有独立健康绑定的 Codex 账号；所有候选账号均不可用时，返回最后一个出口错误。

当前 CLI 登录、device OAuth 和 `fetch_codex_models` 不接入该控制面，仍明确禁用。请从管理面板启动 Codex OAuth。

## 推荐网络结构

WireGuard 只负责把 CliRelay 主机连接到自有出口服务器的私网，不进入 CliRelay 的产品模型。每台出口服务器运行一个仅监听 WireGuard 地址的 SOCKS5 服务：

```text
CliRelay / wg0: 10.77.0.1
  OAuth A -> socks5://10.77.0.2:1080 -> 公网出口 198.51.100.2
  OAuth B -> socks5://10.77.0.3:1080 -> 公网出口 198.51.100.3
```

面板中分别创建两个端点：

| 名称 | Proxy URL | 预期公网 IP |
| --- | --- | --- |
| Codex A | `socks5://10.77.0.2:1080` | `198.51.100.2` |
| Codex B | `socks5://10.77.0.3:1080` | `198.51.100.3` |

先执行端点健康检查，确认观测公网 IP 与预期值一致，再将每个 OAuth 账号固定绑定到对应端点。绑定采用排他 1:1 规则，不提供权重、代理池或同一账号的端点自动故障转移；账号级请求回退不会改变这条绑定。

基础配置只包含健康检查参数：

```yaml
egress-network:
  enabled: true
  endpoint-check-interval: "2m"
  endpoint-health-ttl: "5m"
  probe-urls:
    - "https://api.ipify.org?format=json"
    - "https://ifconfig.co/json"
```

## 从旧 Headscale 方案手工迁移

新 schema 不兼容旧的 Headscale `egress.db`，不会自动推断或迁移节点、local endpoint 或历史状态。

1. 停止 CliRelay，避免数据库仍被持有或写入。
2. 备份配置、auth 文件和原有 `data/egress.db*`。
3. 删除运行目录中的旧 `data/egress.db*`，包括数据库、WAL/SHM 和 lock 文件。
4. 从配置中删除 `headscale`、`local-endpoint-enabled`、`binding-policy` 等旧字段，仅保留上面的简单 `egress-network` 配置。
5. 启动 CliRelay，在面板中手工重建 Endpoint，逐个检查实际出口 IP。
6. 从 Auth Files 或账号绑定页为每个 Codex OAuth 重新选择固定出口并确认保存。
7. 用实际 Codex 请求复核账号、端点和公网出口的一致性。

## 隔离边界

应用层 fail-closed 能保证 CliRelay 的受支持 Codex 路径不会在代理失败时静默直连，也不会为同一 OAuth 账号换用其他端点。调度器只会在首个响应内容产生前跳过整个故障账号，并让下一个 Codex 账号使用其自身独立绑定的健康端点。刷新 token 返回不同的非空 `account_id` 时，会在写入新 token 和 metadata 前失败，避免沿用旧绑定。

这不等于内核级强隔离。若 Codex 与其他 provider 运行在同一个进程和同一个系统用户下，按 UID 设置的统一 kill-switch 无法只限制 Codex 而不影响其他 provider；进程漏洞或未纳入控制面的新代码路径也超出应用层保证。

因此本项目不提供容易造成误解的通用 UID nftables 模板。需要内核级保证时，应把 Codex workload 放入独立进程、容器或 network namespace，再针对该隔离单元限制只能连接 WireGuard peer 的 SOCKS `IP:port`。网络规则必须按实际部署验证，并由运维侧维护回滚方案。
