# Agent / Host 配置 schema（M7a）

[← ROADMAP](../ROADMAP.md)

Agent 协作的两份配置：

- **Agent 仓库** `<owner>/<name>` 的根目录有 `agent.yml`，是 agent 自身的清单。
- **Host 仓库** 的 `.hangrix/agents.yml` 是 team 配置，声明本仓库要起哪些 role、用哪个容器、注入哪些 secrets。

> 落实原则 7：agent 仓库**不**声明镜像 / 包 / 解释器版本 —— 那是 host 该说的话。Agent 跨仓库可复用，host 各自管自己的工具链。

---

## Agent 仓库结构

```
hangrix/reviewer/
├── agent.yml                     # 清单
├── prompts/
│   └── system.md                 # base prompt
└── README.md
```

`agent.yml`：

```yaml
version: 1
kind: agent
entry:
  base_prompt: prompts/system.md
declared_tools:                    # 推荐 / 文档，host 的 can: 才是真授权
  - issue_read
  - issue_comment
  - issue_review_vote
```

**仓库识别：** 根目录有 `agent.yml` 即识别为 agent；可见度规则跟普通 repo 一致（public 谁都引、private 同 owner 仓库引）。`agent.yml` 的 schema **拒绝**任何镜像 / 环境 / secret 字段。

---

## Host 仓库 `.hangrix/agents.yml`

