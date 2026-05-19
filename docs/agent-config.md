# Host 仓库 agents.yml schema

[← ROADMAP](../ROADMAP.md) · [JSON Schema](./agents.schema.json)

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

  # entrypoint: 覆盖容器 PID 1。省略 = runner 用内置默认
  # `/usr/bin/sleep infinity`（容器只是被 docker-exec 的 sandbox）。
  # 镜像里烤了 s6-overlay / supervisord 等监管进程要让它接管时填这里。
  # entrypoint: ["/init"]

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
  reasoning_effort: medium        # 思考强度：minimal / low / medium / high 走内置翻译，其它字符串原样透传给上游；省略走上游默认
  max_context_tokens: 200000      # 最大上下文 token（agent 端 prompt+历史的上限）；0 = 不约束
  max_output_tokens: 8000         # 最大输出 token（单次调用 completion 上限）；0 = 上游默认

roles:
  dispatcher:
    triggers:                     # 路由器：每条新评论 + 新 issue 都听
      issue.opened: {}
      issue.comment: {}
    can: [issue_read, issue_comment, roster_list]
    prompt_file: .hangrix/prompts/dispatcher.md

  backend:
    triggers:
      issue.comment:
        mentioned_only: true      # 只在被 @agent-backend 时唤醒
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
    triggers:
      commit.pushed:
        paths: ["apps/api/**", "internal/**"]
        paths_ignore: ["**/*.md", "**/testdata/**"]
      issue.comment:
        mentioned_only: true
    can: [issue_read, issue_diff, issue_comment, issue_review_vote, read, glob, grep]
    prompt_file: .hangrix/prompts/reviewer.md
    llm:                          # per-role 覆盖：reviewer 要更强推理
      model: claude-opus-4-7
      reasoning_effort: high      # 评审多想一下
      max_output_tokens: 16000

  maintainer:
    triggers:
      review_vote.posted: {}
      ci.status_changed: {}
      commit.pushed: {}
      issue.comment:
        mentioned_only: true
    can: [issue_read, issue_diff, issue_comment, issue_checks, issue_merge]
    prompt: |
      Merge policy: ≥1 reviewer approval for apps/api/**, all required CI checks green,
      docs-only self-merge, "urgent"-labeled bypass review if CI green.
    llm:
      model: gpt-5
```

### 字段语义

- **`container.image:` vs `container.build:`** —— 二选一互斥，spawner 已都支持：
  - `image: <ref>` —— runner 让 docker daemon 直接 pull（或本地命中即用）。这是最快路径，适合镜像已经发布到 registry 的情况。
  - `build: { dockerfile: <path>, context: <path>, args: { … } }` —— runner 收到 task 后先按 host repo 里那份 Dockerfile 跑 `docker build -t <auto-tag>`，再 `docker create` 用该 tag。auto-tag 由 spawner 端按 `(repo_id, dockerfile, context, args)` 算 sha256 出来——同样的 build spec 始终复用同一个 tag，docker 的本地 layer cache 接管增量 rebuild。`dockerfile` / `context` 都是 host-repo 相对路径，runner 会把它们 join 到 cloned checkout 目录上。Dockerfile 改了但 spec 不变 → 同一个 tag 重新 build（docker 的 layer cache 自动失效改动层）；spec 改了 → 新 tag，老镜像保留直到 `docker image prune`。BuildKit 默认启用（DOCKER_BUILDKIT=1），所以 `# syntax=docker/dockerfile:1.x` heredoc 可用。
- **`container.entrypoint:`** —— `[]string`，覆盖容器 PID 1。第一个元素作为 `docker create --entrypoint <argv0>`，后续元素作为 image 后的 CMD args。省略 / 空列表 = runner 用内置默认 `/usr/bin/sleep infinity`（容器仅作为 `docker exec` 的被动 sandbox）。要让镜像里烤好的 supervisor（如 s6-overlay `/init`、`supervisord`、`tini`）接管 PID 1 在容器启动时拉起 postgres / redis 等服务，就把它显式写出来。元素不能是空串；空列表跟未声明等价。
- **`triggers:`** —— 事件订阅。**Map 形式**（GitHub Actions 风格）：key 是事件名，value 是该事件的过滤参数（空 mapping `{}` 表示「无过滤」）。没有 wildcard，未识别的 key 直接报错。可用事件：
  - `issue.opened` / `issue.closed` —— 无参数。
  - `issue.comment` —— 单一事件覆盖原 `comment.any` / `comment.mentioned` 两路。过滤参数：
    - `mentioned_only: true` —— 仅当本 role 被 `@agent-<key>` 提及时唤醒。
    - `from_roles: [<role-key>, ...]` —— 仅响应来自这些 agent role 的评论（用于 agent 间手势接力）。
    - `from_users: [<username>, ...]` —— 仅响应来自指定人类账号的评论。
    - 三者 AND 组合；全部省略时每条评论都唤醒。
  - `commit.pushed` —— 过滤参数：
    - `paths: [<glob>, ...]` —— 改动至少有一个文件命中任一 glob 时才唤醒。空 = 不限制。`*` 不跨 `/`，`**` 跨。
    - `paths_ignore: [<glob>, ...]` —— 改动里至少有一个文件**未被任何 ignore 模式覆盖**才唤醒（一次推送如果全部改动都在 ignore 列表里就不唤醒）。空 = 不限制。
    - 两个 list 都设置时取 AND。
  - `review_vote.posted` / `ci.status_changed` —— 无参数。
- **`can:`** —— 平台工具白名单。没在 `can:` 里的工具 `tools/list` 看不到、`tools/call` 返 `isError`。
- **`not:`** —— 平台工具黑名单。仅在 `can:` 留空时生效，语义是「除列出的工具外其它都可用」。`can:` 和 `not:` 同时给值时**白名单优先**，`not:` 被忽略；两者都为空则 fail-closed（无任何工具）。
- **`scope.paths:`** —— 软约束（写进 role 的初始 prompt 让 dispatcher 知道分派给谁），不在 pre-receive 强制。
- **`prompt:` 或 `prompt_file:`** —— role 的提示词。二选一（schema mutually exclusive）；`prompt_file:` 必须以 `.hangrix/prompts/` 开头，文件随仓库一起进 git。**没有 host addendum 的概念了** —— 直接写 role 自己的完整 prompt 即可。
- **`llm:`** —— team 级 + per-role 两层，**按字段合并**：role 写了哪个字段就覆盖哪个字段，没写的字段继承 team；team 没设的字段走 platform default（即 adapter / upstream 的内置默认）。字段：
  - `model` —— team 级必填，必须命中某 provider 的 `allowed_models`；role 级可省略（= 继承 team）。
  - `reasoning_effort` —— 思考强度，任意字符串（parser 不校验枚举，新模型可直接填新值）。规范值 `minimal | low | medium | high` 走内置翻译：`openai-compat` 原样透传；`anthropic` 翻成 `thinking.budget_tokens`（minimal/low → 1024、medium → 4096、high → 16384，同时 drop temperature、bump max_output_tokens 防 400）。其它非空字符串一律透传，上游自行决定接受或拒绝。`anthropic` 在非规范值下不启用 thinking（避免猜测的预算超 `max_tokens`）。空字符串等同省略。
  - `max_context_tokens` —— Agent 打包 prompt+对话历史时的上限（>= 0，0 = 不约束）。LLM proxy 不强制；由 agent runtime 在送进上游前裁剪。
  - `max_output_tokens` —— 单次 completion 的输出预算（>= 0，0 = 上游默认）。Anthropic 必填 `max_tokens` 由 adapter 兜底到 4096。
  - `temperature` / `top_p` —— 采样旋钮，分别落在 `[0, 2]` / `[0, 1]` 内。零值是合法显式值（确定性解码 / 不做 top_p 截断），跟"字段省略 = 继承"是两件事——内部用指针区分，写了 `temperature: 0` 就是真的把 team 的非零默认覆盖掉。
  Spawn session 时把 host.LLM 和 role.LLM 按字段 merge 出 resolved 视图缓存到 session 元数据，runner 注入 env 时直接读。所以只想改 `model` 而保留 team 的 `max_context_tokens` / `reasoning_effort`，role 里只写一行 `model: …` 就行，不必复制整块。
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
- 评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块与引用块。匹配到的 role key 列表跟随 `issue.comment` 事件一起进 spawner；spawner 对每个订阅 `issue.comment` 的 role 计算它的 CommentFilter（`mentioned_only` 用本 role 是否在 mention 列表里来判定），命中即唤醒。没有额外的 actor-class 网关 —— 任何能写评论的人（读权限已经在评论入口校验）都可以唤醒任何 role。
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
- **归档由 `issue.closed` / `issue.merged` 触发**：该 issue 上全部 session 同步 `archived`，容器回收。admin 停某 role 的力度仍是「host yaml 删 role」或「平台禁用整张 yaml」，而不是逐 session 戳；containered session 走 Delete 也会落到 `archived`（容器需要异步清理时）。已归档行不重启 —— 它就是终态审计快照；但同一 issue 上后续触发该 role 时，spawner **新开一行替代**，归档行保留在历史里。
- **单 role 单容器串行（v1）：** 同 role 在同 issue 上同一时刻只跑一个容器，多 trigger 排队消化。多并发后续 milestone。
- **冲突自治：** 多 role 同时在 `issue/<n>` 上工作时，agent push 前自行 `git pull --rebase`。force-push 已禁，push 失败自然逼迫先 rebase —— 无需平台层协调机制。
