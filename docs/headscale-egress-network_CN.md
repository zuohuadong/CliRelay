# Headscale 出口网络：产品边界、强隔离部署与手动迁移

本文定义 CliRelay Codex OAuth 出口网络的 V1 产品边界和生产迁移顺序。V1 不兼容旧 `proxy-pool` / `proxy_id`，只支持手动迁移。

可直接执行的 systemd、nftables、原子应用/回滚和结构检查资产位于 [`deploy/headscale-egress/`](../deploy/headscale-egress/README.md)。

## 产品模型

V1 只管理三类对象：

1. **Node**：带 `tag:clirelay-egress` 的 Headscale 节点投影；
2. **Endpoint**：节点上真实运行的 SOCKS5 或 HTTP CONNECT `IP:port`；
3. **Binding**：Codex stable identity 到 endpoint 的直接绑定。

```text
Headscale control plane
        │ node enrollment / status sync
        │
CliRelay Codex-only origin ── Tailnet ── Remote endpoint ── Public IP
        │                                      ▲
        └──── stable identity binding ─────────┘
```

边界如下：

- Headscale 是控制面，不是代理服务，也不安装远端代理；
- “添加节点”是节点接入：生成一次性 pre-auth key 和入网命令；
- 远端 `tailscaled`、SOCKS5/CONNECT、认证和防火墙仍需手动或由既有服务器管理系统安装；
- 不使用原生 Exit Node，因为它切换整台 origin 的默认路由，无法按 OAuth 隔离；
- 不存在代理池、权重、同一 OAuth 的 endpoint 自动故障转移或健康节点自动接管；
- 用户电脑、Mac 和临时客户端不属于该产品模型；
- 强隔离模式禁止本机 endpoint。

## 强隔离运行形态

生产强隔离不是在混合 Provider 主进程旁边增加一条代理配置，而是运行一个专用实例：

```text
Mixed-provider CliRelay          Codex-only CliRelay
main UID                         clirelay-codex UID
main config/auth/data            dedicated config/auth/egress.db
normal provider networking       nftables exact addr.port allowlist
                                 exactly one active replica
```

必须满足：

- Codex-only 实例的配置和 auth 目录只放 Codex，动态插件保持关闭；
- 专用 UID `clirelay-codex` 不与主实例共享；
- 配置、auth、`data/egress.db`、日志不与主实例共享；
- systemd unit 固定使用显式 `-config`；
- 管理面板作为只读静态资产随版本预装，不允许实例直连 GitHub 自动下载；
- `egress.db.lock` 只能由一个进程取得；
- nftables 只允许该 UID 新建到精确 endpoint `IP:port` 和固定 Headscale `IP:443` 的 TCP 连接；
- 应用 unit 必须 `Requires=` kill-switch unit，防火墙加载失败时应用不能启动；
- 不能允许整个 `100.64.0.0/10`、loopback、DNS、任意 `:443` 或宿主机直连公网。
- 应用进程不允许创建 AF_UNIX socket，避免通过宿主机本地代理或守护进程 socket 绕过 nftables。

宿主机 root、内核或 nftables 管理权限失陷不在此隔离边界内。

V1 把本机管理密钥和 root 配置权限视为可信运维边界，程序内尚未提供通用 Provider allowlist。管理员不得向专用 auth 目录上传非 Codex 凭据，也不得在专用配置中启用其他 Provider；否则即使仍不会回退到宿主机公网，也会破坏“一个 Codex 身份独占一个出口”的产品语义。

## Binding 和公网 IP 策略

生产默认配置：

```yaml
egress-network:
  binding-policy: "exclusive"
  endpoint-check-interval: "2m"
  endpoint-health-ttl: "5m"
  probe-urls:
    - "https://ip-echo-primary.example/ip"
    - "https://ip-echo-secondary.example/ip"
  headscale:
    sync-interval: "1m"
    node-freshness-ttl: "3m"
```

`exclusive` 表示一个 endpoint 和其观测公网 IP 只能服务一个 stable identity。创建 endpoint 时应登记 `expected_public_ip`；检测到公网 IP 不一致、重复或未知时，不得进入切换就绪状态。

