# Moon Bridge

<p align="center">
  <a href="https://www.gnu.org/licenses/gpl-3.0">
    <img src="https://img.shields.io/badge/License-GPL%20v3-blue.svg" alt="GPL v3 License">
  </a>
</p>

Moon Bridge 是一个用 Go 编写的协议转换与模型路由代理。对外暴露 **OpenAI Responses API**（`/v1/responses`），对内支持 **Anthropic Messages**、**Google Gemini（GenAI）**、**OpenAI Chat Completions** 等多种上游协议。客户端指定不同模型别名时，自动将请求路由到对应上游 Provider 并在协议间自动转换。

> 🍳 **新手先看这里** → [CookBook.md](CookBook.md)：一份按目标找做法的菜谱，5 分钟跑通第一个对话。

---

## 快速开始

```bash
# 复制配置并编辑
cp config.example.yml config.yml
# 修改 config.yml 中的 api_key

# 启动
go run ./cmd/moonbridge -config config.yml

# 另见 CookBook.md 中的详细使用场景
```

要求 Go 1.25+。

## 核心能力

- **协议转换**：OpenAI Responses → Anthropic Messages / Google Gemini / OpenAI Chat，以及反向响应转换
- **模型路由**：通过 `routes` 配置将模型别名映射到不同 Provider 的上游模型
- **四种 Adapter 路径**：
  - **OpenAI Response（直通）**：直接转发到 OpenAI Responses API
  - **Anthropic Messages**：转换到 Anthropic 协议（DeepSeek / Kimi 等 Anthropic 兼容 API）
  - **Google Gemini（GenAI）**：转换到 Google Generative AI 协议
  - **OpenAI Chat**：转换到 OpenAI Chat Completions 协议
- **插件系统**：`CorePluginHooks` 接口，支持预处理输入、后处理响应、流拦截
- **请求跟踪**：每次请求的完整链路记录，支持调试和分析
- **用量统计**：基于会话的 token 用量与费用统计
- **管理 API**：运行时动态修改配置（需启用持久化）
- **Web Search 注入**：自动注入 `web_search` 工具到请求中
- **Prompt 缓存**：支持 explicit / automatic / hybrid 缓存模式

## 三种工作模式

| 模式 | 描述 |
|------|------|
| `Transform`（默认） | 接收 OpenAI Responses 请求 → 按 Provider 协议转换 → 转发 → 转换响应返回 |
| `CaptureAnthropic` | 接收 Anthropic Messages 请求 → 直接转发到预设的 Anthropic 上游 |
| `CaptureResponse` | 接收 OpenAI Responses 请求 → 直接转发到预设的 OpenAI upstream |

Transform 模式下，请求经过完整的协议转换流水线；Capture 模式下仅做透明中转。

## 配置说明

配置文件为 YAML 格式。完整示例见 [`config.example.yml`](config.example.yml)，配置 JSON Schema 见 [`config.schema.json`](config.schema.json)。

**核心结构**：

```yaml
# 工作模式
mode: "Transform"  # Transform / CaptureAnthropic / CaptureResponse

server:
  addr: "127.0.0.1:38440"
  auth_token: ""  # Bearer token，为空则禁用认证

models:
  # 模型定义：context_window / support / pricing / extensions
  my-model:
    context_window: 1000000
    display_name: "My Model"
    web_search:
      support: "auto"     # auto / enabled / disabled / injected
    extensions:
      deepseek_v4:
        enabled: true

providers:
  # Provider 定义：base_url / api_key / protocol / offers
  my-provider:
    base_url: "https://api.example.com"
    api_key: "sk-..."
    protocol: "anthropic"  # anthropic / openai-response / google-genai / openai-chat
    offers:
      - model: my-model

routes:
  alias:                  # 客户端使用的模型名
    model: my-model
    provider: my-provider
```

