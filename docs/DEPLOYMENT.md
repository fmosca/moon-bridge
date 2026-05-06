# Deployment

Moon Bridge 支持两种部署方式：独立二进制和 Cloudflare Workers WASM。

> **注意**：本文档中的基础设施配置（反向代理、Docker Compose 编排、Wasm 部署等）为示例，请根据实际环境调整。

## 独立二进制部署

### 编译

```bash
go build -o moonbridge ./cmd/moonbridge
```

### 运行

```bash
./moonbridge -config /path/to/config.yml
```

### 以 systemd 服务运行

```ini
[Unit]
Description=Moon Bridge
After=network.target

[Service]
ExecStart=/usr/local/bin/moonbridge -config /etc/moonbridge/config.yml
Restart=always
RestartSec=5
User=moonbridge

[Install]
WantedBy=multi-user.target
```

### 使用反向代理（Nginx）

```nginx
server {
    listen 443 ssl;
    server_name moonbridge.example.com;

    location / {
        proxy_pass http://127.0.0.1:38440;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;  # 流式响应需要
    }
}
```

## Docker 部署

示例 `docker-compose.yml`：

```yaml
services:
  moonbridge:
    build: .
    ports:
      - "38440:38440"
    volumes:
      - ./config.yml:/etc/moonbridge/config.yml
      - ./data:/app/data
    command: ["-config", "/etc/moonbridge/config.yml"]
```

详细示例见 [`docker-compose.example.yml`](docker-compose.example.yml)。

### Dockerfile

Moon Bridge 支持多阶段构建：

```dockerfile
FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o moonbridge ./cmd/moonbridge

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/moonbridge .
EXPOSE 38440
CMD ["./moonbridge", "-config", "config.yml"]
```

## Cloudflare Workers WASM 部署

Moon Bridge 支持编译为 WASM 部署到 Cloudflare Workers：

```bash
go build -o worker.wasm ./cmd/cloudflare
```

然后通过 `wrangler.toml` 配置 Worker：

```toml
name = "moonbridge"
main = "worker.wasm"
compatibility_date = "2025-01-01"

[vars]
CONFIG = "..."

[[d1_databases]]
binding = "MOONBRIDGE_DB"
database_name = "moonbridge"
database_id = "..."
```

## 配置管理

- 配置文件通过 `-config` 参数指定
- 运行时通过管理 API（`/api/v1/config`）热重载
- 配置持久化：默认 SQLite（`db_sqlite`），Cloudflare 环境可用 D1（`db_d1`）

## 日志

- 默认输出到 stdout，适合容器化部署
- 支持 `text` 和 `json` 格式
- 日志级别：`debug` / `info` / `warn` / `error`

<!-- VERIFY: 云厂商（AWS/GCP/Azure）的部署指南不在本仓库范围内，请自行配置。 -->
<!-- VERIFY: 生产环境的 SSL 证书管理请使用反向代理（如 Nginx、Caddy）或 Cloudflare 代理。 -->
<!-- VERIFY: Worker WASM 部署需 Wrangler 和 Cloudflare 账号，具体步骤见 Cloudflare 官方文档。 -->
