# Codex 独立出口：systemd 强隔离部署

本目录用于单机 Linux/systemd 部署。强隔离模式固定为一个专用 Codex-only CliRelay 实例：

- 仅承载 Codex OAuth，不加载 Claude、Gemini 或其他 Provider；
- 使用独立用户 `clirelay-codex`，不得与混合 Provider 主实例共用 UID；
- 使用独立配置、auth、`data/egress.db` 和日志目录；
- 只允许一个活跃副本；
- 只允许连接精确的远端 Tailnet endpoint `IP:port` 和固定 Headscale `IP:443`；
- 禁止创建 AF_UNIX socket，避免通过宿主机本地代理或守护进程 socket 绕过 nftables；
- 禁用动态插件，不加载 Home JWT 等非 Codex 子系统；
- 本机 endpoint、自动故障转移、用户电脑出口均不属于强隔离模式。

V1 将本机管理密钥和 root 配置权限视为可信运维边界，尚未在程序内实现通用 Provider allowlist。专用配置和 auth 目录必须只放 Codex；管理人员不得上传其他 Provider 凭据。nftables 仍会阻止宿主机直连，但误装其他 Provider 会破坏 Codex 出口独占语义。

Headscale 负责节点身份、标签和 Tailnet 连接。它不负责安装远端代理，也不是出口代理。远端服务器上的 Tailscale 客户端、SOCKS5/HTTP CONNECT 服务、认证和入站防火墙仍需手动或通过现有服务器管理系统安装。

## 文件

| 文件 | 用途 |
|---|---|
| `clirelay-codex.service` | 专用 systemd unit 和进程沙箱 |
| `clirelay-codex-firewall.service` | 在应用启动前加载专用 kill-switch |
| `environment.example` | Headscale API key 的 root-only EnvironmentFile 模板 |
| `config.codex-only.example.yaml` | Codex-only 准备实例配置 |
| `clirelay-codex.nft.in` | 故意不可直接加载的 nftables 模板 |
| `apply-nftables.sh` | 校验、备份并原子替换专用 nftables table |
| `rollback-nftables.sh` | 原子恢复 apply 前的专用 table |
| `verify-templates.sh` | 不修改系统的模板结构检查 |

## 1. 前置条件

目标主机需要：

- Linux、systemd、nftables、util-linux `flock`；
- 已运行的 `tailscaled`；
- Headscale 使用固定 IPv4 或 IPv6 地址，并提供有效 TLS 证书；
- 两个独立的 HTTPS IP echo 服务，响应为 `{"ip":"..."}` 或纯文本 IP；
- 至少一个远端服务器已经或即将加入 Tailnet；
- 每个远端 endpoint 有确定的 Tailnet IP、TCP 端口和预期公网 IP。

不要允许 `clirelay-codex` 用户访问网络 DNS。若 Headscale URL 使用域名，将固定 IP 写入 `/etc/hosts`，域名继续用于 HTTP Host 和 TLS SNI：

```bash
echo '203.0.113.20 headscale.internal.example' | sudo tee -a /etc/hosts
getent ahosts headscale.internal.example
```

IP 变化时必须先更新 `/etc/hosts` 和 nftables 精确集合；在两者不一致期间同步应失败关闭。

## 2. 创建专用用户和目录

以下路径不得与现有混合 Provider 实例共享：

```bash
sudo useradd \
  --system \
  --home-dir /var/lib/clirelay-codex \
  --create-home \
  --shell /usr/sbin/nologin \
  clirelay-codex

sudo install -d -m 0755 -o root -g root /opt/clirelay-codex/bin
sudo install -d -m 0755 -o root -g root /opt/clirelay-codex/share/headscale-egress
sudo install -d -m 0750 -o root -g clirelay-codex /etc/clirelay-codex
sudo install -d -m 0750 -o root -g clirelay-codex /var/lib/clirelay-codex/config
sudo install -d -m 0750 -o root -g clirelay-codex /var/lib/clirelay-codex/config/static
sudo install -d -m 0700 -o clirelay-codex -g clirelay-codex /var/lib/clirelay-codex/config/data
sudo install -d -m 0700 -o clirelay-codex -g clirelay-codex /var/lib/clirelay-codex/auths
sudo install -d -m 0700 -o clirelay-codex -g clirelay-codex /var/log/clirelay-codex
sudo install -d -m 0700 -o root -g root /var/lib/clirelay-codex/firewall-backups
```

`egress.db` 的固定路径是配置文件所在目录下的 `data/egress.db`，所以上述布局最终为：

```text
/var/lib/clirelay-codex/config/config.yaml
/var/lib/clirelay-codex/config/static/manage.html
/var/lib/clirelay-codex/config/data/egress.db
/var/lib/clirelay-codex/config/data/egress.db.lock
/var/lib/clirelay-codex/auths/
/var/log/clirelay-codex/
/etc/clirelay-codex/environment
```

## 3. 安装二进制、配置和 unit

