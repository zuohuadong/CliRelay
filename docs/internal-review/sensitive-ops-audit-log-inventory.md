# Sensitive Operations Audit Log Inventory

更新时间：2026-04-13

以下操作属于高敏感行为，后续应明确是否记录审计日志：

## Auth 文件

- 下载 auth 文件
- 上传 auth 文件
- 删除单个 auth 文件
- 批量删除 auth 文件
- 修改 auth 文件状态
- 修改 auth 文件可编辑字段

## OAuth / 凭据导入

- Anthropic / Gemini CLI / Codex / Antigravity / Qwen / Kimi / iFlow OAuth 发起
- OAuth 回调提交
- Vertex 凭据导入

## 配置写操作

- 写入配置 YAML
- 删除 proxy-url
- 删除 / 更新 API Key
- 删除 provider 配置
- 修改 Amp 上游 URL / API key / mappings

## 日志与高敏导出

- 删除运行日志
- 下载 request error log
- 下载 request log by id

## 当前状态

- 这份清单是盘点结果，不代表所有操作已经有审计日志。
- 后续应在关键管理写操作上统一记录：操作者来源、动作、对象、结果、时间。
