FROM oven/bun:1.2 AS panel-builder

WORKDIR /panel

COPY panel/package.json panel/bun.lock ./

RUN bun install --frozen-lockfile

COPY panel/ .

ARG VERSION=dev
ENV VITE_APP_VERSION=${VERSION}

RUN bunx vite build

FROM registry.access.redhat.com/ubi9/go-toolset:latest AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

COPY --from=panel-builder /panel/dist /app/panel-dist

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

RUN microdnf install -y tzdata && \
    microdnf clean all

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

COPY --from=panel-builder /panel/dist /CLIProxyAPI/static

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

STOPSIGNAL SIGTERM

ENTRYPOINT ["./CLIProxyAPI"]
