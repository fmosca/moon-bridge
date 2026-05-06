# API 接口

Moon Bridge 对外暴露 OpenAI Responses 兼容端点、模型列表端点和可选的管理 API。

## 基础信息

- **Base URL**：`http://127.0.0.1:38440`（默认）
- **认证**：通过 `auth_token` 配置启用 Bearer Token 认证
- **内容类型**：`application/json`

## 核心端点

### POST /v1/responses

OpenAI Responses API 兼容的聊天/补全端点。

**请求格式**：

```json
{
  "model": "default",
  "input": "Hello, who are you?",
  "include": ["reasoning.encrypted_content"],
  "tools": []
}
```

**关键字段**：

| 字段 | 类型 | 描述 |
|-------|------|-------------|
| `model` | string | 模型名或路由别名 |
| `input` | string 或 array | 输入文本或消息数组 |
| `include` | array | 可选，控制返回内容（如推理内容） |
| `tools` | array | 可选，工具定义列表 |
| `tool_choice` | object | 可选，工具选择策略 |
| `max_output_tokens` | number | 可选，最大输出 token 数 |
| `temperature` | number | 可选，采样温度 |
| `top_p` | number | 可选，核采样参数 |
| `stream` | boolean | 可选，是否启用流式响应 |
| `metadata` | object | 可选，自定义元数据 |

**响应格式**：

```json
{
  "id": "resp_xxx",
  "status": "completed",
  "model": "deepseek-v4-flash(deepseek)",
  "output": [
    {
      "type": "output_text",
      "text": "Hello! I'm an AI assistant...",
      "annotations": []
    }
  ],
  "usage": {
    "input_tokens": 10,
    "output_tokens": 42,
    "total_tokens": 52
  }
}
```

**流式响应**（`stream: true`）：

使用 Server-Sent Events (SSE) 格式：

```
event: response.output_item.added
data: {"type": "reasoning_summary_part", ...}

event: response.output_item.added
data: {"type": "output_text", ...}

event: response.completed
data: {"response": {...}, "usage": {...}}
```

流式事件类型：
- `response.output_item.added` — 添加新的输出项
- `response.content_part.added` — 添加内容块
- `response.text.delta` — 文本增量
- `response.reasoning_summary.delta` — 推理摘要增量
- `response.completed` — 响应完成
- `error` — 错误事件

### GET /v1/models

列出所有可用模型。

**响应**：

```json
[
  {
    "id": "deepseek-v4-flash(deepseek)",
    "object": "model",
    "created": 1700000000,
    "owned_by": "moonbridge",
    "permissions": [],
    "root": "deepseek-v4-flash(deepseek)",
    "parent": null
  }
]
```

### GET /responses、GET /models

与 `/v1/responses` 和 `/v1/models` 相同，无 `/v1` 前缀的兼容端点。

## 管理 API

当 `persistence.active_provider` 启用时，管理 API 在 `/api/v1/` 下可用。

### GET /api/v1/config

获取当前运行时配置。

### PUT /api/v1/config

更新运行时配置（支持热重载）。

**请求体**：完整或部分 YAML/JSON 配置。

### GET /api/v1/codex/config

生成供 Codex Desktop 使用的 TOML 配置。

### GET /api/v1/providers

列出所有已注册的 Provider。

### POST /api/v1/providers

添加新 Provider。

**请求体**：

```json
{
  "key": "my-provider",
  "base_url": "https://api.example.com",
  "api_key": "sk-...",
  "protocol": "anthropic",
  "offers": [
    {"model": "my-model"}
  ]
}
```

### DELETE /api/v1/providers/{key}

删除指定 Provider。

### GET /api/v1/sessions/{id}

获取会话用量统计。

## 错误处理

错误响应格式：

```json
{
  "error": {
    "message": "描述错误的信息",
    "code": "error_code",
    "status": 400
  }
}
```

常见错误码：

| HTTP 状态码 | 场景 |
|--------------|------|
| 400 | 请求参数错误 |
| 401 | 认证失败 |
| 404 | 模型/端点不存在 |
| 422 | 请求体格式错误 |
| 429 | 上游 Provider 限流 |
| 500 | 内部错误 |
| 502 | 上游 Provider 错误 |

## 与 Codex CLI 集成

Codex CLI 使用标准的 OpenAI Responses API，因此只需在 Codex 配置中指向 Moon Bridge 地址即可：

```toml
[openai]
base_url = "http://127.0.0.1:38440/v1"
api_key = "any-non-empty-value"
```

Moon Bridge 会自动处理路由和协议转换。
