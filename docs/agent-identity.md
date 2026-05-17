# Agent Identity & Session Token

[← ROADMAP](../ROADMAP.md)

容器里跑的 agent 需要一张令牌才能跟平台说话。这张令牌的语义是「agent 身份」—— 它代表一次 agent_session 的身份，**而不是**对某个 LLM provider 的临时授权。同一张 token 给 LLM proxy、平台 agent-tools REST 端点、未来的 git push helper 共用。

## 演化

M6a 时把 session token 挂在 `modules/llm_provider`，每张 token 绑死到 (provider, model)。M6c 闭环时发现这个绑定有两个问题：

1. **耦合错位。** Token 表面是 LLM 设施的产物，实际是 agent 在容器里跑的身份凭证。Agent 调平台 agent-tools 端点 / 推 git 跟 LLM provider 没关系。
2. **路由绕弯。** 既然 token 绑 (provider, model)，代理 URL 又带 `{provider_name}` —— 两份冗余信息在校验链里反复对账。

重构后：

- Token 表搬进 `modules/runner`，作为 `agent_sessions` 行的属性（`session_token_prefix` / `session_token_hash` / `session_token_sealed` / `session_token_revoked_at` 四列）。
- 不再绑 provider / model；agent 想用哪个模型由请求体的 `model` 决定，代理 server-side 用 `allowed_models` 反查 provider（见 [llm-proxy.md](llm-proxy.md)）。
- 代理 URL 合并为单一 `/api/llm/v1/responses`，路径里不再出现 `{provider_name}`。
- llm_provider 模块退出 token 业务 —— 删 `llm_session_tokens` 表 + admin token CRUD 路由。

## 当前形态

```
agent_sessions
  id                       BIGSERIAL
  runner_id, repo_id, ...
  model                    TEXT             -- agent 要用的模型（informational + 注入 HANGRIX_LLM_MODEL）
  session_token_prefix     TEXT UNIQUE      -- 公开 prefix（仿 PAT）
  session_token_hash       TEXT             -- bcrypt(secret)
  session_token_sealed     TEXT             -- cryptobox 封的 plaintext，给 runner fetch
  session_token_revoked_at TIMESTAMPTZ
  ...
```

Wire 格式：`hgxs_<8>_<32>`，alphabet `[A-Za-z0-9]`。

## 生命周期

1. **创建会话。** admin（M6c 测试入口）或 M7a 调度器调 `CreateSession`：
   - 生成 plaintext + bcrypt(secret)，cryptobox-seal plaintext。
   - 写入 `agent_sessions`。
   - **不下行明文给 admin 调用方** —— 调用方不需要它，runner 才需要。
2. **Runner 拉任务。** runner 长轮询 `/api/runner/tasks` 拿到 task，service 端 cryptobox-unseal 出 plaintext，**只在 runner 的 Bearer-authed 通道下行一次**。
3. **Runner 注入容器。** 通过 `HANGRIX_SESSION_TOKEN` env 注入到 agent 容器，agent 用作所有平台调用的 Bearer。
4. **代理校验。** 任何 `hgxs_` 入站请求走 `modules/runner/service.SessionTokenValidator`：
   - 正则 prefix 形状。
   - `agent_sessions.session_token_prefix` 查一次。
   - bcrypt 比对 secret。
   - 检查 `SessionTokenActive(now)`（不在 terminal 状态、未被显式 revoke）。
5. **结束。** session 进入 `succeeded` / `failed` / `cancelled` 时 `MarkSessionTerminal` 把 `session_token_sealed = NULL` —— 终态后即使 DB 被泄也无法拿到 plaintext；prefix + hash 留行以便审计。

## 校验放在哪一层

> **决策：** Token 校验是 service-layer 关切，不是 persistence 关切。

`infra.PostgresRepo` 只暴露窄查询：
- `GetSessionByTokenPrefix(prefix)` → `*AgentSession`
- `GetRunnerByAgentTokenPrefix(prefix)` → `*Runner`

`modules/runner/service` 包 bcrypt + active 检查：
- `service.AgentTokenValidator` 实现 `domain.AgentValidator`（用于 runner-facing `hgxr_`）。
- `service.SessionTokenValidator` 实现 `domain.SessionTokenValidator`（用于 agent-facing `hgxs_`）。

Enrollment redemption（`hgxe_`）例外：它是 stateful 状态转移（`FOR UPDATE` 行锁 + 一次性写 used_at），住在 infra 跟其余 DB transaction 一起，由 `domain.EnrollValidator` 暴露。

## 跨模块消费

```
llm_proxy   ─┐
agent_tools  ├─► runner/domain.SessionTokenValidator ─► *AgentSession
push helper ─┘                                            ├─ ID / RunnerID / RepoID 等业务字段
                                                          ├─ Model（agent 应该调哪个 LLM）
                                                          └─ 终态判定 / 显式 revoke
```

每个消费者拿到 `*AgentSession` 后按自己的 policy 验更多东西（llm_proxy 看 model，agent-tools handler 看 role 的 `can:` / `not:` ACL，push helper 看 repo + branch）；通用部分（"这个 token 活的吗"）只校验一次。

## 三种 token 一览

| Prefix | 主体 | 用途 | 校验入口 |
|---|---|---|---|
| `hgx_` | user | PAT，git push + admin API | `modules/token` |
| `hgxe_` | runner | 一次性 enrollment | `modules/runner/infra.RedeemEnrollment` |
| `hgxr_` | runner | 长期 agent token，runner ↔ server | `modules/runner/service.AgentTokenValidator` |
| `hgxs_` | agent_session | agent ↔ 平台（LLM / agent-tools / git） | `modules/runner/service.SessionTokenValidator` |

四种前缀互不重叠 —— 一个 Bearer 头一眼可以分流到对应 validator。

## 不在本设计里的事

- **Token rotation：** v1 不做。Session 结束 token 就失效；要扩展 session lifetime 走开新 session。
- **Per-tool 权限：** session 是粗粒度身份，工具级 capability 由 host yaml `can:` + role 配置决定（M7a 引入）。
- **Multi-binding：** 一张 token 一个 agent_session，不打算让 token 在 session 间复用。
