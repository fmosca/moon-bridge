# Configuration

> 完整示例见 [`config.example.yml`](config.example.yml)，JSON Schema 见 [`config.schema.json`](config.schema.json)

Moon Bridge 使用 YAML 配置文件。默认路径为当前目录下的 `config.yml`，通过 `-config <path>` 可指定任意路径。

## 顶层结构

```yaml
mode: "Transform"  # Transform / CaptureAnthropic / CaptureResponse

log:
  level: "info"   # debug / info / warn / error
  format: "text"  # text / json

server:
  addr: "127.0.0.1:38440"
  auth_token: ""

system_prompt: ""   # 全局 system prompt（可选）

defaults:
  model: "moonbridge"
  max_tokens: 65536

# 协议常量（仅作参考，非配置项）：
# "anthropic"      — Anthropic Messages API
# "openai-response" — OpenAI Responses API
# "google-genai"    — Google Generative AI (Gemini) API
# "openai-chat"     — OpenAI Chat Completions API
```

## Mode

| 值 | 行为 |
|-----|--------|
| `Transform` | 接收 OpenAI Responses 请求，按 Provider 协议转换后转发 |
| `CaptureAnthropic` | 透明代理到 Anthropic 上游（不转换） |
| `CaptureResponse` | 透明代理到 OpenAI 上游（不转换） |

## Server

```yaml
server:
  addr: "127.0.0.1:38440"    # 监听地址
  auth_token: ""              # Bearer 认证 Token（空 = 不认证）
```

## Models

模型定义包含上下文窗口、推理能力、扩展支持等元信息：

```yaml
models:
  my-model:
    context_window: 1000000         # 上下文窗口大小（token）
    max_output_tokens: 384000       # 最大输出 token 数
    display_name: "My Model"        # 展示名
    description: "..."              # 描述
    default_reasoning_level: "high" # 默认推理层级
    supported_reasoning_levels:
      - effort: "low"
        description: "Low effort reasoning"
      - effort: "medium"
        description: "Medium effort reasoning"
      - effort: "high"
        description: "High effort reasoning"
      - effort: "xhigh"
        description: "Extra high effort reasoning"
    supports_reasoning_summaries: true
    default_reasoning_summary: "auto"
    input_modalities:
      - "text"
      - "image"
    supports_image_detail_original: true
    web_search:
      support: "auto"     # auto / enabled / disabled / injected
    extensions:              # 扩展功能配置
      deepseek_v4:
        enabled: true
      visual:
        enabled: true
```

## Providers

Provider 定义上游 API 的连接信息和协议类型。

```yaml
providers:
  my-provider:
    base_url: "https://api.example.com"      # 上游 API 地址
    api_key: "sk-..."                         # API Key
    version: "2023-06-01"                     # API 版本（Anthropic 协议需要）
    user_agent: "moonbridge/1.0"
    protocol: "anthropic"                     # 协议类型（默认 anthropic）

    # Google GenAI 特有字段（protocol: "google-genai" 时有效）
    project: "my-gcp-project"                 # Google Cloud Project ID（Vertex AI 需要）
    location: "us-central1"                   # Google Cloud 区域
    api_version: "v1beta"                     # Gemini API 版本（默认 v1beta）

    # Web Search 配置
    web_search:
      support: "auto"           # auto / enabled / disabled / injected
      max_uses: 1
      tavily_api_key: "tvly-..."
      firecrawl_api_key: "fc-..."
      search_max_rounds: 3

    # 该 Provider 提供的模型列表
    offers:
      - model: my-model
        pricing:
          input_price: 2
          output_price: 8
          cache_write_price: 1
          cache_read_price: 0.25
```

### Protocol 类型

| 协议值 | 上游格式 | 对应 Adapter |
|---------|----------|-------------|
| `anthropic`（默认） | Anthropic Messages API | `internal/protocol/anthropic` |
| `openai-response` | OpenAI Responses API | `internal/protocol/openai`（直通） |
| `google-genai` | Google Generative AI (Gemini) API | `internal/protocol/google` |
| `openai-chat` | OpenAI Chat Completions API | `internal/protocol/chat` |

## Routes

路由将模型别名映射到特定 Provider 的上游模型：

```yaml
routes:
  alias-name:           # 客户端使用的模型名
    model: my-model     # models 段定义的模型名
    provider: my-provider  # providers 段定义的 Provider 名
```

路由使得客户端可以使用友好别名（如 `moonbridge` 或 `gpt-image`），而无需感知 Provider 全限定名（`model(provider)` 格式）。

## Web Search

```yaml
web_search:
  support: "auto"     # 全局默认值
  max_uses: 1
  tavily_api_key: ""
  firecrawl_api_key: ""
```

Web Search 支持可在模型级别、Provider 级别和全局三个层级覆盖。优先级：模型 > Provider > 全局。

支持模式：
| 模式 | 行为 |
|------|------|
| `auto` | 优先使用 Provider 原生 web_search API，不支持时自动回退到注入模式 |
| `enabled` | 启用 Provider 原生 web_search |
| `disabled` | 禁用 Web Search |
| `injected` | 通过 Tavily/Firecrawl 后端注入搜索结果 |

## Cache

```yaml
cache:
  mode: "explicit"              # off / explicit / automatic / hybrid
  ttl: "5m"
  prompt_caching: true
  automatic_prompt_cache: false
  explicit_cache_breakpoints: true
  allow_retention_downgrade: false
  max_breakpoints: 4
  min_cache_tokens: 1024
  expected_reuse: 2
  minimum_value_score: 2048
  min_breakpoint_tokens: 1024
```

## Extensions

```yaml
extensions:
  deepseek_v4:
    enabled: true
    config:
      reinforce_instructions: true
  visual:
    enabled: true
    config:
      provider: "kimi"
      model: "kimi-for-coding"
      max_rounds: 4
      max_tokens: 2048
  db_sqlite:
    enabled: true
    config:
      path: ./data/moonbridge.db
      wal: true
      busy_timeout_ms: 5000
      max_open_conns: 1
  metrics:
    enabled: true
    config:
      default_limit: 100
      max_limit: 1000
  # 以下扩展仅在 Cloudflare Worker 部署时使用：
  # db_d1:
  #   enabled: true
  #   config:
  #     binding: MOONBRIDGE_DB
```

## Proxy（Capture 模式）

仅在 Capture 模式下有效：

```yaml
proxy:
  response:
    base_url: "https://api.openai.com"
    api_key: "sk-..."
    model: "gpt-5.4"
  anthropic:
    base_url: "https://provider.example.com"
    api_key: "sk-..."
    version: "2023-06-01"
```

## CLI 标志

| 标志 | 默认值 | 描述 |
|------|--------|------|
| `-config` | `config.yml` | 配置文件路径 |
| `-addr` | 来自配置文件 | 覆盖监听地址 |
| `-auth-token` | 来自配置文件 | 覆盖认证 Token |
| `-trace` | `false` | 启用请求跟踪 |
| `-log-level` | `"info"` | 日志级别 |
| `-log-format` | `"text"` | 日志格式 |