仓库根的 `.hangrix/agents.yml` 是 team 行为的单一真相来源。配套 `.hangrix/agents.lock` 把 `@<tag>` / `@<branch>` 解到 sha（package-lock 模型）；runner 实际拉的是 sha。

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
    agent: hangrix/dispatcher@v1.2.0
    triggers: [issue.opened, issue.comment.any]
    can: [issue_read, issue_comment, roster_list]

  backend:
    agent: acme/backend-coder@v0.3.1
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
    mention_by: collaborators
    prompt: |                     # host addendum，追加到 agent base prompt
      Always git pull --rebase against issue/<n> before push.
      Cross-module imports MUST go through pkg/ioc DI.

  reviewer:
    agent: hangrix/reviewer@v1.0.0
    triggers: [commit.pushed, issue.comment.mentioned]
    can: [issue_read, issue_diff, issue_comment, issue_review_vote, read, glob, grep]
    mention_by: collaborators
    prompt_file: .hangrix/prompts/reviewer.md
    llm:                          # per-role 覆盖：reviewer 要更强推理
      model: claude-opus-4-7

  maintainer:
    agent: hangrix/maintainer@v1.0.0
    triggers: [review_vote.posted, ci.status_changed, commit.pushed, issue.comment.mentioned]
    can: [issue_read, issue_diff, issue_comment, issue_checks, issue_merge]
    mention_by: owner
    prompt: |
      Merge policy: ≥1 reviewer approval for apps/api/**, all required CI checks green,
      docs-only self-merge, "urgent"-labeled bypass review if CI green.
    llm:
      model: gpt-5
```

### 字段语义

- **`agent: <owner>/<name>@<ref>`** —— 必须有 `@<ref>`（拒空 ref，避免跟着上游漂移）。`<ref>` 可以是 tag / branch / sha；lock 文件统一解析到 sha。
- **`triggers:`** —— 事件订阅。Dispatcher 通常订 `issue.comment.any`（充当路由器）；其它 role 订 `issue.comment.mentioned` 等被 @ 唤醒。强制列具体事件。
- **`can:`** —— 平台工具白名单。Agent 仓库的 `declared_tools` 是文档，host 的 `can:` 才是真授权。
- **`scope.paths:`** —— 软约束（写进 role 的初始 prompt 让 dispatcher 知道分派给谁），不在 pre-receive 强制。
- **`mention_by:`** —— 谁的 @ 能唤醒：`owner` / `collaborators`（默认）/ `anyone`。违反者的 @-mention 进时间线但不投递事件，UI chip 灰色提示"未触发"。
- **`prompt:` 或 `prompt_file:`** —— host 给该 role 的 prompt addendum，**追加**到 agent base prompt 末尾。二选一（schema mutual exclusive）；`prompt_file:` 必须以 `.hangrix/prompts/` 开头。
- **`llm:`** —— 团队级 + per-role 两层；role 级覆盖团队级，团队级覆盖 platform default。字段 `model`（必须命中某 provider 的 `allowed_models`）+ 可选 `max_tokens` / `temperature` / `top_p`。Spawn session 时把 resolved model 缓存到 session 元数据，runner 注入 env 时直接读。
- **Runner 默认注入：** 给每个 role 容器注入一张统一的 session token（[agent-identity.md](agent-identity.md)），LLM endpoint + model 也是默认注入。

---

## Mention 协议

- 语法：`@agent-<role-key>`（如 `@agent-backend`）。`agent-` 前缀预留未来人类 `@<username>` 不撞名。
- 评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块与引用块。匹配到的 role key 去 base 分支的 `.hangrix/agents.yml` 查；存在且通过 `mention_by` 校验则投递 `issue.comment.mentioned` 事件给该 role。
- 同评论 @ 多个 role 投递 N 个独立事件（同 comment_id），各 role 串自己的流。
- 人类直接 `@agent-backend please fix X` 跟 dispatcher 发同样评论效果完全一致 —— "评论 + mention"是人、dispatcher、其它 agent 三方共用的同一协议，没有第二种唤醒方式。

---

## Identity 与 Audit

- commit author name = role key（如 `backend`），email = `<role-key>@agents.<host-domain>`。
- 每次 session 启动落两件版本信息进 audit：
  - `agent_sha`：agent 仓库 pinned 版本。
  - `repo_sha`：host 仓库 base 分支当时的 commit（含 `.hangrix/agents.yml` + `prompts/`）。
- 任何 commit / merge 都能 trace 回 cause `comment_id` —— M4 时间线 append-only 审计流的覆盖面延伸到 agent 全部动作。agent + host 两层 prompt 内容都可按 sha 精确复原。

---

## Session 模型（一 issue 多 role）

- **`modules/agent_session`：** 一个 issue 内每个被唤醒过的 role 各一个 session（取代原 "1 issue 1 session"）。
- 字段：issue id、role key、agent_ref + agent_sha、repo_sha、runner id、container id、状态（`pending | running | idle | archived | failed`）+ 解析后的 role 配置 snapshot（见下条）。
- **冻结点 = session spawn 那一刻。** 第一次唤醒某 role 时，按当时 host 仓库 base 分支的 commit 算 `repo_sha`、按 host yaml 写的 `agent: <owner>/<name>@<ref>` 解析出 `agent_sha`，再把解析后的 role 配置（host addendum prompt / `can:` 工具白名单 / resolved llm / container spec）一并 snapshot 进 session 元数据。**整 session 生命周期不再重读 host yaml** —— host yaml 中途改了不影响在跑的 session。同 issue 不同 role 各自冻结自己的 sha；中途新加的 role 在它第一次被唤醒时拍自己的照。这个约束是 audit 可重现性的支点：按 (`agent_sha`, `repo_sha`) 一对 sha checkout 出来就能精确复现 agent 当时看到的整套 prompt + 工具集 + 代码状态。
- 配套 `agent_session_messages` 存完整对话历史 —— OpenAI Responses-API 风格消息序列（user 事件 / assistant 消息 / tool call + result / 系统事件混排），按 `created_at` 排序；session 归档时只标记不删。
- **归档只能由 `issue.closed` / `issue.merged` 触发**：该 issue 上全部 session 同步 `archived`，容器回收。**无人工 archive 入口** —— admin 想停某 agent 的力度是「平台禁用 agent 仓库」或「host yaml 删 role」，不是逐 session 戳。已归档不允许重启 —— 想继续就开新 issue。
- **单 role 单容器串行（v1）：** 同 role 在同 issue 上同一时刻只跑一个容器，多 trigger 排队消化。多并发后续 milestone。
- **冲突自治：** 多 role 同时在 `issue/<n>` 上工作时，agent push 前自行 `git pull --rebase`。force-push 已禁，push 失败自然逼迫先 rebase —— 无需平台层协调机制。
