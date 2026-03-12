# ── Frontend build ───────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM oven/bun:1 AS frontend-builder

WORKDIR /frontend
COPY frontend/ .
RUN bun install --frozen-lockfile
RUN bunx vite build

# ── Backend build ────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

# ── Runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates

RUN mkdir -p /CLIProxyAPI/panel

COPY --from=backend-builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=frontend-builder /frontend/dist/ /CLIProxyAPI/panel/

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai \
    MANAGEMENT_PANEL_DIR=/CLIProxyAPI/panel \
    CLIRELAY_LOCALE=zh

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
