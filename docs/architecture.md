# 系统架构

## 项目概述

Moon Bridge 是一个 Go 语言编写的 HTTP 代理/协议转换服务器。对外暴露 **OpenAI Responses API**（`/v1/responses`），对内支持 **Anthropic Messages**、**Google Gemini（GenAI）**、**OpenAI Chat Completions** 四种上游协议，以及 OpenAI Responses 直通。

核心定位：让 Codex CLI（或其他 OpenAI Responses API 客户端）通过一个统一入口访问不同协议的上游 LLM Provider，无需客户端感知协议差异。

## 四层架构

```
┌─────────────────────────────────────────────────┐
│                  Service 层                       │
│  server(路由/处理)  dispatch(协议分发)  proxy(代理) │
│  provider(路由)     stats(统计)      trace(跟踪)   │
│  api(管理 API)      store(持久化)    runtime(运行时) │
├─────────────────────────────────────────────────┤
│                  Protocol 层                      │
│  format(核心类型/注册表)  anthropic(Anthropic 适配) │
│  openai(OpenAI 适配)    google(GenAI 适配)        │
│  chat(OpenAI Chat 适配) cache(缓存)               │
├─────────────────────────────────────────────────┤
│                  Foundation 层                    │
│  config(配置)  logger(日志)  openai(共享 DTO)      │
│  modelref(模型引用)  session(会话)  db(数据库)     │
├─────────────────────────────────────────────────┤
│                  Extension 层                     │
│  deepseek_v4  visual  websearch  metrics         │
│  plugin(插件注册)  db_sqlite  db_d1               │
└─────────────────────────────────────────────────┘
```

### Foundation 层

基础能力层，不依赖任何 Protocol 或 Service 组件：

- `internal/foundation/config` — YAML 配置加载、校验、Schema 生成、热重载。支持 `config.schema.json` 和 `config.example.yml`
- `internal/foundation/logger` — 基于 `slog.Handler` 接口封装的日志系统，支持 consumer 模式（插件可注册日志消费者）
- `internal/foundation/openai` — 共享的 OpenAI 基础类型（DTO、枚举），被多个 Protocol 复用
- `internal/foundation/modelref` — 模型引用（例如 `model(provider)` 格式）的解析与规范化
- `internal/foundation/session` — 会话管理、上下文绑定
- `internal/foundation/db` — 数据库 Provider 注册表

### Protocol 层

协议转换核心，每个 Adapter 实现统一的 `format.ProviderAdapter` 接口：

- `internal/protocol/format` — 核心类型定义（`CoreRequest`、`CoreResponse`、`CoreTool`、`CoreContentBlock` 等）+ `Registry`
- `internal/protocol/openai` — **OpenAI Responses Adapter**：Core ⇄ OpenAI Responses 格式的相互转换；输入来自客户端、输出返回客户端
- `internal/protocol/anthropic` — **Anthropic Messages Adapter**：Core ⇄ Anthropic Messages 格式；Stream 事件转换、工具调用映射、缓存控制
- `internal/protocol/google` — **Google Gemini (GenAI) Adapter**：Core ⇄ Google Generative AI 格式（原生流式安全评估、SafetySettings / GenerationConfig 通过扩展字段透传）
- `internal/protocol/chat` — **OpenAI Chat Completions Adapter**：Core ⇄ OpenAI Chat Completions 格式
- `internal/protocol/cache` — Prompt 缓存规划（breakpoint 注入、TTL 管理、命中率跟踪）

### Service 层

业务编排层，组合 Foundation 和 Protocol 组件：

- `internal/service/server` — HTTP 服务器、路由（`/v1/responses`、`/v1/models`、`/health` 等）、认证、插件路由注册
- `internal/service/dispatch` — Adapter 分发路径（switch 协议类型 → 调用对应 Adapter）
- `internal/service/provider` — Provider 管理器（多 Provider 路由、配置热重载）
- `internal/service/proxy` — Capture 模式下的透明代理
- `internal/service/app` — 应用生命周期管理（初始化、注册 Adapter、启动 HTTP 服务）
- `internal/service/api` — 管理 REST API（运行时配置 CRUD）
- `internal/service/stats` — 用量统计（会话级别的 token 和费用聚合）
- `internal/service/trace` — 请求跟踪（捕获请求/响应的完整链路，持久化到 `data/trace/`）
- `internal/service/store` — 配置持久化存储（SQLite / D1）
- `internal/service/runtime` — 运行时上下文

### Extension 层

可插拔的功能扩展：

