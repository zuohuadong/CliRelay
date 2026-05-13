# Security Baseline Checks

更新时间：2026-04-13

每次发布前至少检查以下项目：

## 后端

- `gin.Engine` 已显式配置 Trusted Proxies，不依赖 Gin 默认行为。
- 默认 CORS 不是 `*`，仅允许 same-origin 或显式 allowlist。
- 主 HTTP server 设置了基础 timeout 与 `MaxHeaderBytes`。
- pprof 默认绑定 loopback，非本地暴露需要显式允许。
- public lookup 响应带 `Cache-Control: no-store, private`。
- public lookup 不通过 URL query 传递真实 API key。
- public lookup 存在基础速率限制。
- auth 文件与 Vertex 凭据的 multipart 上传存在服务端大小限制。

## 前端

- management key 持久化存在显式过期时间。
- 高敏值不写入 URL query/hash。
- `bun run lint` 为 0 warning / 0 error。
- 运行期网络层只存在 `src/lib/http/*` 主线实现。
- 高敏下载不通过可分享 URL 暴露原文。
