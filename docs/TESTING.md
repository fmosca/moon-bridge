# Testing

Moon Bridge 使用 Go 标准库 `testing` 包，无外部测试框架依赖。

## 运行测试

```bash
# 全量测试
go test ./...

# 包级别
go test ./internal/protocol/anthropic/...

# 详细输出
go test -v -count=1 ./internal/protocol/...

# 测试覆盖率
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

## 测试层级

### 1. 单元测试

位置：与被测试包同目录

- `internal/protocol/anthropic/adapter_test.go` — Anthropic Adapter 转换测试
- `internal/protocol/google/google_test.go` — Google GenAI Adapter 转换测试
- `internal/protocol/chat/chat_test.go` — OpenAI Chat Adapter 转换测试
- `internal/protocol/openai/adapter_test.go` — OpenAI 适配器测试
- `internal/service/server/server_test.go` — 服务器路由/处理测试
- 各 extension 目录下的 `*_test.go`
- 各 foundation 目录下的 `*_test.go`

特点：使用 Mock HTTP 服务器，不依赖真实 API。

### 2. 协议转换 E2E 测试

位置：`internal/e2e/`

包括 6 个独立测试文件，覆盖所有 4 条转换路径 + 插件 + Web Search：

| 测试文件 | 覆盖范围 |
|-----------|---------|
| `anthropic_e2e_test.go` | Anthropic Messages 协议转换（请求/响应/流式/工具调用） |
| `google_genai_e2e_test.go` | Google Gemini (GenAI) 协议转换 |
| `openai_chat_e2e_test.go` | OpenAI Chat Completions 协议转换 |
| `openai_response_e2e_test.go` | OpenAI Responses 直通路径 |
| `plugin_hooks_e2e_test.go` | CorePluginHooks 全链路集成（PreprocessInput / PostProcessResponse / StreamInterceptor） |
| `websearch_injection_e2e_test.go` | Web Search 注入路径 |

E2E 测试分为两种模式：
- **Mock 模式（默认）**：Mock HTTP 服务器，测试协议转换逻辑
- **真实 Provider 模式**：由 `.env.test` 中的配置决定；设置对应 Provider 的 API Key 即可触发

### 3. 服务层 E2E 测试

位置：`internal/service/e2e/`

- `responses_e2e_test.go` — 完整 HTTP 请求/响应链路测试

### 4. 管理 API 测试

位置：`internal/service/api/`

- `api_test.go` — 管理 API 端点功能测试
- `api_e2e_test.go` — 管理 API 完整集成测试

## 运行 E2E 测试

```bash
# Mock 模式（无需 API Key）
go test ./internal/e2e/... -v -count=1

# 特定 Provider 的 E2E 测试
cd internal/e2e && PROVIDER=deepseek go test -v -count=1 -run TestAnthropicE2E
cd internal/e2e && PROVIDER=gemini go test -v -count=1 -run TestGoogleGenAIE2E
cd internal/e2e && PROVIDER=openai-chat go test -v -count=1 -run TestOpenAIChatE2E
cd internal/e2e && PROVIDER=openai go test -v -count=1 -run TestOpenAIResponseE2E
cd internal/e2e && PROVIDER=plugin-websearch go test -v -count=1 -run TestPluginHooksE2E
```

真实 Provider 模式需要配置 `.env.test` 文件（参考 `.env.test.example`）。

## 编写测试

- 使用 `httptest.NewServer` 模拟上游 API
- 协议转换测试通过比较 Core 格式 ⇄ 协议格式的相互转换结果验证正确性
- E2E 测试通过完整的 HTTP 请求/响应链验证
- 覆盖率目标：单元测试 ≥ 95%，E2E 覆盖所有协议路径

## 测试数据与跟踪

每次 E2E 测试产生的请求跟踪数据保存在 `data/trace/` 下，可用于调试和分析协议转换的正确性。