扩展字段支持 `Project`、`Location`、`APIVersion` 等 Google GenAI 特有配置，以及 `MaxTokens`、`Temperature` 等 Provider 级别默认值。

## 与 Codex CLI 配合使用

编辑 Codex Desktop 的 `config.toml`（或通过 `/api/v1/codex/config` 端点动态更新），将 Moon Bridge 设为 OpenAI API Base URL：

```toml
[openai]
base_url = "http://127.0.0.1:38440/v1"
api_key = "any-non-empty-value"
```

然后在 Moon Bridge 配置中定义与 Codex 模型同名的路由。

## 与 Claude Code 配合使用

```bash
claude --model your-alias \
  --api-url http://127.0.0.1:38440 \
  --api-key any-value
```

## Docker 部署

```bash
docker build -t moonbridge .
docker run -p 38440:38440 \
  -v $(pwd)/config.yml:/etc/moonbridge/config.yml \
  moonbridge
```

## 命令行选项

| 参数 | 默认值 | 描述 |
|------|--------|------|
| `-config` | `config.yml` | 配置文件路径 |
| `-addr` | `127.0.0.1:38440` | 监听地址（覆盖配置文件） |
| `-auth-token` | `""` | Bearer 认证 Token |
| `-trace` | `false` | 启用请求跟踪 |
| `-log-level` | `"info"` | 日志级别 |
| `-log-format` | `"text"` | 日志格式（text / json） |

## HTTP API 参考

| 端点 | 方法 | 描述 |
|------|------|------|
| `/v1/responses` | POST | OpenAI Responses API 主入口 |
| `/responses` | POST | 同上（无 `/v1` 前缀） |
| `/v1/models` | GET | 列出可用模型 |
| `/models` | GET | 同上 |
| `/api/v1/` | — | 管理 API（需启用持久化） |
| `/health` | GET | 健康检查 |

### 管理 API

当 `persistence.active_provider` 设为 `db_sqlite` 时可用：

- `GET /api/v1/config` — 获取当前配置
- `PUT /api/v1/config` — 更新配置（支持热重载）
- `GET /api/v1/codex/config` — 生成 Codex TOML 配置
- `GET /api/v1/providers` — 列出 Provider
- `POST /api/v1/providers` — 添加 Provider
- `DELETE /api/v1/providers/{key}` — 删除 Provider

## 用量统计与日志

- **Session 统计**：自动记录每次请求的 token 用量、费用、延迟
- **统计查询**：`GET /api/v1/sessions/{id}` 查看完整统计
- **日志格式**：支持 text / json 两种格式
- **日志级别**：debug / info / warn / error

## 请求跟踪

当 `trace.enabled: true` 时，每次请求的完整链路记录保存在 `data/trace/` 目录下。包括原始请求、转换后请求、上游响应、转换后响应。跟踪文件按 `Transform/模型名/时间戳/` 组织。

## Extension / 插件系统

Moon Bridge 内置了若干扩展：

| 扩展 | 功能 |
|------|------|
| `deepseek_v4` | DeepSeek V4 推理优化（reinforce instructions、CoT 链回放） |
| `visual` | 视觉模型任务分发（调用 Kimi 等模型处理图像相关任务） |
| `web_search` | Web Search 自动/注入模式（支持 Tavily、Firecrawl） |
| `metrics` | 请求指标采集与查询 |
| `db_sqlite` | SQLite 持久化存储 |
| `db_d1` | Cloudflare D1 持久化（Worker 部署） |
| `plugin` | 三方插件的注册与管理 |

插件通过 `CorePluginHooks` 接口接入流水线：

```go
type CorePluginHooks interface {
    MutateCoreRequest(ctx, *CoreRequest)      // 修改请求
    RememberContent(ctx, []CoreContentBlock)  // 记录响应
    OnStreamEvent(ctx, *CoreStreamEvent)      // 拦截流式事件
    ForEachCoreHook(func(CoreHook))
}
```

## 开源许可

[GPL v3](LICENSE)
