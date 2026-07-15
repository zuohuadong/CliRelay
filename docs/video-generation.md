# Video generation

CliRelay exposes one provider-neutral public Videos API and keeps the xAI-native
operations on explicit subroutes.

## Public API

Use the normal `/v1` base URL with an explicit video model:

```bash
curl -X POST "https://clirelay.example.com/v1/videos" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "agnes-video-v2.0",
    "prompt": "A cinematic tracking shot through a rainy neon street",
    "seconds": 8,
    "size": "1280x720"
  }'
```

The returned `id` is a CliRelay public task ID. Poll and download through the
same API:

```bash
curl "https://clirelay.example.com/v1/videos/$VIDEO_ID" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY"

curl -L "https://clirelay.example.com/v1/videos/$VIDEO_ID/content" \
  -H "Authorization: Bearer $CLIRELAY_API_KEY" \
  --output video.mp4
```

`/openai/v1/videos` remains an alias for existing integrations. The xAI-native
request shapes remain available at:

- `POST /v1/videos/generations`
- `GET /v1/videos/generations/{request_id}`
- `POST /v1/videos/edits`
- `POST /v1/videos/extensions`

Always send `model`. Examples include `agnes-video-v2.0` and
`grok-imagine-video`, depending on configured providers.

Multipart binary `input_reference` uploads are rejected explicitly instead of
being silently discarded. Upload the image to reachable storage and pass
`input_reference.image_url` until binary forwarding is implemented.

## Durable task routing

Public task routing is stored in `data/video.db`. It records the public task ID,
upstream task ID, provider, credential, model, status, and stored result metadata.
All CliRelay replicas that serve the same video API must share this data volume.

## S3-compatible result storage

Configure `video-storage` in `config.yaml` and provide credentials through the
named environment variables. Completed videos are copied to the configured
bucket when a completed task is polled. The status response then returns a
short-lived signed `video_url`, and `/content` redirects to a freshly signed URL.

Object-storage endpoints must use HTTPS; plain HTTP is accepted only for
loopback development endpoints. `max-source-bytes` limits each upstream video
archive (2 GiB by default). Archive downloads reject private, loopback,
link-local, and cloud metadata destinations, including redirect targets.

CliRelay does not delete stored video objects. Configure an R2/S3 bucket
lifecycle rule for `video-storage.prefix` (default: `videos/`) to enforce
retention. R2 Standard is the appropriate storage class for short-lived videos;
lifecycle deletion is asynchronous, so plan for objects to remain for up to
roughly one extra day.

If object storage is disabled or a copy cannot be completed, CliRelay safely
falls back to its authenticated content proxy.

## MCP and ChatGPT apps

Agent clients can configure `https://clirelay.example.com/mcp/video` as a
Streamable HTTP MCP server with `Authorization: Bearer <CliRelay API key>`.
The MCP tools reuse the same public task service and SQLite records.

Tool results contain both text content and `structuredContent`, providing the
data seam needed by a ChatGPT Apps SDK component. Publishing a discoverable
ChatGPT app still requires an Apps SDK UI/plugin and the normal review process;
the raw MCP endpoint is not automatically listed for users.