```bash
sudo install -m 0755 ./cli-proxy-api /opt/clirelay-codex/bin/cli-proxy-api
sudo install -m 0640 -o root -g clirelay-codex \
  deploy/headscale-egress/config.codex-only.example.yaml \
  /var/lib/clirelay-codex/config/config.yaml
sudo install -m 0600 -o root -g root \
  deploy/headscale-egress/environment.example \
  /etc/clirelay-codex/environment
sudo cp -a panel/dist/. /var/lib/clirelay-codex/config/static/
sudo chown -R root:clirelay-codex /var/lib/clirelay-codex/config/static
sudo find /var/lib/clirelay-codex/config/static -type d -exec chmod 0750 {} +
sudo find /var/lib/clirelay-codex/config/static -type f -exec chmod 0640 {} +
sudo cp -a deploy/headscale-egress/. /opt/clirelay-codex/share/headscale-egress/
sudo install -m 0644 deploy/headscale-egress/clirelay-codex.service \
  /etc/systemd/system/clirelay-codex.service
sudo install -m 0644 deploy/headscale-egress/clirelay-codex-firewall.service \
  /etc/systemd/system/clirelay-codex-firewall.service
```

编辑两个模板并替换全部 `__REPLACE_*__`：

```bash
sudoedit /var/lib/clirelay-codex/config/config.yaml
sudoedit /etc/clirelay-codex/environment
sudo grep -R '__REPLACE_' \
  /var/lib/clirelay-codex/config/config.yaml \
  /etc/clirelay-codex/environment
```

最后一条命令必须零输出。再次固定权限：

```bash
sudo chown root:clirelay-codex /var/lib/clirelay-codex/config/config.yaml
sudo chmod 0640 /var/lib/clirelay-codex/config/config.yaml
sudo chown root:root /etc/clirelay-codex/environment
sudo chmod 0600 /etc/clirelay-codex/environment
sudo systemd-analyze verify \
  /etc/systemd/system/clirelay-codex-firewall.service \
  /etc/systemd/system/clirelay-codex.service
sudo systemctl daemon-reload
```

配置初始保持 `egress-network.enabled: false`。这称为“准备模式”：管理端可以同步节点、配置 endpoint 和 binding，但任何 Codex OAuth 外连都会失败关闭。

`probe-urls` 属于 root-owned 配置，面板不能修改。两个探针都必须通过被检测 endpoint 访问；首个探针不可用时会尝试下一个，不能把探针域名/IP加入宿主机 nftables allowlist。生产环境应优先使用自有主探针和独立备用探针，避免单一第三方服务控制全部 endpoint 的健康状态。

强隔离规则不允许实例访问 GitHub，因此管理面板必须在部署前构建并复制到 `config/static/`，同时保持 `disable-auto-update-panel: true`。不要为了面板更新而在 nftables 中放开任意 HTTPS；升级时由管理员随版本替换静态目录。

## 4. 节点接入和远端代理

“添加节点”只生成一次性 Headscale 入网命令，其能力边界是节点接入，不包含远程安装。首次准备可采用以下任一方式：

1. 由 Headscale 管理员先创建 pre-auth key 并让远端节点入网；或
2. 先只在 nftables 中允许固定 Headscale `IP:443`，启动准备实例，通过 SSH tunnel 打开面板生成节点入网命令；节点取得 Tailnet IP 后，再把精确 endpoint `IP:port` 加入 nftables。

远端节点必须手动完成：

- 安装和运行 `tailscaled`；
- 加入 Headscale，并获得 `tag:clirelay-egress`；
- 安装 SOCKS5 或 HTTP CONNECT；
- 只监听具体 Tailnet IP，不监听 `0.0.0.0`、公网地址或 loopback；
- 只允许 CliRelay origin 的 Tailnet IP 访问代理端口；
- 配置认证，并从该节点确认预期公网 IP。

强隔离模式不得创建 `local_server=true` endpoint，也不得将 `127.0.0.1`、`::1` 或宿主机公网地址加入出口集合。

## 5. 渲染并原子加载 nftables

模板中的占位符故意使其无法通过语法检查，防止误加载宽泛规则。创建渲染副本：

```bash
sudo install -d -m 0700 -o root -g root /etc/nftables.d
sudo cp deploy/headscale-egress/clirelay-codex.nft.in \
  /etc/nftables.d/clirelay-codex.nft
sudoedit /etc/nftables.d/clirelay-codex.nft
sudo chown root:root /etc/nftables.d/clirelay-codex.nft
sudo chmod 0600 /etc/nftables.d/clirelay-codex.nft
```

数字 UID 可用以下命令取得：

```bash
id -u clirelay-codex
```

IPv4 endpoint 集合示例仅展示语法：

```nft
elements = { 100.64.0.21 . 1080, 100.64.0.22 . 18080 }
```

Headscale 集合单独固定端口：

```nft
elements = { 203.0.113.20 . 443 }
```

IPv6 endpoint 集合示例：

```nft
elements = { fd7a:115c:a1e0::21 . 1080 }
```

集合只能包含：

- 已登记远端 endpoint 的精确 Tailnet `IP:port`；
- 固定 Headscale `IP:443`。

