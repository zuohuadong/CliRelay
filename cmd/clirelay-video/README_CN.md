# Codex 中使用 CliRelay 视频生成

`clirelay-tools` 是给 Codex 和脚本用户使用的能力工具入口。当前先提供视频生成能力；`clirelay-video` 保留为兼容包装。它调用正在运行的 CliRelay 服务：

- 创建任务：`POST /openai/v1/videos`
- 查询任务：`GET /openai/v1/videos/{video_id}`
- 下载内容：`GET /openai/v1/videos/{video_id}/content`
- 默认模型：`agnes-video-v2.0`

## 环境变量

```bash
export CLIRELAY_BASE_URL="https://cliapi.example.com"
export CLIRELAY_API_KEY="你的 CliRelay API Key"
export CLIRELAY_VIDEO_MODEL="agnes-video-v2.0"
```

`CLIRELAY_BASE_URL` 是服务根地址，不需要包含 `/v1`。

## CLI 用法

```bash
go run ./cmd/clirelay-tools video models

go run ./cmd/clirelay-tools video create \
  -prompt "8 秒竖屏视频，展示翡翠手镯细节" \
  -seconds 8 \
  -size 720x1280 \
  -wait \
  -output out.mp4

go run ./cmd/clirelay-tools video status -id video_xxx
go run ./cmd/clirelay-tools video download -id video_xxx -output out.mp4
```

兼容入口仍可使用：

```bash
go run ./cmd/clirelay-video create -prompt "8 秒竖屏视频" -wait -output out.mp4
```

## Codex MCP 配置

推荐直接连接服务端 HTTP MCP，不需要在用户机器上安装 `clirelay-tools`：

```toml
[mcp_servers.clirelay]
url = "https://cliapi.example.com/mcp"
bearer_token_env_var = "CLIRELAY_API_KEY"
```

然后在本机环境变量中放入 CliRelay API Key：

```bash
export CLIRELAY_API_KEY="你的 CliRelay API Key"
```

服务端 MCP 可用工具：

- `clirelay_video_models`：列出视频模型。
- `clirelay_video_create`：创建视频任务，可设置 `wait=true` 等待完成。
- `clirelay_video_status`：查询任务状态和 `video_url`。
- `clirelay_video_download`：返回鉴权下载 URL 和当前状态。远程 MCP 运行在 CliRelay 服务端，不能直接写入用户本机文件。

本地调试或需要直接写本机文件时，可以使用 stdio MCP。先构建二进制：

```bash
go build -o ./bin/clirelay-tools ./cmd/clirelay-tools
```

再在 Codex 的 MCP 配置中加入 stdio server：

```toml
[mcp_servers.clirelay_tools]
command = "/absolute/path/to/CliRelay/bin/clirelay-tools"
args = ["mcp"]
env = { CLIRELAY_BASE_URL = "https://cliapi.example.com", CLIRELAY_API_KEY = "你的 CliRelay API Key", CLIRELAY_VIDEO_MODEL = "agnes-video-v2.0" }
```

在 Codex 中可以直接说：

> 用这段文案生成一个 8 秒 9:16 的视频，完成后给我下载链接。

Codex 会通过 MCP 工具创建任务、轮询状态并返回可下载结果。需要直接保存到本机文件时，使用本地 stdio MCP 或 CLI。
