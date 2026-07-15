# 视频生成

CliRelay 提供统一的、与供应商无关的公开视频 API，同时把 xAI 原生协议保留在明确的子路由上。

## 公共 API

直接使用标准 `/v1` Base URL，并始终明确传入视频模型：

```bash
curl -X POST "https://clirelay.example.com/v1/videos" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "agnes-video-v2.0",
    "prompt": "雨夜霓虹街道中的电影感追踪镜头",
    "seconds": 8,
    "size": "1280x720"
  }'
```

返回的 `id` 是 CliRelay 公共任务 ID。使用同一套 API 查询和下载：

```bash
curl "https://clirelay.example.com/v1/videos/$VIDEO_ID" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY"

curl -L "https://clirelay.example.com/v1/videos/$VIDEO_ID/content" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY" \
  --output video.mp4
```

`/openai/v1/videos` 继续作为旧集成的兼容别名。xAI 原生请求保留在：

- `POST /v1/videos/generations`
- `GET /v1/videos/generations/{request_id}`
- `POST /v1/videos/edits`
- `POST /v1/videos/extensions`

请始终传入 `model`，例如 `agnes-video-v2.0` 或 `grok-imagine-video`，具体取决于已配置的供应商。

multipart 二进制 `input_reference` 上传会被明确拒绝，不再静默丢失。当前请先把图片上传到可访问的存储，再传入 `input_reference.image_url`。

## 持久化任务路由

公共任务路由保存在 `data/video.db`，记录公共任务 ID、上游任务 ID、供应商、凭据、模型、状态及存储结果。共同提供视频 API 的多个 CliRelay 实例必须共享该数据卷。

## S3 兼容结果存储

在 `config.yaml` 配置 `video-storage`，凭据通过指定环境变量注入。轮询到任务完成时，CliRelay 会把视频复制到对象存储；状态响应返回短期签名 `video_url`，`/content` 则重定向到新生成的签名地址。

对象存储端点必须使用 HTTPS；只有本机回环地址允许 HTTP，便于本地开发。`max-source-bytes` 控制单个上游视频的最大归档大小，默认 2 GiB。归档下载会拒绝私网、回环、链路本地及云元数据地址，并对重定向执行同样校验。

CliRelay 不会主动删除已归档的视频对象。请在 R2/S3 bucket 为 `video-storage.prefix`（默认 `videos/`）配置 Lifecycle 规则以执行保留期；短期视频应使用 R2 Standard。Lifecycle 删除是异步的，实际删除时间应预留约一天的缓冲。

未启用对象存储或复制失败时，CliRelay 会安全降级到需要 API Key 的鉴权代理下载。

## MCP 与 ChatGPT App

Agent 客户端可以把 `https://clirelay.example.com/mcp/video` 配置为 Streamable HTTP MCP，并使用 `Authorization: Bearer <CliRelay API key>`。MCP 工具与公共 API 共用任务服务和 SQLite 数据。

工具结果同时返回文本和 `structuredContent`，可作为 ChatGPT Apps SDK 组件的数据接口。要让普通 ChatGPT 用户发现和使用，仍需实现 Apps SDK UI/plugin 并走发布审核；原始 MCP 地址不会自动公开展示。