- `extensions/deepseek_v4` — 深度 DeepSeek 集成：reinforce instructions、CoT 链回放
- `extensions/visual` — 视觉模型任务分发：当主模型不支持图像时，自动路由到视觉模型处理
- `extensions/websearch` — Web Search 自动模式：通过原生 API（Tavily 等）执行搜索
- `extensions/websearchinjected` — Web Search 注入模式：工具注入到 Adapter 请求中
- `extensions/metrics` — 请求指标采集与查询
- `extensions/plugin` — 三方插件注册管理（`PluginRegistry` + `CorePluginHooks`）
- `extensions/db_sqlite` / `extensions/db_d1` — 持久化 Provider（SQLite 本地 / Cloudflare D1 Worker）

## 三种运行模式

| 模式 | 请求协议 → 上游协议 | 描述 |
|------|---------------------|------|
| `Transform`（默认） | OpenAI Responses → 任意 Adapter | 完整的协议转换流水线 |
| `CaptureAnthropic` | Anthropic Messages → Anthropic | 透明投递到 Anthropic 上游 |
| `CaptureResponse` | OpenAI Responses → OpenAI Responses | 透明投递到 OpenAI 上游 |

Capture 模式不经过 Adapter 路径，仅做透明代理转发。

## 请求生命周期数据流（Transform 模式）

```
客户端 (Codex CLI)
    │ POST /v1/responses (OpenAI Responses 格式)
    ▼
server.handleResponses()
    │ 认证 / 日志 / 统计初始化
    ▼
adapter_dispatch.go (Adapter 分发)
    │ preferred.Protocol 决定上游协议
    │ ProtocolOpenAIResponse  →  直通 openai.ProviderAdapter
    │ ProtocolAnthropic       →  anthropic.NewAnthropicProviderAdapter
    │ ProtocolGoogleGenAI     →  google.NewGeminiProviderAdapter
    │ ProtocolOpenAIChat      →  chat.NewChatProviderAdapter
    │
    ├── openai.FromCoreResponse()  (反向转换)
    │    CoreResponse ← upstream 响应
    │
    ├── 插件拦截 (PluginHooks)
    │    MutateCoreRequest → [Adapter] → RememberContent → OnStreamEvent
    │
    ▼
客户端 ←── OpenAI Responses 响应
```

### 初始化流程（`internal/service/app`）

```
app.Run()
  ├── 加载配置 (config.Load)
  ├── 初始化日志
  ├── 注册 Extension
  ├── 初始化 Provider 管理器
  ├── 创建 Adapter Registry
  │   ├── openai.NewOpenAIAdapter          (Client + Stream)
  │   ├── anthropic.NewAnthropicProviderAdapter (Provider + Stream)
  │   ├── google.NewGeminiProviderAdapter   (Provider + Stream)
  │   └── chat.NewChatProviderAdapter      (Provider + Stream)
  └── 启动 HTTP 服务器
```

## 模型路由

路由解析优先级：

1. 客户端直接指定 Provider 限定名（`model(provider)` 格式）
2. Moon Bridge `routes` 配置中的别名映射
3. Provider `offers` 列表中匹配模型名

### 模型引用格式

- 内部格式：`model(provider)` — 例如 `deepseek-v4-flash(deepseek)`
- 友好别名：通过 `routes` 配置定义，如 `moonbridge → deepseek-v4-pro(deepseek)`

### Provider 协议字段

每个 Provider 通过 `protocol` 字段声明上游协议：

```yaml
providers:
  deepseek:
    base_url: "https://api.deepseek.com/anthropic"
    protocol: "anthropic"      # → Anthropic Messages Adapter
  openai:
    base_url: "https://api.openai.com"
    protocol: "openai-response" # → OpenAI Responses 直通
  google:
    base_url: "https://generativelanguage.googleapis.com"
    protocol: "google-genai"    # → Google Gemini (GenAI) Adapter
  xiaoai:
    base_url: "https://api.xiaoai.com/v1"
    protocol: "openai-chat"     # → OpenAI Chat Adapter
```

协议常量的定义见 `internal/foundation/config/config.go`：
- `ProtocolAnthropic`
- `ProtocolOpenAIResponse`
- `ProtocolGoogleGenAI`
- `ProtocolOpenAIChat`

## Adapter 体系

所有 Adapter 实现同一接口族的四个方法：

```go
type ProviderAdapter interface {
    ProviderProtocol() string                     // 返回协议名
    FromCoreRequest(context.Context, *CoreRequest) (any, error)    // Core → 上游格式
    ToCoreResponse(context.Context, any) (*CoreResponse, error)    // 上游 → Core
}

type ProviderStreamAdapter interface {
    ProviderProtocol() string
    FromCoreRequest(context.Context, *CoreRequest) (any, error)
    ToCoreStreamEvent(context.Context, any) (*CoreStreamEvent, error)
    HandleStreamError(context.Context, error) (*CoreResponse, error)
}
```

