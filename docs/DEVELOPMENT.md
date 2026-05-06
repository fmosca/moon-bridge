# Development

## 前置要求

- Go 1.25+
- 上游 LLM Provider API Key（可选，用于 E2E 测试）

## 项目结构

```
cmd/
  moonbridge/     # 主入口（二进制）
  cloudflare/     # Cloudflare Worker 入口

internal/
  e2e/                    # 端到端集成测试
  extension/              # 可插拔扩展
    codex/                # Codex 模型目录
    db/                   # 数据库 Provider（SQLite / D1）
    deepseek_v4/          # DeepSeek V4 推理优化
    metrics/              # 用量指标
    plugin/               # 三方插件注册
    visual/               # 视觉模型分发
    websearch/            # Web Search 自动模式
    websearchinjected/    # Web Search 注入模式
  foundation/             # 基础能力层
    config/               # 配置加载/校验
    db/                   # 数据库抽象
    logger/               # 日志系统
    modelref/             # 模型引用解析
    openai/               # 共享 OpenAI DTO
    session/              # 会话管理
  protocol/               # 协议转换层
    anthropic/            # Anthropic Messages Adapter
    cache/                # Prompt 缓存规划
    chat/                 # OpenAI Chat Adapter
    format/               # Core 类型定义 + Registry
    google/               # Google Gemini (GenAI) Adapter
    openai/               # OpenAI Responses Adapter
  service/                # 业务编排层
    api/                  # 管理 API
    app/                  # 应用生命周期
    e2e/                  # 响应 E2E 测试
    provider/             # Provider 管理器
    proxy/                # Capture 模式代理
    runtime/              # 运行时上下文
    server/               # HTTP 服务器 + 路由
    stats/                # 用量统计
    store/                # 配置持久化
    trace/                # 请求跟踪
```

## 构建

```bash
# 构建二进制
go build -o moonbridge ./cmd/moonbridge

# 构建 Cloudflare Worker（WASM）
go build -o worker.wasm ./cmd/cloudflare
```

## 运行

```bash
go run ./cmd/moonbridge -config config.yml
```

支持热重载：修改配置后通过管理 API 或重启应用应用更改。

## 常用命令

```bash
# 运行单元测试（全量）
go test ./...

# 运行指定包测试
ogo test ./internal/protocol/anthropic/...
ogo test ./internal/protocol/google/...

# 运行 E2E 测试
cd internal/e2e && go test -v -count=1

# 运行 E2E 测试（使用真实 Provider）
cd internal/e2e && PROVIDER=deepseek go test -v -count=1 -run TestAnthropicE2E

# 构建并运行
make run
```

## 添加新 Provider Adapter

1. 在 `internal/foundation/config/config.go` 中添加协议常量（如 `ProtocolMyAdapter`）
2. 创建 `internal/protocol/<adapter>/` 包，实现 `format.ProviderAdapter` 和 `format.ProviderStreamAdapter` 接口
3. 在 `internal/service/app/app.go` 中注册 Adapter 到 Registry
4. 在 `internal/service/server/dispatch.go` 中添加协议分支
5. 添加对应的 E2E 测试

## 管理 API 开发

管理 API 端点定义在 `internal/service/api/` 中，通过 `Generator` 接口生成代码端配置：

```go
type Generator interface {
    GenerateConfigToml(cfg config.Config) (string, error)
    GenerateAgentConfig(config.Config) (map[string]any, error)
}
```

## 代码约定

- 文件名反映职责，不使用项目管理编号（如 `phase4_test.go`）
- 使用 `log/slog` 进行结构化日志
- 包级配置通过 `internal/foundation/config` 统一管理
- 协议转换统一使用 `format.CoreRequest` / `CoreResponse` 作为中间表示
