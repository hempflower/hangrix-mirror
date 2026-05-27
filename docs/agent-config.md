# Host 仓库 agents.yml schema

[← ROADMAP](../ROADMAP.md) · [JSON Schema](./agents.schema.json)

每个 host 仓库通过 `.hangrix/` 声明 team 行为。配置分两层：**`.hangrix/agents.yml`** 放 team 级共享配置（容器环境、LLM 默认、review gate、可复用的 `tools:` 工具规则），**`.hangrix/agents/<role-key>.md`** 每个文件定义一个 role —— YAML front matter 配触发器 / 权限 / 工具规则 / llm，Markdown 正文就是该 role 的 prompt。**所有 agent 配置都在 host 仓库内部**；版本固定 = host 仓库的 commit sha；没有第二个仓库或 lock 文件需要追踪。

> 落实原则 7：host 仓库说自己用什么环境跑 agent。没有独立的「agent 仓库」概念。一个 role 一个文件 —— agent 多了 `agents.yml` 也不会膨胀；跨 host 仓库复用 role 直接复制那个 `.md` 文件。

---

## 仓库布局

```
host-repo/
├── .hangrix/
│   ├── agents.yml          # team 级：container / llm / reviewers / tools 规则
│   └── agents/             # 每个 role 一个文件，文件名 = role key
│       ├── dispatcher.md   #   front matter（配置）+ 正文（prompt）
│       ├── backend.md
│       └── reviewer.md
└── ...your code...
```

`.hangrix/agents.yml`（team 级，**不含 roles**）：

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

  env:                            # 明文环境变量，入 git；value 支持 ${VAR_NAME} 引用仓库变量/机密（见下文）
    NODE_ENV: development
    GOFLAGS: "-mod=readonly"
    OPENAI_API_KEY: ${OPENAI_API_KEY}

  volumes:                        # repo-scope 共享缓存（runner 本地 bind mount）
    - { name: pnpm-store, mount: /caches/pnpm }
    - { name: go-mod, mount: /go/pkg/mod }

llm:                              # team 默认 LLM（可选；省略走 admin 配的 platform default）
  model: claude-sonnet-4-6        # 路由由 provider.allowed_models 反查决定
  reasoning_effort: medium        # 思考强度：minimal / low / medium / high 走内置翻译，其它字符串原样透传给上游；省略走上游默认
  max_context_tokens: 200000      # 最大上下文 token（agent 端 prompt+历史的上限）；0 = 不约束
  max_output_tokens: 8000         # 最大输出 token（单次调用 completion 上限）；0 = 上游默认

tools:                            # 可复用工具规则：名字 → 平台工具名 glob 白名单
  all: ["*"]                      # `*` 匹配全部平台工具（编排者用）
  worker:                         # 干活的角色：读 + 评论 + 创建/编辑 issue + 管自己的贡献分支
    - issue_read
    - issue_comment
    - issue_create
    - issue_edit
    - contribution_*              # 通配：contribution_list/read/set_meta/close 等
    - roster_list
  reviewer:                       # 审查者：读 + 评论 + 投票 + 问卷
    - issue_read
    - issue_comment
    - issue_review_vote
    - contribution_list
    - contribution_read
    - roster_list
    - ask_question
    - check_questionnaire
    - close_questionnaire
```

`.hangrix/agents/backend.md`（一个 role；文件名 `backend` 就是 role key）：

```markdown
---
triggers:
  issue.comment:
    mentioned_only: true          # 只在被 @agent-backend 时唤醒
scope: { paths: ["apps/api/**", "internal/**"] }
mcp: [playwright]                 # 可选：需要浏览器自动化时声明
permission: write                 # 服务端边界：可调用写端点
tools: [worker]                   # 引用 agents.yml 的 worker 规则（平台工具白名单）
llm:                              # 可选 per-role 覆盖
  model: claude-opus-4-7
  reasoning_effort: high
