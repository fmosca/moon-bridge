# Getting Started

> 5 分钟跑通第一个对话。更多用法见 [CookBook.md](CookBook.md)。

## 1. 安装

### 前置要求

- **Go 1.25+** — 用于编译和运行
- 一个上游 LLM Provider 的 API Key（如 DeepSeek、OpenAI、Anthropic、Kimi 等）

### 获取代码

```bash
git clone https://github.com/ZhiYi-R/moon-bridge.git
cd moon-bridge
```

### 编译

```bash
go build -o moonbridge ./cmd/moonbridge
```

或者直接运行：

```bash
go run ./cmd/moonbridge -config config.yml
```

## 2. 配置

复制示例配置并编辑：

```bash
cp config.example.yml config.yml
```

详细配置说明见 [CONFIGURATION.md](CONFIGURATION.md)。

### 最小配置示例

以 DeepSeek 为例，编辑 `config.yml`：

```yaml
mode: "Transform"
server:
  addr: "127.0.0.1:38440"

defaults:
  model: "deepseek-chat"

models:
  deepseek-chat:
    context_window: 1000000

providers:
  deepseek:
    base_url: "https://api.deepseek.com/anthropic"
    api_key: "sk-你的-DeepSeek-API-Key"
    version: "2023-06-01"
    protocol: "anthropic"
    offers:
      - model: deepseek-chat

routes:
  default:
    model: deepseek-chat
    provider: deepseek
```

### 与 Codex CLI 搭配使用

在 Codex Desktop 配置中设置 Moon Bridge 为 OpenAI API Base URL：

```toml
[openai]
base_url = "http://127.0.0.1:38440/v1"
api_key = "any-non-empty-value"
```

**支持四种协议的上游 Provider：**

| 协议 | 配置 protocol | 示例 Provider |
|------|---------------|---------------|
| Anthropic Messages | `anthropic` | DeepSeek、Kimi、Anthropic |
| OpenAI Responses | `openai-response` | OpenAI（直通） |
| Google GenAI (Gemini) | `google-genai` | Google Gemini |
| OpenAI Chat | `openai-chat` | 兼容 OpenAI Chat 的 API |

## 3. 启动

```bash
go run ./cmd/moonbridge -config config.yml
```

日志输出：

```
INFO HTTP 服务器监听中 addr=127.0.0.1:38440
```

## 4. 测试连通性

```bash
curl http://127.0.0.1:38440/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any-value" \
  -d '{
    "model": "default",
    "input": "Hello"
  }'
```

如果配置正确，将返回 LLM 的响应。

## 5. 验证模型列表

```bash
curl http://127.0.0.1:38440/v1/models
```

返回已注册的模型列表，包括来自所有 Provider 的模型及其能力描述。

## 下一步

- [CookBook.md](CookBook.md) — 常见用法场景
- [ARCHITECTURE.md](ARCHITECTURE.md) — 系统架构详解
- [CONFIGURATION.md](CONFIGURATION.md) — 完整配置指南