`probe-urls` 只允许 root 配置的 HTTPS IP echo 地址。所有探针请求仍通过被检测 endpoint；网络错误、非 2xx 或无效 IP 会继续尝试下一个探针，全部失败才标记 unhealthy，所有合法响应均不匹配时标记 `ip_mismatch`。生产至少配置两个相互独立的探针，避免单点故障。

同一 OAuth 不会因为节点离线、检查失败或网络波动自动切换。失败时返回稳定 egress 错误，恢复后继续使用原 binding。

## 节点接入

管理面板 `出口网络 -> Nodes -> 添加节点` 调用 Headscale 创建一次性、带服务标签、默认一小时有效的 pre-auth key，并展示入网命令。明文 key 只在本次响应中显示。

建议服务标签：

```text
tag:clirelay-egress
```

Headscale policy 至少要做到：

1. CliRelay origin 只能访问远端节点的代理端口；
2. 无关 Tailnet 节点不能访问代理端口；
3. 出口节点之间不能横向访问；
4. 只有指定管理身份可注册和同步带服务标签的节点。

远端代理必须：

- 只监听具体 Tailnet IP；
- 使用独立 TCP 端口和认证；
- SOCKS 使用远端 DNS 解析语义；
- 入站防火墙只允许 origin 的 Tailnet IP；
- 能独立验证当前公网 IP。

## 双层 fail-closed

### 应用层

Codex OAuth code exchange、token refresh、HTTP、WebSocket、compact、quota/probe 和图像请求都必须先解析 binding。以下任一状态直接失败：

- stable identity 未绑定；
- endpoint 不存在、被禁用或协议非法；
- endpoint 健康状态过期或异常；
- Headscale 节点离线、标签不匹配或同步状态过期；
- 当前公网 IP与 `expected_public_ip` 不一致；
- `egress-network.enabled` 关闭。

失败时不得使用全局 `proxy-url`、环境代理、宿主机直连、其他 endpoint 或旧 WebSocket 连接。

### 宿主机层

应用逻辑之外，专用 UID 由 nftables exact `addr.port` set 约束。已建立/关联连接可以继续传输；其他新建 IPv4/IPv6 连接全部拒绝。

仓库模板故意包含不可加载的 `__REPLACE_*__`，必须填入真实数值 UID 和精确地址端口后先运行：

```bash
sudo nft -c -f /etc/nftables.d/clirelay-codex.nft
```

然后使用仓库脚本原子备份和加载。不要在未保留 SSH 管理通道时修改整机防火墙，也不要使用 `nft flush ruleset`。

地址或端口变更后，加载新规则并重启 Codex-only 实例，以关闭旧规则下仍处于 established 状态的连接；不要只 reload 防火墙后长期保留旧连接。

## Headscale 密钥和 DNS

Headscale API key 只从 root-only systemd EnvironmentFile 注入，YAML 只记录变量名：

```yaml
headscale:
  url: "https://headscale.internal.example"
  api-key-env: "CLIRELAY_HEADSCALE_API_KEY"
```

强隔离 UID 不允许网络 DNS。使用 Headscale TLS 域名时，把固定 IP 写入宿主机 `/etc/hosts`，并只在 nftables 中允许该固定 `IP:443`。IP 变化时，管理员需要显式更新 hosts 和规则；在完成前系统失败关闭。

## 浏览器 OAuth 范围

当前方案隔离：

- 服务端授权码交换；
- token refresh；
- 所有 Codex API 请求。

管理员浏览器打开 `auth.openai.com` 时，页面仍使用管理员工作站的出口。这不是“用户电脑作为出口节点”，也不属于当前 V1 的服务端隔离范围。

如果业务要求授权页面本身也必须呈现与账号相同的公网 IP，需要在对应出口侧运行隔离浏览器/远程浏览器，不能把当前 V1 描述为覆盖完整浏览器授权链路。

## 手动迁移：推荐的并行准备实例

推荐使用新二进制的独立准备实例，监听 `127.0.0.1:18317`。准备实例与旧实例使用不同 UID、端口、配置、auth 和数据库，不接收生产流量。

