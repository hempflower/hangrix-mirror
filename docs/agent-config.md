# Host 仓库 agents.yml schema

[← ROADMAP](../ROADMAP.md)

每个 host 仓库通过根目录的 `.hangrix/agents.yml` 声明 team 行为 —— 哪些 role、各自的 prompt、触发条件、`can:` 工具白名单、容器环境、LLM 选型。**所有 agent 配置都在 host 仓库内部**：role 的提示词写成 inline `prompt:` 或 `.hangrix/prompts/<role>.md` 文件；版本固定 = host 仓库的 commit sha；没有第二个仓库或 lock 文件需要追踪。

> 落实原则 7：host 仓库说自己用什么环境跑 agent。没有独立的「agent 仓库」概念 —— 跨 host 仓库复用 prompt 直接复制 markdown 文件，几十行 prompt 不值得专门的仓库引用 / lock 文件 / bundle 分发三段链路。

---

## 仓库布局

```
host-repo/
├── .hangrix/
│   ├── agents.yml
│   └── prompts/
│       ├── dispatcher.md
│       ├── backend.md
│       └── reviewer.md
└── ...your code...
```

`.hangrix/agents.yml`：

```yaml
version: 1

container:                        # host 声明的容器环境
  # image / build 二选一
  image: ghcr.io/acme/dev:1.2.3
  # build:
  #   dockerfile: .hangrix/agent.Dockerfile
  #   context: .
  #   args: { GO_VERSION: "1.26" }

  env:                            # 明文环境变量，入 git
    NODE_ENV: development
    GOFLAGS: "-mod=readonly"

  secrets:                        # 只列名字，值在仓库设置的"机密"页面配
    - GITHUB_TOKEN
    - NPM_AUTH_TOKEN

  volumes:                        # repo-scope 共享缓存（runner 本地 bind mount）
    - { name: pnpm-store, mount: /caches/pnpm }
    - { name: go-mod, mount: /go/pkg/mod }

llm:                              # team 默认 LLM（可选；省略走 admin 配的 platform default）
  model: claude-sonnet-4-6        # 路由由 provider.allowed_models 反查决定

roles:
  dispatcher:
    triggers: [issue.opened, issue.comment.any]
    can: [issue_read, issue_comment, roster_list]
    prompt_file: .hangrix/prompts/dispatcher.md

  backend:
    triggers: [issue.comment.mentioned]
    scope: { paths: ["apps/api/**", "internal/**"] }
    can:
      - issue_read
      - issue_diff
      - issue_comment
      - read
      - write
      - edit
      - glob
      - grep
      - bash
    prompt: |
      You write Go backend code in apps/api/** and internal/**.
      Always git pull --rebase against issue/<n> before push.
      Cross-module imports MUST go through pkg/ioc DI.

  reviewer:
    triggers: [commit.pushed, issue.comment.mentioned]
    can: [issue_read, issue_diff, issue_comment, issue_review_vote, read, glob, grep]
    prompt_file: .hangrix/prompts/reviewer.md
    llm:                          # per-role 覆盖：reviewer 要更强推理
      model: claude-opus-4-7

  maintainer:
    triggers: [review_vote.posted, ci.status_changed, commit.pushed, issue.comment.mentioned]
    can: [issue_read, issue_diff, issue_comment, issue_checks, issue_merge]
    prompt: |
      Merge policy: ≥1 reviewer approval for apps/api/**, all required CI checks green,
      docs-only self-merge, "urgent"-labeled bypass review if CI green.
    llm:
      model: gpt-5
```

### 字段语义

- **`triggers:`** —— 事件订阅。Dispatcher 通常订 `issue.opened` + `issue.comment.any` 当路由器；其它 role 订 `issue.comment.mentioned` 等被 @ 唤醒。强制列具体事件，没有 wildcard。
- **`can:`** —— 平台工具白名单。没在 `can:` 里的工具 `tools/list` 看不到、`tools/call` 返 `isError`。
- **`not:`** —— 平台工具黑名单。仅在 `can:` 留空时生效，语义是「除列出的工具外其它都可用」。`can:` 和 `not:` 同时给值时**白名单优先**，`not:` 被忽略；两者都为空则 fail-closed（无任何工具）。
- **`scope.paths:`** —— 软约束（写进 role 的初始 prompt 让 dispatcher 知道分派给谁），不在 pre-receive 强制。
- **`prompt:` 或 `prompt_file:`** —— role 的提示词。二选一（schema mutually exclusive）；`prompt_file:` 必须以 `.hangrix/prompts/` 开头，文件随仓库一起进 git。**没有 host addendum 的概念了** —— 直接写 role 自己的完整 prompt 即可。
- **`llm:`** —— team 级 + per-role 两层；role 级覆盖 team 级，team 级覆盖 platform default。字段 `model`（必须命中某 provider 的 `allowed_models`）+ 可选 `max_tokens` / `temperature` / `top_p`。Spawn session 时把 resolved model 缓存到 session 元数据，runner 注入 env 时直接读。
- **Runner 默认注入：** 给每个 role 容器注入一张统一的 session token（[agent-identity.md](agent-identity.md)），LLM endpoint + model 也是默认注入。