每个 Adapter 被注册到 `format.Registry` 中，server 端的分发逻辑根据 `preferred.Protocol` 查找对应的 Adapter。

| Adapter | 协议 | 上游格式 | 备注 |
|---------|------|---------|------|
| openai (`internal/protocol/openai`) | `openai-response` | OpenAI Responses API | 兼做 Client 端 Adapter（入站请求反向转换） |
| anthropic (`internal/protocol/anthropic`) | `anthropic` | Anthropic Messages API | 支持 prompt 缓存 |
| google (`internal/protocol/google`) | `google-genai` | Google Generative AI (Gemini) API | SafetySettings/GenerationConfig 通过扩展字段 |
| chat (`internal/protocol/chat`) | `openai-chat` | OpenAI Chat Completions API | 工具调用/流式支持 |

### 跨协议工具调用

协议间工具调用的核心挑战在于格式差异。Moon Bridge 的 `CoreTool` / `CoreToolCall` / `CoreContentBlock` 作为中间表示，屏蔽了各协议的差异：

- **Anthropic** → `tool_use` / `tool_result` content blocks
- **OpenAI Response** → `function_call` / `function_call_output` items
- **OpenAI Chat** → `tool_calls` / `tool` role messages
- **Google Gemini** → `functionCall` / `functionResponse` parts

### Web Search 工具注入

`InjectWebSearchTool` 在 Transform 模式下动态注入 `web_search` 工具定义：

1. 检查 Provider 的 `web_search.support` 字段
2. `auto` 模式：优先使用原生 web_search API
3. `injected` 模式：注入工具 + 使用 Tavily/Firecrawl 后端执行
4. `enabled` / `disabled`：由 Provider 自身处理/禁用

## 缓存系统

通过 `internal/protocol/cache` 模块实现 Anthropic Messages API 的 prompt 缓存：

| 模式 | 行为 |
|------|------|
| `off` | 禁用缓存 |
| `explicit` | 仅注入显式 breakpoint（由 Adapter 控制） |
| `automatic` | 自动检测并添加 breakpoint |
| `hybrid` | 同时使用显式 + 自动 |

支持配置项：TTL、最小缓存 token 数、breakpoint 上限、预期复用率。

## 请求跟踪系统

`trace.enabled: true` 时，完整的请求链路保存到 `data/trace/`：

```
data/trace/
  Transform/
    deepseek-v4-flash/
      20260506T140136Z-459a0e4e/
        Response/
          1.json       # 原始 OpenAI Responses 请求 + 响应/错误
          3.json
          ...
        Anthropic/
          1.json       # 转换后的 Anthropic Messages 请求
          3.json
          ...
```

每个跟踪捕获：
- HTTP 请求头
- 完整的请求体（OpenAI + Anthropic 格式）
- 响应/错误
- 流式事件序列

## 管理 API

当 `persistence.active_provider` 启用时（SQLite 或 D1），Moon Bridge 提供管理 REST API：

| 端点 | 方法 | 功能 |
|------|------|------|
| `/api/v1/config` | GET | 获取当前配置 |
| `/api/v1/config` | PUT | 更新配置 |
| `/api/v1/codex/config` | GET | 生成 Codex TOML 配置 |
| `/api/v1/providers` | GET | 列出 Provider |
| `/api/v1/providers` | POST | 添加 Provider |
| `/api/v1/providers/{key}` | DELETE | 删除 Provider |

管理 API 通过 `internal/service/api` 和 `internal/service/store` 实现，配置持久化使用 SQLite 数据库。

## 关键设计决策

### 协议兼容性

- **统一 Core 格式**：所有协议转换以 `CoreRequest` / `CoreResponse` 为中间表示，新增协议只需实现 Adapter 接口
- **扩展字段**：协议特有的字段通过 `map[string]any` 扩展字段透传，不污染核心类型
- **流式支持**：每个 ProviderAdapter 都同时实现 Provider + ProviderStreamAdapter，统一流式/非流式转换

### 工具调用

- **统一工具定义**：`CoreTool` 包含 Name / Description / InputSchema / Extensions，各 Adapter 各自映射到协议特有格式
- **工具调用痕迹**：`CoreToolCall` + `CoreContentBlock[ToolUse|ToolResult]` 覆盖完整的多轮工具调用链

### Plugin 体系

- **CorePluginHooks 接口**：`MutateCoreRequest`（请求预处理）、`RememberContent`（响应记录）、`OnStreamEvent`（流拦截）
- **星型注册**：`PluginRegistry` 管理所有插件，server 端遍历调用

### 多 Provider & 会话隔离

- 每个 Provider 独立配置（base_url / api_key / protocol / offers）
- `ProviderManager` 支持运行时热加载
- 会话级隔离，统计信息按 session 聚合