1. 冻结旧配置写入，记录 Codex OAuth 文件和旧出口对应关系。
2. 备份旧二进制、旧配置、旧 auth 和旧数据目录。
3. 创建 `clirelay-codex` 用户以及独立 config/auth/data/log 目录。
4. 复制 Codex auth 文件到新的专用 auth 目录；不要让新旧实例写同一个目录。
5. 安装新二进制和专用 systemd unit，配置保持 `egress-network.enabled: false`。
6. 固定 Headscale 地址，先渲染只允许 Headscale `IP:443` 的 nftables 集合并原子加载。
7. 启动准备实例，通过 SSH tunnel 访问本地管理面板。
8. 生成节点接入命令；在远端服务器手动安装/启动代理并取得精确 Tailnet `IP:port`。
9. 将 endpoint 精确 `IP:port` 加入 nftables 集合，语法检查后原子更新。
10. 同步节点，创建 endpoint，登记 `expected_public_ip` 并完成检测。
11. 为每个 `codex:<sha256(account_id)>` 写入唯一 binding；文件名不是身份主键。
12. 确认没有未绑定身份、缺少 `account_id`、重复公网 IP、离线/过期节点或异常/过期 endpoint。
13. 停止准备实例，对新配置、新 auth 和新 `data/egress.db` 创建一致性快照。
14. 进入维护窗口，停止旧实例，确认端口和进程完全退出。
15. 把新实例端口改为正式端口并设置 `egress-network.enabled: true`。
16. 再次确认 nftables 精确集合和 Headscale 固定 IP，启动 Codex-only 正式实例。
17. 验证 code exchange、refresh、HTTP、WebSocket、compact、quota/probe 和图像请求均使用绑定公网 IP。
18. 停止一个 endpoint，验证关联 OAuth 失败且无切换、无宿主机直连。

同一时间只允许一个新实例取得 `egress.db.lock`。不要通过删除 lock 文件运行第二副本。

## 手动迁移：停机准备窗口

如果不能并行运行准备端口：

1. 完成旧实例全量备份；
2. 停止旧实例；
3. 启动 `enabled: false` 的新实例；
4. 在明确停机窗口内完成节点、endpoint、检测和 binding；
5. 停止新实例并备份 config/auth/`egress.db`；
6. 设置 `enabled: true` 后重新启动并执行全部验收。

`enabled: false` 期间 Codex 外连始终被阻断，不会恢复旧直连。

## 备份和回滚

一致性备份必须把以下内容作为一个整体：

- 专用二进制版本；
- `/var/lib/clirelay-codex/config/config.yaml`；
- `/var/lib/clirelay-codex/auths/`；
- `/var/lib/clirelay-codex/config/data/egress.db`；
- 渲染后的 nftables 文件及 apply 脚本返回的 rollback backup directory。

最稳妥的备份方式是在服务停止后复制。不要只复制运行中的 SQLite 文件。

回滚到旧版本：

1. 停止 Codex-only 新实例；
2. 使用 `rollback-nftables.sh` 恢复 apply 前的专用 table；
3. 恢复旧二进制、旧配置、旧 auth 和旧数据目录的完整快照；
4. 启动旧实例并验证；
5. 远端节点和代理可以保留，但应继续拒绝无关来源。

回滚后准备再次启用新版本时，也必须同时恢复同一时点的新 config/auth/`egress.db`，避免 token 与 binding 不一致。

## 验收清单

- Codex-only 实例使用独立 UID、目录和正式端口，混合 Provider 主实例不使用该 UID；
- systemd 启用 `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome=true`、空 CapabilityBoundingSet；
- nftables 只有精确 endpoint `IP:port` 和 Headscale `IP:443`，同时覆盖 IPv4/IPv6 set；
- 两个 OAuth 同时请求时呈现各自登记的不同公网 IP；
- exclusive 策略拒绝 endpoint 或公网 IP 复用；
- endpoint/节点异常或状态过期时请求失败，不自动切换；
- OAuth 未选择 endpoint 时不能开始；
- Headscale API key 不出现在配置、数据库、日志、管理 API 或面板响应中；
- 本机 endpoint 不可创建、不可绑定、不可回退；
- 第二实例无法取得 `egress.db.lock`；
- 浏览器 OAuth 的工作站网络边界已在产品说明和运维交接中明确。