### Schema 强约束

- 严格拒未知键 —— 笔误立刻报错而不是静默忽略。
- `version: 1` 必填。
- `roles:` 至少一个；role key 限制 `[a-z][a-z0-9-]*`，**预留 `agent-` 前缀给 mention 协议**（参见下文）。
- `prompt:` / `prompt_file:` 二选一互斥；同时给两个直接 reject。
- `container.image` / `container.build` 二选一互斥。

---

## Mention 协议

- 语法：`@agent-<role-key>`（如 `@agent-backend`）。`agent-` 前缀预留未来人类 `@<username>` 不撞名。
- 评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块与引用块。匹配到的 role key 去 base 分支的 `.hangrix/agents.yml` 查；只要 role 存在且订阅了 `issue.comment.mentioned` 触发器就投递事件，没有额外的 actor-class 网关 —— 任何能写评论的人（读权限已经在评论入口校验）都可以唤醒任何 role。
- 同评论 @ 多个 role 投递 N 个独立事件（同 comment_id），各 role 串自己的流。
- 人类直接 `@agent-backend please fix X` 跟 dispatcher 发同样评论效果完全一致 —— 「评论 + mention」是人、dispatcher、其它 agent 三方共用的同一协议，没有第二种唤醒方式。

---

## Prompt 拼装

Agent 容器内 LLM 实际看到的 prompt 由两层 + runtime 上下文拼接：

```
[runtime 上下文 KV]   ← agent / runner 注入：role key / issue id / repo / cause kind / ...
  ↓
[平台 baseline]       ← agent 二进制 `//go:embed`，按 RFC 2119 关键词写规则
  ↓                     明文声明 baseline 不可被上层 prompt weaken
[role prompt]         ← host yaml 的 `prompt:` 或 `prompt_file:` 解析后内容
```

> 历史注记：v1 之前的设计是三层（baseline → agent 仓库 base_prompt → host addendum），随 agent-as-repo 一并取消。想跨 host 仓库复用 prompt 直接复制 markdown 文件即可。

---

## Identity 与 Audit

- commit author name = role key（如 `backend`），email = `<role-key>@agents.<host-domain>`。
- 每次 session 启动落一份版本信息进 audit：`repo_sha` = host 仓库 base 分支当时的 commit（含 `.hangrix/agents.yml` + `.hangrix/prompts/`）。
- 任何 commit / merge 都能 trace 回 cause `comment_id` —— M4 时间线 append-only 审计流的覆盖面延伸到 agent 全部动作。**按 `repo_sha` checkout 即可精确复现 agent 当时看到的整套 prompt + 工具集 + 代码状态**，无需第二个仓库的对位 checkout。

---

## Session 模型（一 issue 多 role）

- **`modules/agent_session`：** 一个 issue 内每个被唤醒过的 role 各一个 session（取代原 "1 issue 1 session"）。
- 字段：issue id、role key、`repo_sha`、runner id、container id、状态（`pending | running | idle | archived | failed`）+ 解析后的 role 配置 snapshot（见下条）。
- **冻结点 = session spawn 那一刻。** 第一次唤醒某 role 时，按当时 host 仓库 base 分支的 commit 算 `repo_sha`，把解析后的 role 配置（prompt 内容 / `can:` 工具白名单 / resolved llm / container spec）一并 snapshot 进 session 元数据。**整 session 生命周期不再重读 host yaml** —— host yaml 中途改了不影响在跑的 session。同 issue 不同 role 各自冻结自己的 `repo_sha`；中途新加的 role 在它第一次被唤醒时拍自己的照。这个约束是 audit 可重现性的支点。
- 配套 `agent_session_messages` 存完整对话历史 —— OpenAI Responses-API 风格消息序列（user 事件 / assistant 消息 / tool call + result / 系统事件混排），按 `created_at` 排序；session 归档时只标记不删。
- **归档只能由 `issue.closed` / `issue.merged` 触发**：该 issue 上全部 session 同步 `archived`，容器回收。**无人工 archive 入口** —— admin 想停某 role 的力度是「host yaml 删 role」或「平台禁用整张 yaml」，不是逐 session 戳。已归档不允许重启 —— 想继续就开新 issue。
- **单 role 单容器串行（v1）：** 同 role 在同 issue 上同一时刻只跑一个容器，多 trigger 排队消化。多并发后续 milestone。
- **冲突自治：** 多 role 同时在 `issue/<n>` 上工作时，agent push 前自行 `git pull --rebase`。force-push 已禁，push 失败自然逼迫先 rebase —— 无需平台层协调机制。
