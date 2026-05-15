# LLM Proxy 设计

[← ROADMAP](../ROADMAP.md)

平台第一步要能跟 LLM 说话：admin 配 provider → 平台跑代理 → 任何 OpenAI SDK 客户端都能调 → 用量落表。Agent 端零 provider 知识，平台拿完整观测面，master key 永远不进容器。

模块切分：
- `modules/llm_provider` —— provider registry + usage log。Admin 接口走 cookie + RequireAdmin。
- `modules/llm_proxy` —— `/api/llm/v1/responses` 单一端点。鉴权走 Bearer `hgxs_` session token（由 `modules/runner` 的 `SessionTokenValidator` 解析，见 [agent-identity.md](agent-identity.md)）。

## Provider 模型

```
llm_providers
  id                BIGSERIAL
  name              TEXT UNIQUE           -- 在 admin URL 里出现，不在代理 URL 里出现
  type              openai | anthropic | openai-compat
  base_url          TEXT
  api_key_encrypted TEXT                  -- cryptobox sealed
  allowed_models    TEXT[]                -- 路由表：model → provider
  created_by        BIGINT
```

字段刻意精简：早期版本里的 `visibility / allowed_repos / rate_limit_rpm / is_platform_default / default_model` 全部被移除 —— 它们要么被 agent_session 维度的策略取代（repo / rate），要么在 model-based routing 下不再有意义（default）。

Provider api_key 走 `pkg/cryptobox`（AES-256-GCM，master key 来自 `config.llm.encryption_key`）；admin GET 不下行明文，只回 `has_api_key` 布尔位。

## 路由：model → provider

代理拿到 `POST /api/llm/v1/responses`：

1. 解 Bearer `hgxs_` → 拿到 `*AgentSession`（runner 模块的 `SessionTokenValidator`）。
2. 解请求体得到 `model`。
3. 调 `Repo.FindProviderByModel(ctx, model)` —— 走 `WHERE model = ANY(allowed_models) ORDER BY id ASC LIMIT 1`。GIN 索引让查询 O(log n)。
4. 解密该 provider 的 sealed api_key，按 `provider.type` dispatch 到对应 adapter。

> **决策：** 没引入 priority 字段。id ASC 让"先注册的赢"成为可预测规则；要换偏序就调 `allowed_models` 数组本身，不需要新列。

> **空 `allowed_models`：** 不参与路由 —— 显式不可用。要让某 provider 服务模型必须列举。

## Adapter 三种

| Type | 上游路径 | 翻译方向 |
|---|---|---|
| `openai` | `/v1/responses` | 透传 |
| `openai-compat` | `/v1/chat/completions` | Responses ↔ Chat Completions |
| `anthropic` | `/v1/messages` | Responses ↔ Messages |

代理本身只懂 Responses-API 进 / 出；adapter 的工作是把 typed `Request` 翻译成上游 wire、把上游 response 翻译回 typed `Response`。详细字段映射在 `internal/modules/llm_proxy/upstream/{openai,openai_compat,anthropic}.go`。

**reasoning effort：** OpenAI `reasoning.effort` 在 `openai-compat` 透传（不识别的厂商会忽略未知字段）；`anthropic` 翻成 `thinking.budget_tokens`（minimal/low → 1024，medium → 4096，high → 16384），同时 drop temperature、bump max_tokens 防 400。

**reasoning content round-trip：** DeepSeek 等 `openai-compat` 类 reasoner 返回 `reasoning_content` 字段；adapter 提取到 `Response.Reasoning`，下一回合的 `KindReasoning` input item 会回填到对应 assistant message 的 `reasoning_content` —— 跨轮 chain-of-thought 不丢。Anthropic thinking blocks 同理，外加 signature 字段在 strict mode 下必须 round-trip。

**Streaming：** v1 一律 501。typed Response 不表达 token stream。

## Usage log

每次请求 best-effort 写 `llm_usage_log`：

```
session_id | provider_id | model | prompt_tokens | completion_tokens
| total_tokens | reasoning_tokens | latency_ms | status_code | error_message | request_path
```

`session_id` 是 `agent_sessions.id`（无 FK，跨模块解耦）；写失败只记日志不阻断响应。`(provider_id, created_at DESC)` 复合索引给 M10+ dashboard 留路径。

## 鉴权链

- Admin `/api/admin/llm/*`：cookie session + `RequireAdmin`。
- 代理 `/api/llm/v1/responses`：纯 Bearer `hgxs_`，不读 cookie。

两条链不共享中间件 —— 浏览器自动带的 cookie 不能让一次 agent 调用混进 admin 身份。

## 实现拓扑

```
modules/llm_provider/
├── domain/                   Repo / Lookup interfaces
├── infra/                    sqlc 生成的查询 + cryptobox 封装
│   ├── queries.sql
│   ├── llmproviderdb/        generated
│   └── migrations/
└── handler/                  /api/admin/llm 路由

modules/llm_proxy/
├── upstream/                 Provider interface + 三个 adapter + wire 编解码
│   ├── upstream.go           Provider / Request / Response / Registry / UpstreamError
│   ├── wire.go               Responses-API JSON 编解码（union 类型走 UnmarshalJSON）
│   ├── openai.go             native Responses-API
│   ├── openai_compat.go      Chat Completions
│   └── anthropic.go          Messages
└── handler/                  /api/llm/v1/responses 路由 + 鉴权
```

## 测试

`internal/modules/llm_proxy/upstream/upstream_test.go` 用 `httptest.NewServer` 起 mock 上游，跑：
- Adapter 翻译（typed Request → 上游 wire → typed Response）
- ReasoningEffort 三档映射（low/medium/high → Anthropic budget_tokens）
- ReasoningContent 跨轮 round-trip
- UpstreamError 透传上游 status

DeepSeek 真上游验证：`cmd/probe-llm-proxy/main.go`（`//go:build probe`），跑 `DEEPSEEK_API_KEY=... go run -tags=probe ./cmd/probe-llm-proxy`。

## 不在本设计里的事

- **Provider 配置 UI**：admin 后台单独排期；本地一律 curl。
- **Key rotation 工具**：等真有需求再补 `hangrix llm rotate-key`。换 key 不能解旧密文。
- **Anthropic 流式 / 多模态**：text-only 非流式跑通即可，多模态等到 M9 上下文优化时一起做。
- **Per-host repo / per-role 配额**：列已经撤回；要做时挂在 agent_session 而非 provider 上。