若某个地址族完全不用，删除该 set 中整行 `elements = { __REPLACE_...__ }`，保留空 set。禁止加入整个 `100.64.0.0/10`、loopback、DNS、任意 `:443` 或 OpenAI/ChatGPT 目标地址。

检查占位符和语法：

```bash
sudo grep -n '__REPLACE_' /etc/nftables.d/clirelay-codex.nft
sudo nft -c -f /etc/nftables.d/clirelay-codex.nft
```

第一条必须零输出，第二条必须成功。加载脚本先备份现有专用 table，再把删除旧 table 与创建新 table 放入同一个 nftables transaction：

```bash
sudo deploy/headscale-egress/apply-nftables.sh \
  /etc/nftables.d/clirelay-codex.nft
```

记录命令输出的 rollback backup directory。需要回滚时：

```bash
sudo deploy/headscale-egress/rollback-nftables.sh \
  /var/lib/clirelay-codex/firewall-backups/clirelay_codex.YYYYMMDDTHHMMSSZ.XXXXXX
```

脚本会先运行 `nft -c`。真正加载由单个 `nft -f` transaction 完成；语法或内核校验失败时不会留下半套规则。不要通过 `nft flush ruleset` 安装或回滚。

正式部署由 `clirelay-codex-firewall.service` 在应用 unit 之前加载同一渲染文件。kill-switch unit 失败时，应用 unit 因 `Requires=` 不能启动，避免重启后出现无宿主机隔离窗口：

```bash
sudo systemctl enable clirelay-codex-firewall.service
sudo systemctl start clirelay-codex-firewall.service
sudo systemctl reload clirelay-codex-firewall.service  # 地址端口变更后
sudo systemctl restart clirelay-codex.service          # 关闭旧规则下已建立的连接
```

## 6. 启动准备实例

服务只监听 `127.0.0.1:18317`，通过 SSH tunnel 管理：

```bash
sudo systemctl enable --now clirelay-codex.service
ssh -L 18317:127.0.0.1:18317 root@CLIRELAY_HOST
```

然后打开 `http://127.0.0.1:18317/management.html`：

1. 同步 Headscale 节点；
2. 创建远端 endpoint，填写 `expected_public_ip`；
3. 检测每个 endpoint；
4. 以 `exclusive` 策略为每个 Codex stable identity 分配唯一 endpoint；
5. 确认无未绑定身份、重复公网 IP、离线节点、过期节点或过期健康检查。

准备阶段不要向此端口发送生产 Codex 流量。

## 7. 单副本规则

该部署不支持多个副本共享同一份 `egress.db`：

- systemd unit 使用 `/run/clirelay-codex/instance.lock` 防止同主机重复启动；
- CliRelay 使用 `data/egress.db.lock` 取得数据库单写锁；
- 第二实例无法取得锁时必须启动失败，不能删除 lock 文件绕过；
- 不要把配置、auth 或 egress 数据放到 NFS/共享盘供多副本同时使用。

需要迁移主机时，停止旧实例、复制一致性快照，再启动新实例。任意时刻只能有一个活跃写入者。

## 8. 运行验收

读取概览，不把管理密钥写入 shell history：

```bash
read -rsp 'Management key: ' MANAGEMENT_KEY; echo
curl --fail --silent --show-error \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  http://127.0.0.1:18317/v0/management/egress/overview
unset MANAGEMENT_KEY
```

查看 unit、安全属性、连接和 nftables counter：

```bash
sudo systemctl status clirelay-codex.service
sudo systemctl show clirelay-codex.service \
  -p User -p MainPID -p NoNewPrivileges -p ProtectSystem -p ProtectHome
sudo ss -ntp | grep cli-proxy-api
sudo nft list table inet clirelay_codex
sudo journalctl -u clirelay-codex.service --since '-10 minutes'
```

在发起 endpoint 检测和 Codex 请求时抓取连接：

```bash
sudo tcpdump -ni any \
  '(host ENDPOINT_TAILNET_IP and tcp port ENDPOINT_PORT) or (host HEADSCALE_FIXED_IP and tcp port 443)'
```

验收至少包括：

1. 两个 OAuth 的成功请求分别呈现各自登记的 `expected_public_ip`；
2. 停止一个远端代理后，对应 OAuth 稳定返回 egress 错误，不切换到其他 endpoint；
3. endpoint 停止期间，`ss`/`tcpdump` 中没有 CliRelay 直连 OpenAI/ChatGPT；
4. 向未允许目标发起新连接时，nftables reject counter 增长；
5. 节点状态或 endpoint 健康状态超过 TTL 后，新连接失败；
6. 第二个相同 unit/二进制无法取得实例锁或 `egress.db.lock`。

## 9. 浏览器 OAuth 边界

该方案隔离的是服务端 OAuth code exchange、token refresh 和 Codex API 请求。管理员浏览器打开 `auth.openai.com` 时，授权页面仍使用管理员工作站的网络出口。

如果产品要求“浏览器授权页面也必须使用与账号相同的公网 IP”，必须在对应出口侧提供隔离浏览器/远程浏览器；这不属于当前 V1，不能把当前实现描述为覆盖完整浏览器登录链路。
