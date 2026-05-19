# Hangrix 项目概览

Hangrix 是一个**自托管的、AI 原生的代码协作平台**，纵向整合了三个层：

1. **Git 仓库托管** — 完整的 Smart HTTP 后端（`apps/hangrix/internal/modules/repo/handler/git_http.go`），支持 clone/fetch/push、public/private、分支保护。不是对接 GitHub API，是自己托管 bare repo。
2. **Issue 追踪** — 每个 issue 绑定一个 Git 分支 + agent 会话。AI 代理在 Docker 容器里自动处理 issue（读代码 → 改代码 → 提交 → 推送）。人类和 agent 的评论/事件混排时间线。生命周期：open → agent work + review → merged → 分支删除。
3. **AI 代理协作** — 多角色（product-designer / implementer / reviewer / maintainer）通过 `.hangrix/agents.yml` 配置，每个角色有不同的 trigger、tool 白名单、prompt。Agent 在隔离 Docker 容器里运行，通过平台 LLM 代理调模型（API key 不入容器），用 session token 鉴权。

## 四个组件

| 组件 | 位置 | 做什么 |
|---|---|---|
| 控制面服务 | `apps/hangrix/` | Go + chi + PostgreSQL + Redis。管理用户/组织/仓库/issue/agent 会话。内嵌前端 SPA。 |
| Agent 运行时 | `apps/hangrix-agent/` | 容器内运行的二进制。LLM 调用、工具执行、工作循环。 |
| 容器编排器 | `apps/hangrix-runner/` | 宿主机进程。拉任务 → 启 Docker 容器 → 转发 stdin/stdout。 |
| Web 前端 | `apps/web/` | Nuxt 4 + shadcn-vue。仓库浏览、issue 管理、Admin 面板。构建后 `//go:embed` 进 hangrix 二进制。 |

## 关键设计决策

- **Prompt 在仓库里** — 没有外部 agent bundle。改 prompt 就是一个 commit。
- **LLM API key 不入容器** — 服务端加密存储，agent 用 session token 调统一代理端点 `/api/llm/v1/responses`。
- **模块化单体** — `pkg/ioc` DI 容器，模块间只能依赖对方 `domain/` 接口。
- **平台自举** — 本仓库用 Hangrix 开发 Hangrix。

深入阅读：`docs/tech-stack.md`、`docs/agent-config.md`、`docs/runner-protocol.md`、`docs/llm-proxy.md`、`docs/agent-identity.md`。