---
You write Go backend code in apps/api/** and internal/**.
Push your work to a contribution branch (issue-<N>/backend/<slug>);
the server opens a merge request for review. Cross-module imports
MUST go through pkg/ioc DI.
```

正文（front matter 之后的全部内容）就是该 role 的 system prompt。`permission` 是服务端强制的读/写边界；`tools` 引用的规则决定 LLM 能看到哪些**平台**工具（本地工具 read/write/edit/glob/grep/bash/webfetch 永远可用，不受 `tools` 限制）。

### 字段语义

### `issues:` —— Issue 行为开关

可选的顶层配置块，控制 issue 生命周期中的平台行为。

- **`delete_branch_on_merge:`** —— bool，默认 **`true`**。当 `true` 时，issue 合并成功后自动删除对应的 `issue/<n>` 分支。删除遵守 branch protection 规则：若命中 `forbid_delete` 则保留分支并在 merge 响应中报告原因 `"protected"`；若 guard 拒绝则报告 `"denied"`；其它失败报告 `"delete_failed"`。设为 `false` 时合并后保留分支（旧行为）。

```yaml
# 示例：关闭自动删分支
issues:
  delete_branch_on_merge: false
```

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
  - `commit.pushed` —— 过滤参数（**人类 push**；agent 默认不直接 push，走 patch 流程）：
    - `paths: [<glob>, ...]` —— 改动至少有一个文件命中任一 glob 时才唤醒。空 = 不限制。`*` 不跨 `/`，`**` 跨。
    - `paths_ignore: [<glob>, ...]` —— 改动里至少有一个文件**未被任何 ignore 模式覆盖**才唤醒（一次推送如果全部改动都在 ignore 列表里就不唤醒）。空 = 不限制。
    - 两个 list 都设置时取 AND。
  - `patch.submitted` —— 过滤参数（agent 提交 patch 时触发；取代 agent 侧的 `commit.pushed`）。与 `commit.pushed` 共用相同的 PushFilter 模型：
    - `paths: [<glob>, ...]` / `paths_ignore: [<glob>, ...]` —— 语义同上，但匹配的是 patch 解析出的 `changed_paths` 而非 git 提交差异。
    - maintainer 后续 apply patch 时**不会**再伪造一次 `commit.pushed`，避免 reviewer/tester 被重复唤醒。
  - `review_vote.posted` / `ci.status_changed` —— 无参数。
- **`permission:`** —— 仓库权限级别（GitHub 式），服务端在平台 v1 REST API 上强制。`read` 只放行只读端点（GET issue / comment / todo / contribution / mergeability / sessions）；`write` 额外放行所有写操作（评论、编辑、合并、关闭、投票、todo 更新、contribution apply/close/set_meta、release 操作、附件上传、session 恢复、创建 issue）。省略时默认 `read`（fail-safe：忘写不会误授写权）。这是**唯一的服务端访问边界**——粗粒度、可审计、像 GitHub 的 collaborator read/write。
- **`tools:`**（agent front matter）—— 该 role 引用的工具规则名列表（来自 agents.yml 的 `tools:` 映射）。role 能看到的**平台**工具 = 所引用规则的 glob 模式并集；其余平台工具从 LLM tool schema 中隐藏。**由 agent 侧强制（schema 隐藏），服务端不校验**——服务端只认 `permission`。所以 `tools` 塑形「这个角色该用哪些平台工具」，`permission` 才是安全边界。省略 = 不给任何平台工具。**本地工具（read/write/edit/glob/grep/bash/webfetch）永远可用，不受 `tools` 限制。** 引用了未定义的规则名时加载失败。
- **`tools:`**（agents.yml team 级，见上例）—— 可复用规则映射：规则名 → 平台工具名 glob 白名单（`*` 通配，如 `issue_*`、`contribution_*`、`*`）。**仅平台工具、仅白名单。** agent 多了用规则避免每个文件重复罗列工具；多个 role 共享同一规则即可。
- **`mcp:`** —— 可选，MCP 服务器白名单。字符串数组，每项对应仓库根目录 `.mcp.json` 中 `mcpServers` 的一个 key。**缺失或空数组时该 role 不加载任何 MCP 服务器**；非空时仅加载列出的服务器。引用了 `.mcp.json` 中不存在的 server 名时 session 明确失败并报告缺失的 server 名。`mcp:` 是独立的 MCP 白名单，不复用 `tools` 的平台工具规则。
- **`scope.paths:`** —— 软约束（写进 role 的初始 prompt 让 dispatcher 知道分派给谁），不在 pre-receive 强制。
- **prompt（正文）** —— role 的提示词就是 `.hangrix/agents/<role>.md` 中 front matter 之后的 Markdown 正文。正文不能为空。跨 host 仓库复用某个 role，直接复制它的 `.md` 文件即可。
- **`llm:`** —— team 级 + per-role 两层，**按字段合并**：role 写了哪个字段就覆盖哪个字段，没写的字段继承 team；team 没设的字段走 platform default（即 adapter / upstream 的内置默认）。字段：
  - `model` —— team 级必填，必须命中某 provider 的 `allowed_models`；role 级可省略（= 继承 team）。
  - `reasoning_effort` —— 思考强度，任意字符串（parser 不校验枚举，新模型可直接填新值）。规范值 `minimal | low | medium | high` 走内置翻译：`openai-compat` 原样透传；`anthropic` 翻成 `thinking.budget_tokens`（minimal/low → 1024、medium → 4096、high → 16384，同时 drop temperature、bump max_output_tokens 防 400）。其它非空字符串一律透传，上游自行决定接受或拒绝。`anthropic` 在非规范值下不启用 thinking（避免猜测的预算超 `max_tokens`）。空字符串等同省略。
  - `max_context_tokens` —— Agent 打包 prompt+对话历史时的上限（>= 0，0 = 不约束）。LLM proxy 不强制；由 agent runtime 在送进上游前裁剪。
  - `max_output_tokens` —— 单次 completion 的输出预算（>= 0，0 = 上游默认）。Anthropic 必填 `max_tokens` 由 adapter 兜底到 4096。
  - `temperature` / `top_p` —— 采样旋钮，分别落在 `[0, 2]` / `[0, 1]` 内。零值是合法显式值（确定性解码 / 不做 top_p 截断），跟"字段省略 = 继承"是两件事——内部用指针区分，写了 `temperature: 0` 就是真的把 team 的非零默认覆盖掉。
  Spawn session 时把 host.LLM 和 role.LLM 按字段 merge 出 resolved 视图缓存到 session 元数据，runner 注入 env 时直接读。所以只想改 `model` 而保留 team 的 `max_context_tokens` / `reasoning_effort`，role 里只写一行 `model: …` 就行，不必复制整块。
- **Runner 默认注入：** 给每个 role 容器注入一张统一的 session token（[agent-identity.md](agent-identity.md)），LLM endpoint + model 也是默认注入。

### Schema 强约束

- `version: 1` 必填。
- 至少有一个 role 文件（`.hangrix/agents/<key>.md`）；否则加载报错。role key = 文件名（去掉 `.md`），限制 `[a-z][a-z0-9-]*`，**预留 `agent-` 前缀给 mention 协议**（参见下文）。
- 每个 role 文件必须有合法 YAML front matter（`---` 围栏）+ 非空正文。
- role front matter 的 `tools:` 引用的规则名必须在 agents.yml 的 `tools:` 里定义。
- `permission:` 只能是 `read` / `write`（省略默认 `read`）。
- `container.image` / `container.build` 二选一互斥。

## 仓库变量与机密变量

`container.env` 的 value 支持 `${VAR_NAME}` 整值引用（如 `OPENAI_API_KEY: ${OPENAI_API_KEY}`），引用的是仓库级别的 **变量**（明文存储）和 **机密变量**（加密存储）。`agentsconfig` 解析器将 `${...}` 当作普通字符串保留原样；实际的变量解析与展开由 Runner 在容器启动前完成。

- **管理入口：** 仓库设置页 → 「变量与机密」tab（仅 `manage` 权限可见）。
- **API：** `GET/POST /api/repos/{owner}/{name}/variables` 列表/创建；`PATCH/DELETE /api/repos/{owner}/{name}/variables/{name}` 更新/删除。
- **机密回显：** 创建机密时输入明文；保存后列表只显示「已设置」状态，不再回显旧值。更新时输入新值覆盖，留空则保持原值不变。
- **展开失败：** Runner 在 session 启动时若 `container.env` 引用了不存在的变量名，session 明确失败并返回缺失的变量名（不静默注入空值）。
- **展开范围：** 仅整值 `${NAME}` 替换；`prefix-${NAME}`、`${A}-${B}`、`${VAR:-default}` 等复合语法不做展开，原样保留。
- **`container.secrets` 已移除：** 旧的 `secrets:` 数组字段已从 schema、解析器和默认模板中移除。如有旧配置使用了 `secrets:`，请将值迁移到仓库设置页的「变量与机密」中，并在 `container.env` 中通过 `${VAR_NAME}` 引用。


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
[role prompt]         ← `.hangrix/agents/<role>.md` front matter 之后的正文
```

> 历史注记：v1 之前的设计是三层（baseline → agent 仓库 base_prompt → host addendum），随 agent-as-repo 一并取消。想跨 host 仓库复用 prompt 直接复制 markdown 文件即可。

---

## Identity 与 Audit

- commit author name = role key（如 `backend`），email = `<role-key>@agents.<host-domain>`。
- 每次 session 启动落一份版本信息进 audit：`repo_sha` = host 仓库 base 分支当时的 commit（含 `.hangrix/agents.yml` + `.hangrix/agents/`）。
- 任何 commit / merge 都能 trace 回 cause `comment_id` —— M4 时间线 append-only 审计流的覆盖面延伸到 agent 全部动作。**按 `repo_sha` checkout 即可精确复现 agent 当时看到的整套 prompt + 工具集 + 代码状态**，无需第二个仓库的对位 checkout。

---

## Session 模型（一 issue 多 role）

- **`modules/agent_session`：** 一个 issue 内每个被唤醒过的 role 各一个 session（取代原 "1 issue 1 session"）。
- 字段：issue id、role key、`repo_sha`、runner id、container id、状态（`pending | running | idle | archived | failed`）+ 解析后的 role 配置 snapshot（见下条）。
- **冻结点 = session spawn 那一刻。** 第一次唤醒某 role 时，按当时 host 仓库 base 分支的 commit 算 `repo_sha`，把解析后的 role 配置（prompt 正文 / `permission` 级别 / `tool_patterns` 解析后的平台工具白名单 / resolved llm / container spec）一并 snapshot 进 session 元数据。**整 session 生命周期不再重读 host yaml** —— host yaml 中途改了不影响在跑的 session。同 issue 不同 role 各自冻结自己的 `repo_sha`；中途新加的 role 在它第一次被唤醒时拍自己的照。这个约束是 audit 可重现性的支点。
- 配套 `agent_session_messages` 存完整对话历史 —— OpenAI Responses-API 风格消息序列（user 事件 / assistant 消息 / tool call + result / 系统事件混排），按 `created_at` 排序；session 归档时只标记不删。
- **归档由 `issue.closed` / `issue.merged` 触发**：该 issue 上全部 session 同步 `archived`，容器回收。admin 停某 role 的力度仍是「host yaml 删 role」或「平台禁用整张 yaml」，而不是逐 session 戳；containered session 走 Delete 也会落到 `archived`（容器需要异步清理时）。已归档行不重启 —— 它就是终态审计快照；但同一 issue 上后续触发该 role 时，spawner **新开一行替代**，归档行保留在历史里。
- **单 role 单容器串行（v1）：** 同 role 在同 issue 上同一时刻只跑一个容器，多 trigger 排队消化。多并发后续 milestone。
- **冲突自治（patch-first）：** Agent 不再直接 push 到 `issue/<n>`；改为提交 patch，由 maintainer 审核后 apply。并行冲突从"push 时冲突"前移为"apply 前判定 base_head_sha 是否匹配"——stale patch（base 落后于当前 issue head）会被明确标记且不能 apply，作者需基于最新 head 重新生成。这避免了多 agent 同时 rebase 同一分支的竞态，也让 reviewer 能按单次贡献粒度审阅 diff。
