# ROADMAP

## 定位

Hangrix 是一个 **AI-Native 的 git 平台**。

不是"在传统 git 平台上加个 AI 助手"，而是反过来：把 AI agent 当作平台的一等公民，所有 git 能力都围绕 agent 协作的工作流来设计。

**产品形态的关键决定：Issue 是用户的主入口，也是工作流的唯一容器。**

我们刻意**不做独立的 PR / review 模块**。在 Hangrix 里：

- 用户绝大多数时间面对的是 issue —— 在 issue 里提需求、问问题、汇报 bug。
- 每个 issue 是三位一体：**一段对话 + 一条 git 分支 + 一个 agent 会话**。
- Agent 在 issue 内响应：理解需求、在该 issue 的分支上写代码、把改动以评论形式回流到 issue。
- "Code review" 不是独立页面，而是 issue 里对 agent 产出的讨论；"merge" 是 issue 的一个状态转移，不是另一个实体。

这等价于把 GitHub 的 Issue + PR + Review + Discussion 折叠成同一个东西，因为在 agent 协作语境下，这些原本就是同一件事的不同切面。

## 设计原则

1. **Issue 是工作流的最小单位。** 任何代码变更都必须有一个 issue 承载它。没有"游离的分支"或"无 issue 的 PR"。这给了 agent 一个稳定的上下文锚点。
2. **每个 git 实体都有 agent-friendly 表达。** 仓库、commit、diff、issue 既要给人看的 UI，也要给 agent 用的结构化形式（流式 diff、语义化摘要、可寻址的 anchor）。
3. **agent 是 first-class identity，不是 webhook。** agent 有账号、有权限、有 audit log；它的提交、评论与人类账号同形。但 **agent identity 与 user 是不同实体**——用户表只代表人类，agent 走独立的模型（见 M7 的 agent-as-repo）。
4. **平台能力以工具暴露，git 能力以 CLI 暴露。** 平台动作（issue 评论 / 看 diff / merge）走明确的工具集，agent 通过 MCP-style 调用；仓库读写直接用容器里的 `git` CLI —— 不把 git 包成一层平台工具，也不依赖把整个仓库塞进上下文。
5. **可见 / 可停 / 可 revert，不做事前门禁。** agent 所有写操作（commit / merge / 评论）落审计，能快速 revert；admin / repo owner 能一键停某 agent 或关闭仓库的自动 session。不做"diff 行数限制 / 文件白名单 / 先批准再做"这类事前门禁 —— 安全靠事后约束。
6. **本地优先的形态。** 当前以单二进制 + 嵌入式 SPA 形态运行；多租户/SaaS 形态是后续选项，不是前提。
7. **Agent 运行环境由 host 仓库定义，agent 不可自决。** 同一 agent 在 Go 项目和 Node 项目跑的环境不同 —— 这是 host 该说的话。Agent 仓库（M7 的 agent-as-repo）只声明 prompt + 工具偏好，不声明镜像 / 包 / 解释器版本；host 仓库通过 `.hangrix/agents.yml` 声明容器（image 或 build）+ env + secrets + volumes。Agent 跨仓库可复用，host 各自管自己的工具链。

## 当前状态（M6c 完成 + Agent 身份重构）

**M6c 全部闭环 + agent 身份模型重构。** Agent 链路第三段立起来：独立的 `hangrix-runner` 二进制以「outbound-only HTTP 长轮询」连回服务端，按 session 拉容器、bind-mount agent binary、转发 stdin/stdout。**最近一轮重构把 session token 从「LLM 路由附属物」剥离成「agent 身份令牌」**：现在一张 `hgxs_<...>` 同时用于 LLM proxy / 平台 MCP / 未来 git push，token 一对一挂在 `agent_sessions` 行上，与 LLM provider 解耦。LLM proxy 端点合并成单一 `/api/llm/v1/responses`，provider 路由由请求体 `model` 字段反查 `allowed_models` 决定。Provider 字段裁剪到只剩 name/type/base_url/api_key/allowed_models。详见 [docs/agent-identity.md](docs/agent-identity.md)。

服务端 `modules/runner`（四张表 + admin/agent 两套 HTTP 表面 + enroll/agent/session 三类 token）和 `modules/llm_provider`（provider registry + usage log）都改走 **sqlc 生成查询**（`llmproviderdb` / `runnerdb`），手写 SQL 已退场。Token 校验（stateless：`hgxr_` 和 `hgxs_`）从 infra 搬到 `modules/runner/service`：repo 只做行查询，service 包 bcrypt + active 检查，符合「persistence 只负责数据访问，service 负责业务编排」。Enrollment 因为是 stateful 状态转移（`FOR UPDATE` + 一次性 redemption）仍住在 infra。

服务端把 `hangrix-agent` 和 `hangrix-runner` 两个二进制 `//go:embed` 进自身（`internal/modules/runner/binaries`，`payload/*` 由 `scripts/build-embed-binaries.mjs` 预编译，`.gitignore`d，启动期 SHA256 缓存），通过 `GET /api/runner/binaries` / `/api/runner/binaries/{name}` 流式分发 + `GET /api/runner/bootstrap` 一次性下发 endpoints/image/binary 元数据。运行时配置只剩 `server.url` + `runner.default_agent_image`，**「`enroll` 一次性下发，`serve` 零必填参数」** 的契约由 runner 端 `state.json`（`~/.hangrix/state.json`）+ 内容寻址缓存 `~/.hangrix/agent-binaries/<sha256>/` 兜住。

退出条件由 `apps/hangrix-runner/internal/loop/session_test.go` 端到端验证（FakeOrchestrator + httptest.Server，5 秒内闭环）。Runner 实现细节参见 [docs/runner-protocol.md](docs/runner-protocol.md)。下一步 **M7a（多 role 基础设施）** 把 host yaml + agent-as-repo + role-key 接到这个 runner 调度层上。

已就绪：
- **脚手架（M0）**：Go 1.26 + Nuxt 4 单二进制；`pkg/ioc` DI；chi、viper、air、Turborepo。
- **账号基础设施（M1）**：用户 / 角色 / 会话 / admin 后台。
- **Git 内核（M2）**：`modules/git`（go-git 读封装）+ `modules/repo`（元数据 + bare repo）+ smart HTTP `git-upload-pack`。
- **Git 平台（M3 核心）**：`modules/token` PAT + `git-receive-pack` 写路径 + 分支 / Tag CRUD + 仓库设置 + Compare + README 渲染。`resolveRef` 透明 peel annotated tag。
- **协作辅助（M3 stretch）**：`branch_protections` 表 + `pre-receive` 钩子（force-push / delete 拦截）+ commit 包含查询 + archive 下载（zip / tar.gz）。
- **Issue 容器（M4）**：`modules/issue` 完整模块 —— Issue / Comment / Event 三张表 + `issue_counters` per-repo 单调编号 + sub-issue（parent_id / parent_number）。Issue API：list / create / patch / merge / sync / timeline / diff / commits / children + 评论 create / list（**评论删除已撤回**——issue 时间线只追加不删除，删除按钮和后端路由都已移除）。**写入收紧**：`repodomain.BranchWriteGuard` + `PushObserver` 两个跨模块接口，issue 模块挂上去；`hangrix-issue-mode` sidecar 同步开放 issue 编号给 pre-receive 钩子，钩子里 base 锁定 + `issue/<n>` 校验双线生效；web API 的 `createBranch` / `deleteBranch` / receive-pack 也都跑同一份 guard。`MergeBranch` 三方合并实现进 `modules/git`（FF / merge-commit / up-to-date 三态 + 冲突哨兵 `ErrMergeConflict`）。前端：Issues 列表 / 详情 / 新建（含 `?parent=N` 子 issue 入口）；详情页 conversation + commits + diff 三 tab（tab 状态写进 `?tab=` URL 可分享 / 可回退）、GitHub 风格评论卡（avatar header strip + 相对时间 + tooltip 显示绝对时间戳）、评论 / 系统事件混排时间线、15s 自动刷新（hidden tab 自动暂停 —— 取代了原来的"手动 Sync 按钮"）、合并按钮、parent / children 侧栏 + 「Changes」(+N −M / files changed) 卡。FileDiffList 重写成 GitHub 风格：行号 gutter + emerald/red 行底 + sky hunk header + 折叠 + 每个文件 +N −M 徽章 + "view before / view after" blob 链接（commit 详情页和 issue diff tab 共用）。
- **组织（M5）**：`modules/org` 完整模块 —— `organizations` + `organization_members` 两张表 + 跨模块 `Resolver`（`ResolveOwner` / `Membership`）。`modules/repo` 重构成 owner_kind/owner_id 二元归属（user 或 org，DB CHECK 保证恰一；UNIQUE 拆 partial 索引按 kind 限定 name 唯一）。`POST /api/orgs` / 成员管理 / `POST /api/repos/{owner}/{name}/transfer` 全部就绪；transfer 走 DB swap + 磁盘 rename 的"先 DB 后磁盘 + 失败回滚 DB"策略。保留名（`admin` / `api` / `git` / `static` / `_` 等）+ 跨表 namespace 互斥校验（创建 user 撞 org 名 / 反之都返 409）。前端：导航栏「New organization」入口、`/orgs/new` 表单、`/{name}` 统一 profile 页（同一个 `[owner]/index.vue` 通过 `ResolveOwner` 渲染 user 或 org 视图）、`/{name}/settings` + `/{name}/settings/members`、新建仓库表单的 Owner 下拉、`/{owner}/{name}/settings` 的 Transfer 弹窗（type-to-confirm `<owner>/<name>`）。**Org visibility 字段在 ship 前主动撤回**：原计划 public / private 两档，落地后判断「私有 org 给非成员看什么」始终拐不出有意义的语义，干脆删列 + 删 UI，所有 org 一律登录可见，私密性靠仓库 visibility 兜底（见后文"计划外"）。
- **LLM proxy（M6a，**重构后形态**）**：`modules/llm_provider`（registry + usage log）+ `modules/llm_proxy`（HTTP 代理）。Provider 字段只有 name / type / base_url / api_key / allowed_models；api_key 走 `pkg/cryptobox`（AES-256-GCM，master key 来自 `config.llm.encryption_key`）落盘加密。代理端点合并为单一 `POST /api/llm/v1/responses`（OpenAI Responses-API 兼容、非流式）：Bearer `hgxs_` → 校验 → 用请求体 `model` 反查 `allowed_models` 命中的 provider（lowest-id 优先）→ 解密 api_key → 按 `provider.type` 分发（`openai` Responses 原生 / `openai-compat` Chat-Completions 翻译 / `anthropic` Messages 翻译）→ 落 `llm_usage_log`。Provider 路由的细节 + adapter 翻译规则参见 [docs/llm-proxy.md](docs/llm-proxy.md)。
- **Agent runtime（M6b）**：新 module `apps/hangrix-agent` —— Go 二进制（**无 third-party agent framework 依赖**；唯一非 stdlib 库是 `JohannesKaufmann/html-to-markdown/v2`，给 `webfetch` 用），跑在容器里跟 M6a LLM proxy + 平台 MCP server + 容器内 `git` CLI 三方说话。`internal/llm` 手写 OpenAI Response API 非流式客户端（`message` / `function_call` / `function_call_output` 三类 input item + 指数退避重试 + 4xx 不重试）；`internal/mcp` Streamable HTTP MCP client（`tools/list` + `tools/call`，JSON 一发一收 + 单帧 SSE 都解）；`internal/tools/local` 七件 Claude-Code-语义工具（`read` 行号 prefix + `write` 默认拒覆盖 + `edit` 三模式 + `ReadTracker` read-before-edit 守卫 + `glob` 支持 `**` mtime 倒序 + `grep` 优先 ripgrep + `bash` 走 `bash -c`（**不是 `sh -c`**）、前台 / 后台 task_id 轮询 / 超时、task_id 与 command 互斥强校验 + `webfetch` HTML → Markdown 4 MiB 上限）；`internal/tools` registry 按 `HANGRIX_TOOL_CATALOG` JSON 数组过滤本地 + 远端工具，未知工具名返错误结果让 LLM 自我修正；`internal/prompt` 三层 prompt 拼装（runtime KV 块 + `//go:embed` baseline.md + agent bundle `entry.base_prompt` + `HANGRIX_HOST_ADDENDUM`，bundle 误配走错而非静默跌回）；`internal/runtime` 主循环（首帧必须 `kind:history`、单轮 LLM⇄tool round-trip 上限 16、尾窗口裁剪 60 条）；`internal/ipc` JSON-Lines stdin/stdout（16 MiB scanner buffer 给完整 history、stdout mutex 防并发交错）；**`cmd/hangrix-agent/main` 走 `pkg/ioc` 装配** —— `buildContainer()` 加载 8 个 module（`config` / `llm` / `mcp` / `tools` / `prompt` / `ipc` / `runtime` / `app`） + `ioc.Get[*app.App](c).Run(ctx)` + provider 内 panic 由 `recover` 翻成单行 stderr + exit 1 维持失败形状不变；`internal/app/App.Run` 收 `signal.NotifyContext(SIGTERM, SIGINT)` graceful shutdown。退出条件由 `internal/runtime/loop_test.go` 端到端验证：mock LLM 按调用次数返 tool call → final message + mock MCP 出 `stub.ping` + 真本地 `read` + `io.Pipe` 喂 IPC，断言一轮 round-trip 同时跑了一个本地工具 + 一个 MCP 工具；`cmd/hangrix-agent/wiring_test.go` 在 CI 上钉住 ioc 依赖图。Ship 后又一轮运行时打磨（system prompt 按 RFC 2119 重写、工具错误改「what / why / how」、branch 策略放宽、env 名清出 prompt body、`pkg/` → `internal/` + ioc 装配等）详见 M6b 末尾「后续打磨」小节。
- **数据库迁移系统**：`goose v3` 库模式 + 每模块独立 `goose_<module>` 版本表，启动时 sequential 应用。
- **前端基础**：shadcn-vue + Tailwind v4 + 5 套布局矩阵；vee-validate + zod + 全局 i18n errorMap；中英双语；独立 Admin Sidebar。新增组件 `dialog` / `textarea` 给 PAT / 设置 / 分支 / Tag / Compare / Issue 表单复用；`marked` + `dompurify` 给 README 渲染。M4 接入 RepoSidebar Issues 入口（即使空仓库也可见）；M5 接入 AppSidebar 的 Organizations section（动态列出 `useMyOrgs` 拉到的组织 + 「New organization」固定项）。面包屑机制从 AppHeader 的 130 行 `route.path` 大 switch 重构成 `composables/useBreadcrumbs` + 每页 `setBreadcrumbs(supplier)` —— supplier 包在 `watchEffect` 里跟随 locale / params / fetched 数据自动重算；未迁移页面会在 header 显示原始路径作为告警。

## 里程碑

### M1 — 账号与权限基础设施 ✅

先把"人"立起来——后面所有 git/agent 能力都挂在用户身份上，先没有用户就没法谈权限、审计、agent identity。

**权限模型刻意简单：只分 `user`（普通用户）和 `admin`（管理员）两级。** 仓库级、组织级权限延后到真正需要时再加。

- [x] `modules/user`：用户实体（id、username、email、password_hash、role、disabled、created_at、updated_at）。`role` 是 `user | admin`，默认 `user`。**实现选择修订**：DB 落 **Postgres 17**（不是 SQLite——pgx + sqlc 的组合在类型安全和扩展性上更划算）；密码 hash 用 bcrypt。
- [x] `modules/auth`：注册 / 登录 / 登出 / 当前用户。
  - `POST /api/auth/register`、`POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/me`
  - 会话用服务端 session：cookie + **Redis** KV（`session:<token>` 主键 + `user_sessions:<id>` 反查索引，便于禁用账号时强制下线全部会话）。不上 JWT。
  - 注册首个账号自动 `admin`（bootstrap）。
- [x] **中间件**：`RequireAuth` / `RequireAdmin`，挂在保护路由上。
- [x] `modules/user` 的用户接口：`GET /api/users/me`、`PATCH /api/users/me`（改邮箱 / 改密码——改密码校验旧密码）、`GET /api/users/{id}`（公开字段）。
- [x] `modules/user` 的管理员接口（全部 `RequireAdmin`）：`GET /api/admin/users` 列表、`PATCH /api/admin/users/{id}` 改角色与启用状态（服务端阻止 admin 修改自己）。**软删除**走 `disabled = true`，不真删行。
- [x] 前端页面：登录、注册、个人资料、admin 用户管理。
- [x] Nuxt 侧 auth：`useCurrentUser()` 暴露当前用户；全局 middleware 拦未登录访问；admin-only 路由再校验 role。

**计划外但已经做了的事**：
- 数据库迁移系统（goose v3 库模式，per-module 版本表）。M1 启动时还在用 `Exec(schemaSQL)` 单文件 bootstrap，收尾时换成正式迁移管线。
- 前端 5 套布局矩阵（详见 [docs/tech-stack.md](docs/tech-stack.md) 的"布局矩阵"一节）。
- Admin 后台独立的 `AdminSidebar` + amber 配色 + 「返回工作区」分离按钮——让 admin 视觉上始终知道自己在管控视图。
- 全局 zod errorMap：表单校验文案不在 schema 里硬编码，统一走 `validation.*` i18n 键。

**计划里删掉的事**：
- 原计划在 user 表里加 `kind = human | agent` 字段（当时为 agent 那一步铺路），已删除。决定改为"users 只代表人类，agent identity 走独立路径"（M7 落到 agent-as-repo + role key）——避免账号系统在 password / 邮箱 / 登录等地方对人和 agent 拧着说。

**退出条件（已通过）**：
1. 启动空库 → 注册第一个账号自动 admin → 登录 → 改资料 → 注册第二个账号是普通用户 → admin 能在管理页禁用第二个用户 → 被禁用的用户登录失败。✅
2. 所有非公开接口未登录时返回 401；非管理员访问 admin 接口返回 403。✅
3. 整条链在浏览器里跑通。✅

### M2 — Git 内核（read-only） ✅

让平台能"看"仓库。这是后面所有 agent 能力的基础。M1 的用户身份决定了谁能创建仓库、谁能浏览。

- [x] `modules/repo`：仓库元数据（owner_id、name、description、visibility `public | private`、default_branch、created/updated_at，`UNIQUE(owner_id, name)`）。Bare repo 落 `data/repos/<owner>/<name>.git`。私有仓库只有 owner 和 admin 可见；public 任何登录用户可见——visibility 在 handler 里显式判断，**不引入仓库级 ACL 表**。
- [x] `modules/git`：go-git 实现的读封装（接口在 `domain.Git`，是其他模块**唯一**能 import 的 git 入口）。能力：`Init` / `SeedReadme` / `ListRefs` / `ListCommits` / `CommitByID`（带 diff）/ `Tree` / `Blob` / `DiffRefs`。
- [x] 仓库管理 API：`POST /api/repos`（自己名下，可选 `init_readme` 让 git clone 立即可用，无须等 M3 push）、`DELETE`（owner 或 admin）、`GET /api/repos/me`、`GET /api/users/{username}/repos`、`GET|PATCH /api/repos/{owner}/{name}`。
- [x] Git 读 API：`/refs`、`/commits[?ref=&offset=&limit=]`、`/commits/{sha}`、`/tree[?ref=&path=]`、`/blob[?ref=&path=]`、`/diff?from=&to=`。
- [x] 前端：`/repos`（列表）、`/repos/new`（创建）、`/[owner]/[name]`（详情，Tabs：文件 + 提交记录）、`/[owner]/[name]/blob`（文件查看，Base64 解码 + 二进制下载）、`/[owner]/[name]/commits/[sha]`（commit + 文件级 diff）。
- [x] **Smart HTTP clone**：`GET /git/{owner}/{name}.git/info/refs?service=git-upload-pack` + `POST /git/{owner}/{name}.git/git-upload-pack`。shell-out 到系统 `git upload-pack --stateless-rpc`。Public repo 免认证，private repo 走 cookie 或 HTTP Basic（PAT 在 M3）。

**计划外但已经做了的事**：
- 给 `gitdomain` 的值类型加了 JSON snake_case tag——读 API 直接序列化 domain 值，不再写一层 DTO。
- 升级 `tailwind-merge@3` 修了之前 sidebar 折叠时 logo 被裁掉的 `p-2!`/`p-0!` 同 variant 冲突未去重 bug（v2 不认 Tailwind v4 的 `!`-后缀语法）。
- 装了 `tabs` / `select` / `radio-group` / `checkbox` 四个新 shadcn 组件供 repo 页面用。

**退出条件（已通过）**：登录用户在 UI 创建仓库（init_readme 勾上）→ `git clone http://.../{owner}/{name}.git` 拿到 README 和 initial commit → 在网页上看到文件树、commit 列表、单 commit diff。✅

### M3 — Git 平台（push / 分支 / Tag / 设置 / 协作辅助） ✅

把 M2 的"只读 git 形态"升级成完整的 git 协作平台：用户能 push、建分支、打 tag、改仓库设置、看 ref 对比、读 README。**这是 M4（Issue）之前最后一步通用 Git 形态**——之后所有写入都要挂在 issue 下，M3 是平台仍像 Gitea/GitHub 一样自由的最后窗口。

> **过渡说明**：M3 允许直接 push 任意分支、删任意分支、改默认分支——这是有意的过渡形态。M4 引入 issue 后会**收紧**：默认分支只能由 issue merge 推进，非 default 分支必须挂在某个 issue 下，直接 push 到游离分支会被拒。M3 写入路径上线时就要预埋"未来要拒绝"的 hook 点（一个 `BranchWriteGuard` 接口的空实现），不要让 M4 接入时挖一层抽象。

#### 核心（必做）

- [x] **HTTP smart push（receive-pack）**：`POST /git/{owner}/{name}.git/git-receive-pack` + `info/refs?service=git-receive-pack`。Shell-out 到系统 `git receive-pack --stateless-rpc`。Auth：cookie / HTTP Basic（PAT 或密码）；写操作要求 caller 通过认证且 PAT 必须有 `repo:write` scope。`gitInfoRefs` 同一入口按 `service` query param 分派到 upload-pack / receive-pack。
- [x] **Personal Access Token（PAT）**：`modules/token` 完整模块（domain + Postgres infra + handler + ioc）。token 行存 `prefix` 公共标识 + bcrypt(secret)；wire 格式 `hgx_<8>_<32>`（一次性返回明文，之后再不可恢复）。Scope 只有 `repo:read` 和 `repo:write` 两档，write 隐式包含 read。Profile 页有完整的 create / list / revoke UI，create 后弹窗一次性展示明文 + 复制按钮。识别 PAT 的策略：basic auth 的 password 字段以 `hgx_` 开头先试 token validator，否则 fallback 到 bcrypt 密码。
- [x] **分支管理**：API `POST /branches`（body `{name, start_ref}`）、`DELETE /branches/*`（chi wildcard 支持 `feature/x` 这种带 `/` 的分支名）。`PATCH /repos/{owner}/{name}` 接受 `default_branch` 切换并**同步写盘 HEAD**（`Git.SetHEAD`），先改盘再改 DB 保持一致；切到不存在的分支返回 400 且不改 DB。删当前 HEAD 分支返回 409 `cannot delete current HEAD branch`。UI：`/[owner]/[name]/branches` 列表页 + 新建对话框 + 设默认 + 删除确认（默认分支删按钮禁用）。
- [x] **Tag 管理**：API `POST /tags`（body `{name, ref, message?, annotated?: bool}`，annotated 时 message 必填）、`DELETE /tags/*`。Lightweight tag 直接 SetReference 指向 commit；annotated tag 先 encode `*object.Tag`（带 tagger=caller + message + commit target）写入 storer，再让 tag ref 指向 tag 对象 hash。UI：`/[owner]/[name]/tags` 列表 + 新建对话框（annotated 复选框联动 message textarea）+ 删除确认。
- [x] **仓库设置页**：`/[owner]/[name]/settings`，owner/admin 才可访问，否则自动 redirect 回仓库详情。「一般」表单（description / visibility radio / default_branch select）+「危险区」（删除仓库——type-to-confirm 对话框需要输入 `<owner>/<name>` 才能启用红色按钮；transfer ownership 占位按钮）。
- [x] **Compare 视图**：`/[owner]/[name]/compare?from=&to=` 复用 M2 的 `GET /diff?from=&to=`；两个 ref Select + 文件级 diff（复用 commit detail 的 `FileDiffList` 组件）。**Annotated tag 的 diff bug 修了**——`resolveRef` 现在透明 peel annotated tag 到底层 commit，所有内部调用方拿到的 hash 都直指 commit。
- [x] **README Markdown 渲染**：根目录命中 `README.{md,markdown,mdx}`（大小写不敏感）时，前端 fetch blob → `marked` 渲染（GFM）→ `DOMPurify` sanitize → 注入 Card。**只在仓库 root + Files tab 渲染**，binary blob 跳过。

#### 协作辅助（核心写完再做，不阻塞 M4）

- [x] **分支保护规则**：`modules/repo` 加 `branch_protections` 表（repo_id、pattern、forbid_force_push、forbid_delete、forbid_direct_push（owner 也得绕道——M3 阶段不强制，M4 默认启用））。Web 上：仓库设置页「分支保护」section（CRUD）。强制点：`git push` 走 bare repo 的 `pre-receive` 钩子 + `hangrix-protections` sidecar，禁止 force-push 与 delete；`DELETE /branches/*` API 也走同一份规则。default branch 仍由 `ErrCannotDeleteHEAD` 兜底（独立于规则表）。`forbid_direct_push` UI 上标 M4 标签，M3 不强制。
- [x] **分支包含查询**：API `GET /api/repos/{owner}/{name}/commits/{sha}/contains` → 返回包含该 commit 的分支 + tag 名列表（用 go-git 的 `IsAncestor` 遍历）。UI：commit 详情页「在以下 ref 中」小卡片。
- [x] **Archive 下载**：`GET /api/repos/{owner}/{name}/archive/{ref}.{zip|tar.gz}` shell-out `git archive`，文件名 `<repo>-<ref>.<ext>`。UI：仓库主页 ref 选择器旁的下载下拉。

#### 计划外但已经做了的事

- **`resolveRef` 透明 peel annotated tag**：在 git infra 这一层把 annotated tag 解到底层 commit，ListCommits / Tree / Blob / DiffRefs / CreateBranch 等所有内部消费者都受益，不需要各自写 peel 分支。
- **shared `FileDiffList` 组件**：commit detail 和 compare 复用同一段 +/-/@@ diff 渲染逻辑。
- **`gitCaller` 抽象**：把 cookie / PAT / 密码三种来源统一成一个 `(user, token?, authMethod)` 元组，让 `hasWriteScope` 一致地处理 scope 检查。

#### 不在 M3 内的事

- **多协作者 / 组织** — 仍然只 `owner / admin`，不引入 collaborator 表。
- **Web UI 直接编辑文件 / 在线 commit** — agent 接入后（M7）才有自动化写入路径，人类先用 git CLI。
- **issue / PR / discussion** — M4 的事。
- **SSH 协议** — 永久不做，见「不在路线图内的事」。

**退出条件（核心 7 条已通过）**：
1. ✅ 用户在 web 上注册 → 创建 PAT → 用 PAT 通过 HTTP basic auth `git push` 到一个新建仓库的 main 分支。
2. ✅ 创建非 default 分支 `feature/x`，在仓库 branches 页看到它，切默认分支，看到旧 default 还在但不是默认了。
3. ✅ 在分支页删除一个分支；尝试删除当前 HEAD 分支，被 409 拦下。
4. ✅ 打一个 annotated tag `v0.1`，refs 列表里能看到，且 `compare?from=<commit>&to=v0.1` 正确出 diff。
5. ✅ 在 compare 页选两个 ref，看到全部 diff。
6. ✅ README.md 在仓库主页被渲染成 markdown。
7. ✅ 在仓库设置页双确认删除整个仓库；DB 行和磁盘 bare repo 都消失。

**协作辅助退出条件**：
8. ✅ 给 `main` 配 force-push 保护规则，本地 `git push --force` 被 `pre-receive` 钩子拒绝；删除 `main` 也被拒。Web 上删除受 forbid_delete 保护的分支返回 409。
9. ✅ commit 详情页能看到「在以下 ref 中」列出包含该 commit 的分支 + tag。
10. ✅ 仓库主页点「Download」可下载当前 ref 的 `.zip` / `.tar.gz`。

**全程没有"issue"、"agent"、"PR"这些词出现在 UI 上。** ✅ 边界守住。

### M4 — Issue 作为唯一工作单元 ✅

把 issue 立成产品主入口，但 agent 还没接入；先让 issue 的对话 + 分支 + 合并都能用人类账号跑通，下个里程碑再把 agent 塞进同一个容器。

**核心模型——一个 issue 同时是三样东西：**

| 切面 | 内容 |
| --- | --- |
| 对话 | 标题 + 描述 + 评论流（按时间线，含人类评论、agent 消息、系统事件如 commit/merge） |
| 分支 | 自动绑定一条 git 分支 `issue/<n>`（**create 时即落库**，首次 push 才在磁盘出现 commit） |
| 会话 | 一个 agent session（M7 才接入；M4 阶段 issue 可以没有 session） |

#### 已完成

- [x] `modules/issue`：四张表
  - `issues`（id、repo_id、number、author_id、title、body、state `open | merged | closed`、branch_name、base_branch、head_sha、parent_id、parent_number、merge_commit_sha、merged_at、created/updated_at；UNIQUE(repo_id, number)）。
  - `issue_counters`（per-repo 单调 issue number，create 走 UPSERT + RETURNING 在同一事务里 mint，杜绝并发竞争）。
  - `issue_comments`（含 `file_path` + `line` —— 行内评论是 IssueComment 的一种，不是另一个实体）。
  - `issue_events`（kind + JSONB payload，承载 `commit_pushed` / `branch_merged` / `state_changed` / `title_changed`）。
- [x] Issue API（全部挂在 `/api/repos/{owner}/{name}/issues` 下）：
  - `POST /issues`（可选 `parent_number` 走子 issue 路径）
  - `GET /issues?state=&offset=&limit=` / `GET /issues/{n}` / `PATCH /issues/{n}`（title / body / state）
  - `GET /issues/{n}/timeline`（评论 + 事件 union，前端按 created_at 排序混排）
  - `GET /issues/{n}/diff`（base..issue_branch；空分支返回 `[]`）
  - `GET /issues/{n}/commits`（base..issue_branch 的 commit 列表，从 head 走 ListCommits + IsAncestor 提前停，上限 200）
  - `GET /issues/{n}/children`
  - `POST /issues/{n}/comments`（评论只追加；**不开放删除接口** —— issue 时间线视作 append-only 审计流，避免事后改史）
  - `POST /issues/{n}/merge`（owner / admin 触发；冲突返回 409 + `ErrMergeConflict`）
  - `POST /issues/{n}/sync`（手动驱动一次 HeadSHA 同步 + `commit_pushed` 事件补录；M4 UI 不再用，留作给 agent / 外部调用方用）
- [x] **Issue 分支与 push 的关系（核心收紧，已上线）**：
  - **跨模块接口**：`repodomain.BranchWriteGuard` + `repodomain.PushObserver` 进 repo domain；repo handler 依赖 `[]Guard` / `[]Observer`，issue 模块通过 ioc 把自己的实现挂进去。
  - **API 侧**：`createBranch` / `deleteBranch` 都过 guard chain，违规返回 403 + `ErrBranchWriteDenied`。
  - **CLI 侧**：在 receive-pack handler 里 PreReceive 阶段把当前开放 issue 编号写到 `hangrix-issue-mode` sidecar；pre-receive 钩子脚本读 sidecar 后：
    - 推到 base branch → 拒（only-merge）
    - 推到不匹配 `issue/<n>` 的任何分支 → 拒
    - 推到 `issue/<n>` 但 n 不在开放 issue 集合 → 拒
  - **PostReceive**：在 detached context 里跑 issue 的 `SyncIssueBranch`，比对 head_sha 后写 `commit_pushed` 事件。
  - **Merge 内部豁免**：merge API 通过 `BranchWriteOp.IsInternal = true` 绕过 guard，让 owner 可以把 issue 分支推进 base branch。
- [x] **三方合并实现进 `modules/git`**：`MergeBranch(intoBranch, fromRef, message, author)` 返回 `(sha, mode)`，mode 是 `fast-forward` / `merge-commit` / `up-to-date`。
  - FF / up-to-date 路径直接走 ref 更新。
  - 真正三方合并：flatten 三棵 tree → 逐 path 三方对比 → 单边修改取该边、双边相同合并取任意一边、双边发散返回 `ErrMergeConflict`（M4 不做行级解决，留给"先 rebase 再合"的人工流程或 M7 agent）。
  - 同步新增 `ResolveCommit(path, ref)` —— 空仓 / 不存在的分支返回空字符串而不是 error，便于 guard / sync 处理"新建 branch"路径。
- [x] **子 issue（计划外但已上线）**：父 issue 的 `issue/<n>` branch 成为子 issue 的 base branch；合并子 issue 是把 commit 推进父分支，再合并父 issue 时把整组 commit 一并带进 default branch。父 / 子关系靠 `parent_id` 外键 + 冗余 `parent_number` 实现，避免列表视图二次查询。
- [x] **前端**：
  - RepoSidebar 加 Issues 入口（即使空仓也可见 —— 第一个动作可以是开 issue 而不是 push）。
  - Issues 列表（state 过滤 + 卡片视图）+ 新建页面（无 base-branch 选择器：top-level 用 default、子 issue 用父分支，base 是隐含上下文；轻量 card padding、占位符不写要求文案）+ 详情页：
    - 三 tab：`Conversation` / `Commits` / `Diff`，tab 状态映射进 `?tab=` query 参数（默认 conversation 时 query 留空），URL 可分享 / 浏览器前进后退 / 刷新都能保持选中态。
    - GitHub 风格评论卡：avatar header strip + 作者名 + 动词（`opened` / `commented`）+ 相对时间（hover 显示绝对时间）；系统事件（commit_pushed / branch_merged / state_changed）退到细行内 strip + 圆点 marker，不抢评论的视觉。
    - 评论框：avatar header + 大尺寸 textarea（`rows="8"` + `min-h-44`）+ 紧凑内边距。
    - 15s 自动刷新（拉取 issue + timeline + diff + commits + children），hidden tab 自动暂停 —— 取代了原来的手动 Sync 按钮。
    - Parent / sub-issues 侧栏 + 「Changes」卡（diff 行数 +N −M / 改了 N 个文件）+ merge / close / reopen 操作。
  - `FileDiffList` 重写成 GitHub 风格 unified diff：table 布局 + 旧/新行号双 gutter + emerald/red 行底 + sky hunk header + 文件级 +N −M 徽章 + 文件级折叠开关 + 「view before」/「view after」blob 链接（commit 详情页和 issue diff tab 共用同一组件）。
  - 中英双语 i18n 完整接入。
- [x] **权限**：依然只用 M1 的 `user / admin` 二分。public 仓库的 issue / 评论对任何登录用户开放；merge / state 变更归 owner 或 admin。

#### 计划里改掉的事

- **issue 分支不再"懒创建"**：原计划"首次 push 才建分支"，实际改为 create 时立刻落 `branch_name`、磁盘上不创建 ref，等首次 push（受 guard 校验）落盘。代价是磁盘和元数据短暂错位（issue 已存在但 `issue/<n>` ref 不存在），收益是 guard 的判断只看 DB，pre-receive 钩子也不用查"分支是否预创建"这种态。
- **行内评论 UI 暂未做**：API 支持 `file_path` + `line` 字段，前端 diff tab 暂时只渲染 patch，没接评论锚点。下个 milestone 顺手补。
- **issue 状态机简化**：不允许从 `merged` 反向。`closed ↔ open` 自由切换；进入 `merged` 只走 `/merge` 端点。
- **评论不允许删除**：原本提供了删除评论的 UI 和 `DELETE /issues/{n}/comments/{id}` 路由，落地时撤回。理由：issue 时间线本身就是审计流（M7 起 agent 会把动作落到这里），让作者后期改史会破坏可追溯性。需要纠正的话再发一条评论说明就行。前端 UI / 后端路由 / Store 接口三处都已清理掉。
- **手动 Sync 按钮替换成自动轮询**：原侧栏的「Sync」按钮拿掉，换成 15s 间隔的 auto-refresh（hidden tab 不跑）。`POST /sync` 端点保留 —— 给将来的 agent / 外部脚本用。

#### 退出条件（已通过）

1. ✅ 用户登录 → 在自己仓库开一个 issue「修一下登录页 bug」→ 拿到分支名 `issue/1`。
2. ✅ 用 git CLI checkout `issue/1` → 改代码 → push → receive-pack 钩子放行（issue 开放）→ post-receive 写入 `commit_pushed` 事件 → 浏览器刷新可见。
3. ✅ 在 issue diff tab 看到分支 vs base 的文件级 diff。
4. ✅ 点 merge → MergeBranch 走 FF（issue 分支以 base 为起点的情况下）或写出 merge-commit；issue state 转 `merged`，timeline 多两条 event（branch_merged + state_changed）。
5. ✅ 尝试 `git push origin random-branch` → 被 pre-receive 钩子拒，错误信息明确指向"open issue 才能 push"。
6. ✅ 尝试 `git push origin main` → 被钩子拒（只能 merge）。
7. ✅ 在 issue 详情页点「New sub-issue」→ 子 issue base 自动是父 issue 的分支；合并子 issue 把 commit 推进父分支。
8. ✅ 全程 UI 中没有"PR"这个词。

### M5 — 组织 / Organizations ✅

把"个人账号 + 仓库"模型扩展到组织：一个 organization 是独立的 owner 实体，能拥有仓库、有多个成员、有自己的资料页。所有 git / issue 能力对 org-owned 仓库无感复用 —— 仓库路由 `/{owner}/{name}` 的 `<owner>` 从"必须是 user 用户名"扩展到"user 或 org 名"。

> **为什么先做（在 M6 agent 链路之前）：** M7 起官方预设 agent 仓库（`hangrix/dispatcher` / `hangrix/maintainer`）需要一个稳定的 owner 命名空间 —— 不能让平台预设挂在某个具体 admin 个人名下，否则该人离开 / 删号 / 转账号都会带跑整个 agent 生态。先把 organization 立起来，让 `hangrix/*` 这种"平台级 agent owner"自然成立；同时让用户能用组织聚合自己的项目和 agent 仓库。
>
> 实现上刻意参考 GitHub 的组织模型 —— "用户 / 组织 / 仓库"三层在 git 协作语境下已被验证够用，不发明新概念。

#### 设计原则

1. **Owner 命名空间统一。** `<owner>` 既可以是 user 用户名，也可以是 organization 名；同 namespace 互斥（创建 org 时校验该名在 `users.username` ∪ `organizations.name` 全集内未占用，反之亦然）。所有 `/{owner}/{name}` 路由透明支持两类。
2. **Org 不是 identity。** Org 没有密码 / session / PAT —— org 本身不"做事"，做事的总是它的成员。Org 是 namespace + ACL 容器。
3. **权限继续刻意简单。** Org-level role 只两档：`owner` / `member`。Owner 能改 org 设置 / 加减成员 / 删 org；member 能在 org 名下建仓库 + 访问 org 的私有仓库。**不引入 team / outside collaborator / repo-level role**（那些等真有需求再做）。
4. **Repo 归属二选一，可转移。** 任一仓库归属 user 或 org（不可同时）。Transfer 是 owner-only 操作：DB 字段切换 + 磁盘 bare repo rename 落在同一事务。
5. **Agent identity 不变（M7 起）。** Commit author 仍是 role key、email 仍是 `<role-key>@agents.<host-domain>` —— 跟仓库归属于谁无关。Org owner 跟 user owner 在 agent 调度路径上等价。

#### 数据模型

- **`modules/org`**：新模块。
  - `organizations`（id、name UNIQUE、display_name、description、avatar_url、created_by、created_at、updated_at、deleted_at NULL）。软删除走 `deleted_at` —— 跟 user `disabled` 的取舍一致，留行不真删，便于审计。**原计划的 `visibility` 列已撤回**（见后文"计划外但已经做了的事"），M5 在 ship 前删了列：迁移 `00001_create_organizations.sql` 创建带 visibility 的表，`00002_drop_org_visibility.sql` 立刻把它删掉 —— 历史层叠保留是为了让任何中间 checkout 的部署一次性应用就能落到一致状态。
  - `organization_members`（org_id、user_id、role `owner | member`、added_by、added_at），PK `(org_id, user_id)`。约束：单 org 至少一个 owner（删最后一个 owner / 降级返 409）。
- **`modules/repo` 改造**：
  - `repos.owner_id`（M1 时是 `NOT NULL REFERENCES users(id)`）拆成 `owner_kind` + `owner_user_id` / `owner_org_id`：`owner_kind IN ('user','org')`，两列其一 NOT NULL（DB CHECK `(owner_user_id IS NOT NULL) <> (owner_org_id IS NOT NULL)` 保证恰一）。
  - UNIQUE 拆 partial：`UNIQUE (owner_user_id, name) WHERE owner_user_id IS NOT NULL` + `UNIQUE (owner_org_id, name) WHERE owner_org_id IS NOT NULL` —— 同 owner 下 name 唯一，但 user 跟 org 哪怕同名也互不冲突（owner 名 namespace 已经在 ResolveOwner 那一侧保证全集唯一）。
  - 新增 `org/domain.Resolver` 跨模块接口（`ResolveOwner(name) → (kind, id, name)` + `Membership(orgID, userID) → (role, ok, err)`），由 `modules/org` 注入 ioc 容器；`modules/repo` 的 handler 和 git 路径全部走它，不再各自查 `users.username`。
- **磁盘路径**：bare repo 落 `data/repos/<owner>/<name>.git`，`<owner>` 直接是 user 用户名或 org 名（共享同 namespace，路径无需区分）。transfer 时先 DB swap 再磁盘 `os.Rename` —— 失败回滚 DB（不是同一事务，因为磁盘操作不在 DB 事务里，所以是"补偿"而非"原子"，留了 admin 兜底空间）。
- **保留名（reserved）**：补一份系统保留 owner 名单（`admin` / `api` / `git` / `static` / `_` 等）拒掉 user / org 创建撞名，避免跟 web 路由冲突。`IsReservedName` 在 user 注册和 org 创建两条路径都生效。

#### API

- [x] **Org CRUD（authenticated user 都可创建）**：
  - `POST /api/orgs`（body：name / display_name / description）—— 创建者自动落 `owner` 角色。原计划的 `visibility` 字段已删（见后文）。
  - `GET /api/orgs/{name}` —— 任意已登录用户可见。
  - `PATCH /api/orgs/{name}`（owner-only，display_name / description / avatar_url 三个字段）/ `DELETE /api/orgs/{name}`（owner-only，type-to-confirm 输入 org name 才生效）。
  - `GET /api/orgs?member_of=me`（列我加入的 org，给 AppSidebar 用）/ `GET /api/users/{username}/orgs`（列出某用户归属的 org）。
- [x] **成员管理**：
  - `GET /api/orgs/{name}/members`。
  - `POST /api/orgs/{name}/members`（owner-only，body：`{username, role}`）—— **直接加，不走 invitation 流程**（v1 刻意简单；本地优先形态不需要邮件邀请）。
  - `PATCH /api/orgs/{name}/members/{username}`（改 role）/ `DELETE /api/orgs/{name}/members/{username}`（移除）—— 最后一个 owner 移除 / 降级返 409。`DELETE` 自我移除（caller == target）跳过 canManage 检查，但仍受最后一个 owner 约束。
- [x] **仓库归属**：
  - `POST /api/repos` body 新增可选 `owner: "<name>"`（省略时归 caller 个人；指定时通过 `ResolveOwner` + `Membership` 校验 caller 是该 org 成员 —— v1 任何 role 都可建库，admin 后续按需收紧）。
  - 新增 `GET /api/orgs/{name}/repos`；既有 `GET /api/users/{username}/repos` 保留。Org-owned 仓库列表上对成员显示所有（含 private），对非成员只显示 public。
  - `POST /api/repos/{owner}/{name}/transfer`（caller 必须能写源仓库，且对 target 是合法 owner 角色或 admin；body：`{target_owner, confirm}`，`confirm` 必须等于 `<owner>/<name>` 才放行）—— DB 字段切换 + 磁盘 rename，磁盘失败时回滚 DB swap。Same-owner 转移幂等返 200。

#### Web UI

- [x] **新建 org 入口**：AppSidebar 新增 Organizations section，列出 `useMyOrgs` 拉到的组织 + 固定的「New organization」链接（导航栏底部不另开下拉，避免与个人菜单冲突）。
- [x] **Org profile 页 `/{name}`**：avatar + display_name + description + 仓库列表 tab + 成员列表 tab。路由层面：单一 `[owner]/index.vue` 同时拉 `/api/users/{name}` 和 `/api/orgs/{name}` 然后按命中渲染 —— 一个返回有效数据另一个 404 是正常路径，404 不上报 UI。URL 形态与 user profile 完全一致。
- [x] **Org 设置页 `/{name}/settings`**：owner-only，否则 redirect 回 profile。基本信息表单（display_name / description / avatar_url）+ 危险区（删除 org，type-to-confirm 输入 org name）。
- [x] **成员管理页 `/{name}/settings/members`**：列表 + 加成员（按 username 搜）+ 改 role（dropdown）+ 移除（trash 按钮）；最后一个 owner UI 上 disable 改 role + 移除按钮。
- [x] **新建仓库表单**：owner 字段从只读"当前用户"改为下拉（Select），选项 = 「@我」+ 我加入的所有 org。`?owner=<name>` query 预选某 org（由 org profile 页的「New repository」按钮使用）。**Select 空值改用 `__self__` 哨兵**：reka-ui 的 SelectItem 禁止 `value=""`，所以个人 namespace 用一个非空 sentinel 占位，提交时再还原成"不传 owner"。
- [x] **仓库设置页的「Transfer ownership」**：M3 留的占位按钮升级成真功能 —— 弹窗输入 target owner name + 再输入 `<owner>/<name>` 确认；成功后 router.replace 到新路径。
- [x] **M3 / M4 已建页面对 org-owned 仓库无感**：仓库详情 / 分支 / Tag / Issue / Compare / Settings 全部走 `ResolveOwner` + `Membership`，权限判断里再没有"caller 是不是 owner_user_id"这种用户绑定逻辑。UI 上没有 org 专属 / user 专属分支。

#### 兼容性与边界

- **现有 user-owned 仓库已就地迁移**：`00003_repos_owner_org.sql` 加 `owner_kind` / `owner_org_id` 两列 + DB CHECK + partial UNIQUE，老 `owner_user_id` 行就地 `owner_kind='user'` 落位（migration 里 backfill）。
- **PAT / 会话不变**：依然只有 user 有 PAT、有 session。Org-owned 仓库的写权限 = 「user 是该 org 成员 + PAT 有 `repo:write` scope」复合判断（在 `git_http.go` 的 `gitCaller` 里实现）。
- **Issue / 分支保护规则照搬**：M3 的 `branch_protections` 表按 repo_id 挂，跟 owner 是 user 还是 org 无关；M4 的 `issue_counters` / pre-receive 钩子同理，**没动一行代码就能跑**。
- **Audit**：v1 没拉独立的 audit log 表 —— `organizations.created_by` / `organization_members.added_by` / `repos.updated_at` 这些列已经把"谁做了什么"信息埋下了，等真有审计回放需求再补一张 `audit_events` 表（M10+ 候选）。Transfer 失败的磁盘错误目前只走 HTTP 500 + 标准日志，没专门表打点。

#### 不在 M5 里的事

- **Team / sub-group**：v1 不做。Org 只有 owner / member 两档。若将来要按"前端组 / 后端组"分权，再加 `teams` / `team_members` / `repo_team_access` 三张表 —— 但要先证明这些是 agent 协作语境下的必需，不是抄 GitHub。
- **Outside collaborator**：M3 的 owner-only 模型还没扩到"非 owner 协作者"，先维持现状。M10+ 一并做（见 [不在路线图内的事] 末尾候选）。
- **Org-level PAT / OAuth app**：org 不"做事"，没有自己的 token。
- **Invitation 流程**：直接加成员，不发邀请邮件、不等接受 —— 本地优先形态下用户已经在同一部署里，不需要 SaaS 风格的 invite handshake。
- **Billing / 多租户**：永远 by 设计不做（原则 6 本地优先）。
- **独立 audit 表**：见上"Audit"段，留给 M10+。

#### 计划外但已经做了的事

- **撤回 Org visibility**：原 spec 里 organizations 表有 `visibility public | private` 列，设计意图是「private org 只对成员可见」。落地走到一半发现这个语义在本地优先 + 仓库自己也有 visibility 的形态下并不增加任何价值 —— 用户真正想隐藏的永远是仓库内容，org 名本身在 namespace 全集里早就是公开标识（用 `git clone` 试一下就知道存不存在）。继续维护 visibility 只会让 `canRead` 在「org 私 / 仓库公」「org 公 / 仓库私」四象限之间纠结。直接砍掉：domain / handler / sqlc / 前端 / i18n 全清，迁移 `00002_drop_org_visibility.sql` 落库；`canRead` 简化为「登录即可见」。仓库可见性独立保留，与 owner kind 完全解耦。
- **面包屑机制重构**：M5 加了 `/{owner}/settings*` 一系列页面之后，AppHeader 那套 130 行 `route.path` 大 switch 终于撑不住。换成 `composables/useBreadcrumbs` —— 每页 `setBreadcrumbs(supplier)`，supplier 包在 `watchEffect` 里跟随 locale / `route.params` / fetched 数据自动重算。19 个现有页面全部迁移（含 M2-M4 已有的所有 repo 子页），未迁移页面会在 header 显示原始路径作为告警信号。这件事不属于 M5 spec，但 M5 的 owner / settings / members 三层嵌套页是触发它的最后一根稻草。
- **`.mcp.json` 瘦身**：开发工具链清理 —— 移除社区 `@modelcontextprotocol/server-filesystem` / `server-git`（前者跟内置文件工具重复，后者 npm 上根本没这个包名），把 playwright 改成官方 `@playwright/mcp@latest` 并固定 `--browser=chromium`。让 Playwright 验收 M5 UI 端到端走通的步骤无障碍跑完。

#### 退出条件

1. ✅ 用户 A 在 web 上新建 org `playtestorg` → 拿到 `/playtestorg` profile 页 + 自动是 owner → 在 `playtestorg` 名下新建仓库 `playtestorg/e2erepo`（勾上 init_readme）→ `git clone http://.../git/playtestorg/e2erepo.git` 拉到 README。
2. ✅ 用户 A 把用户 B（`bobtest`）加为 `playtestorg` 的 member → B 登录后能在 AppSidebar 里看到 `playtestorg` → B 也能在 `playtestorg` 名下创建仓库（`POST /api/repos` 带 `owner=playtestorg` 返 201）。
3. ✅ 用户 A 把私人仓库 `playtester/my-personal` transfer 给 `playtestorg` → 磁盘从 `data/repos/playtester/my-personal.git` rename 到 `data/repos/playtestorg/my-personal.git` → `/playtestorg/my-personal` 路由立刻生效 → 原 `/playtester/my-personal` 路由 404。**未做的子项**：旧 PAT 在 transfer 后能否继续 push（PAT 跟 user 绑定，仓库归属变化不影响 token 自身，但 push 权限需 caller 是新 owner 的成员 —— 已经在 receive-pack 路径覆盖，但没专门跑一遍端到端）。
4. ✅ 用户 A 创建保留名 `admin` 的 org → 409 `name is reserved`；创建 user 重名 `playtester` 的 org → 409 `name already taken`；反向尝试注册 user `playtestorg`（与 org 撞名）→ 409 `username already exists`。
5. ✅ 在 acme 仅剩 alice 一个 owner 时移除 alice → 409 `cannot remove the last owner`；升 bob 为 owner 后再移除 alice → 204。
6. ✅ 在 org-owned 仓库上跑 M4 的退出条件 1-8 —— smoke-test 已经验证 push / merge / 钩子链路，所有路径都走 `ResolveOwner` 解析 owner，pre-receive 和 issue guard 完全无感。
7. ✅ 全程 admin 后台没有"组织管理"特殊视图 —— `/admin/users` 是唯一管理界面，org 是用户能自助管理的资源；admin 仍能借身份进任意 org settings 兜底，但 sidebar 上不挂入口。

### M6a — LLM provider & proxy ✅（**已经历一轮重构，详见 [docs/llm-proxy.md](docs/llm-proxy.md)**）

平台第一步要能跟 LLM 说话。Admin 配 provider → 平台跑代理 → 任何 OpenAI SDK 客户端都能调 → 用量落表。

**当前形态摘要：** 端点合并为单一 `POST /api/llm/v1/responses`；provider 路由由请求体 `model` 字段反查 `allowed_models` 决定；Provider 字段裁剪到 name/type/base_url/api_key/allowed_models 五项；session token 从这个模块剥离搬进 `modules/runner`（见 [docs/agent-identity.md](docs/agent-identity.md)）。三种 adapter（`openai` / `anthropic` / `openai-compat`）都走 typed Request/Response；reasoning effort 在 Anthropic 翻成 thinking budget，DeepSeek `reasoning_content` 跨轮 round-trip；stream=true 一律 501。Infra 已迁到 sqlc 生成。

**退出条件（已通过）：** admin 注册 provider → 用 `hgxs_` token 调 `/api/llm/v1/responses` → 上游 mock 返回 200 + body 原样回 → `llm_usage_log` 落一行（含 reasoning_tokens 拆分）→ revoke session 立即 403。Adapter 翻译规则与边界条件在 `internal/modules/llm_proxy/upstream/upstream_test.go` 跟 [docs/llm-proxy.md](docs/llm-proxy.md) 里钉住。

### M6b — Agent runtime（Go binary） ✅

立一个独立的 agent 二进制（**用 Go 从头写，不依赖任何现成的 agent SDK**），跑在容器里跟 M6a 的 LLM proxy + 平台 API + git CLI 三方说话。M6c 把这个二进制 bind-mount 进 runner 调度的容器里。

> **为什么从头写：** 现有 SDK（Claude Agent SDK / OpenAI Assistants / LangChain agent runner）都是 opinionated 的 high-level 抽象，对 prompt 拼装 / tool call 协议 / 重试 / 上下文管理留的口子有限。Hangrix 要把 audit、role identity、prompt 来源（base + host addendum）、git 工作流深度嵌进 loop，control 全攥手里更划算。**单二进制 Go 实现 + OpenAI Response API HTTP 客户端 hand-rolled + git CLI shell-out** —— 没有 third-party agent framework 依赖。

#### 角色与边界

- **进程级 agent runner**：每个 role 在容器里跑一个 `hangrix-agent` 进程，存活到 issue 归档或 idle 超时。
- 通过 **stdin / stdout JSON-Lines** 跟外层 runner（M6c）通信：runner 喂事件，agent 报告 tool call / 状态 / 日志。
- 通过 HTTP 跟 **M6a 的 LLM proxy** + **平台 API** 通信，凭证从 env 拿。
- 通过 **shell-out** 调容器内 `git` CLI 处理仓库读写。
- **无状态执行器**：session 状态归 Hangrix 平台管，agent 进程重启时从平台拉历史重建上下文。

#### 二进制构成

```
hangrix-agent/                       # Go module
├── cmd/hangrix-agent/
│   ├── main.go                      # 入口：buildContainer() + ioc.Get[*app.App].Run(ctx)
│   └── wiring_test.go               # 钉住 ioc 依赖图
├── internal/                        # M6b ship 后 pkg/ → internal/，可见性靠 Go internal/ 机制兜底
│   ├── config/                      # *Config + env 校验（缺必填项即时 panic）
│   ├── llm/                         # OpenAI Response API HTTP 客户端（hand-rolled）
│   ├── mcp/                         # HTTP MCP client（连平台 MCP server 拉 issue_* / roster_*）
│   ├── tools/                       # 工具注册表 —— 本地工具 in-process + 平台工具走 internal/mcp
│   │   └── local/                   # read / write / edit / glob / grep / bash / webfetch 本地实现
│   ├── prompt/                      # 三层 system prompt 拼装
│   │   ├── baseline.md              # 内置 runtime baseline（//go:embed）
│   │   ├── assemble.go              # baseline + agent base_prompt + host addendum 叠加
│   │   └── module.go                # ioc provider：translate *config.Config → Inputs
│   ├── runtime/                     # 主循环 + 上下文管理
│   ├── ipc/                         # runner 通信（stdin/stdout JSON-Lines）
│   └── app/                         # 顶层 *App.Run(ctx)，被 ioc.Get 解出来
└── go.mod
```

每个 `internal/<name>/` 包内都带一个 `module.go`，就近注册 ioc provider（`Deps` 结构体 + `New*(deps *Deps) *T` + `Module() *ioc.Module`），不另起 wiring shim 包。

工具分两类，LLM 看到的是一个扁平 function-call 列表：

- **本地工具**（agent binary 内置，容器内执行）：`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`，参考 Claude Code 同名工具的语义（`read` 行号 + offset/limit / `edit` 强制先读 / `bash` 含 stdout-stderr-exit_code / `grep` 用 ripgrep / 等）。Agent 不包装 git —— 仓库操作（clone / pull --rebase / commit / push）由 LLM 通过 `bash` 直接调 `/usr/bin/git`，凭证由 M6c runner 在容器启动时通过 credential helper 预配置。
- **平台工具**（Hangrix 平台通过 HTTP MCP 暴露）：`issue_*` / `roster_*`，agent 用 `internal/mcp` 的标准 MCP client 调远端服务端点（M7b 起 ship 的平台 MCP server）。M6b 期间这类工具走 stub —— 没有真平台 MCP server 也能跑本地工具自检。

#### 启动期环境（由 M6c runner 注入）

| Env | 含义 |
|---|---|
| `HANGRIX_SESSION_TOKEN` | **统一 session-scope 凭证** —— 同一个 token 同时用于：LLM proxy（Bearer）、平台 MCP server（Bearer）、git HTTPS push（HTTP Basic 的 password 字段，username 任意）。session 结束即过期 |
| `HANGRIX_LLM_ENDPOINT` / `HANGRIX_LLM_MODEL` | LLM proxy 端点 + 已 resolve 的具体 model 名 |
| `HANGRIX_PLATFORM_MCP_ENDPOINT` | 平台 HTTP MCP server endpoint |
| `HANGRIX_SESSION_ID` | session uuid |
| `HANGRIX_ROLE` | 当前 role key |
| `HANGRIX_HOST_REPO` | host 仓库 `<owner>/<name>` |
| `HANGRIX_ISSUE_NUMBER` / `HANGRIX_WORKING_BRANCH` / `HANGRIX_BASE_BRANCH` | 关联 issue 上下文 |
| `HANGRIX_AGENT_BUNDLE` | 容器内 agent 仓库展开路径（含 `agent.yml` + `prompts/`）|
| `HANGRIX_HOST_ADDENDUM` | host prompt addendum 文件路径（runner 写到容器内临时文件，agent 读）—— 不假设长度，避免 env 上限风险 |
| `HANGRIX_TOOL_CATALOG` | 该 role `can:` 允许的工具名 JSON 数组 |

#### IPC 协议（stdin / stdout JSON-Lines）

Runner → agent（stdin）：

```json
{"kind": "history", "messages": [...]}      // 第一条，session 历史回放
{"kind": "event", "event": "issue.comment.mentioned", "payload": {...}}
{"kind": "control", "op": "shutdown"}
```

`history.messages` 是 OpenAI Response API 风格的扁平消息数组，按时间排：

```json
[
  {"role": "user", "kind": "event", "event": "issue.comment.mentioned", "content": "..."},
  {"role": "assistant", "content": "...", "tool_calls": [{"id": "tc-1", ...}]},
  {"role": "tool", "tool_call_id": "tc-1", "content": "...result..."},
  {"role": "assistant", "content": "..."},
  ...
]
```

Agent → runner（stdout）：

```json
{"kind": "status", "phase": "thinking"}
{"kind": "message", "role": "assistant", "content": "...", "tool_calls": [...]}
{"kind": "tool_call", "name": "issue_comment", "args": {...}, "result": {...}, "tool_call_id": "tc-1"}
{"kind": "log", "level": "info", "msg": "..."}
{"kind": "done", "turn_id": "..."}
```

Runner 实时把 `message` + `tool_call` 落到该 session 的消息日志（含 LLM 完整对话历史，是 audit 的一部分），同时把语义事件（commit / merge / review_vote）转回平台事件总线。容器下次重启时，runner 从消息日志里取出全部历史回放为一条 `history` IPC 消息。

#### 主循环

```
systemPrompt := prompt.Assemble(env)              // baseline + agent base + host addendum

// 首条 stdin 必须是 history（可能为空数组，新 session 时）
history := ipc.ReadHistory(stdin)
ctx := context.New(systemPrompt, history)         // 用历史预填消息列表

for {
  event := ipc.ReadEvent(stdin)                   // 等下一个事件
  if event.IsShutdown() { return }
  ctx.AppendEvent(event)

  for {                                           // 单轮可能多 tool round-trip
    resp := llm.CreateResponse(model, ctx.Messages(), tools.CatalogForRole(role))
    ctx.AppendAssistant(resp)
    ipc.ReportMessage(resp)                       // → runner 落入 session 消息日志
    ipc.ReportStatus("thinking")

    if !resp.HasToolCalls() { break }

    for _, call := range resp.ToolCalls {
      result := tools.Execute(call)
      ctx.AppendToolResult(call.ID, result)
      ipc.ReportToolCall(call, result)            // → runner 落入 session 消息日志
    }
    // 把 tool 结果喂回 LLM
  }

  ipc.ReportDone()
}
```

#### 已完成

- [x] **新模块 `apps/hangrix-agent/`**：独立 Go module（`github.com/hangrix/hangrix/apps/hangrix-agent`），加进 `go.work`。**无 third-party agent framework 依赖**；唯一非 stdlib 库是 `JohannesKaufmann/html-to-markdown/v2`（M6b ship 后给 `webfetch` 引入，详见后续打磨）。纯 `go build ./cmd/hangrix-agent` 出来即可 bind-mount。
- [x] **`internal/llm`**：手写 OpenAI Response API 非流式客户端 —— `POST {endpoint}/responses`，`input` 走 `message` / `function_call` / `function_call_output` 三类 item，`tools` 走扁平 function 描述符。指数退避重试覆盖网络错误 + 5xx + 429；4xx 直接返回不重试。`ToInputItems` 把 agent 内部的扁平 `Message` 转成 Response API 的 item 数组（assistant 文本 + 后续 function_call 拆成相邻多 item，确保 call_id 关联得回对应的 function_call_output）。SSE 流式留作后续，**M6b 走非流式就够跑通退出条件**。
- [x] **`internal/mcp`**：Streamable HTTP MCP client（JSON-RPC 2.0 over POST，Bearer 鉴权）。`tools/list` + `tools/call` 两条路径，单 `Accept: application/json, text/event-stream` 头，单条 SSE `data:` 帧也能解；JSON-RPC envelope 里有 `error` 字段直接转 Go error。同样指数退避重试，4xx 视为 terminal。
- [x] **`internal/tools/local`**：七件本地工具按 Claude Code 语义实现 —— `read`（行号 prefix + 默认 2000 行 + offset/limit）/ `write`（默认拒覆盖，需显式 overwrite）/ `edit`（`replace` / `insert` / `delete` 三模式，**`ReadTracker` 保证同 session 先 `read` 过才允许 `edit`**）/ `glob`（支持 `**`，按 mtime 倒序）/ `grep`（优先 `rg`、缺失则 Go fallback；ripgrep 退出码 1 = 零匹配语义对齐）/ `bash`（**走 `bash -c` 不是 `sh -c`**，前台 + `run_in_background=true` + tool-owned `task_id` 轮询；task_id 与 command 互斥强校验；前台超时返 `timed_out=true`）/ `webfetch`（默认 HTML → Markdown：标题 / 列表 / 链接 / 代码块都保下来；`raw=true` 取原始 body；4 MiB 上限避免吞 context）。所有工具的错误信息按「what / why / how」三段写，LLM 把 tool error 直接当文档用。
- [x] **`internal/tools` registry**：合并本地 + 远端工具源，按 `HANGRIX_TOOL_CATALOG`（JSON 数组）过滤；不在 catalog 里的工具直接从 LLM 看见的清单里删除。统一 `Call(ctx, name, args) → CallResult{Source, ResultJSON, IsError, ErrMsg}` 签名；未知工具名返 `IsError + 可用工具列表`，让 LLM 在下一轮自我修正。tool descriptor schema 跟 OpenAI function-calling 对齐。
- [x] **"push 前自行 rebase"靠 prompt 教**：写进 `baseline.md` 的「Git collaboration」节 —— `git push` 失败 → `git pull --rebase origin <working-branch>` → 重推；不要 `--author`；agent 允许临时切分支但最终 work product 必须落在 working branch。代码侧零 git 重试逻辑。
- [x] **`internal/prompt/baseline.md`**：runtime 内置 baseline，`//go:embed` 编进二进制。覆盖操作原则 / 工作循环 / 工具纪律 / Git 协作 / 行为约束 / 上报回流六节，按 RFC 2119 关键词（**MUST / SHOULD / MAY**）写规则，明文声明「baseline 由平台 own，agent 作者和 host 不能在上层 prompt 里 weaken」。
- [x] **`internal/prompt/assemble`**：三层叠加按 spec 顺序拼装（runtime 上下文 KV 块 → baseline → AGENT BASE PROMPT 标题 + 内容 → HOST REPO ADDENDUM 标题 + 内容）。`agent.yml` 里 `entry.base_prompt` 走最小行扫描提取（避开 yaml 依赖）；bundle 路径配了但读不到 → 返错而不是静默跌回 baseline-only（misconfig 要响）。Runtime KV 块只暴露 role / session_id / host_repo / issue_number / base_branch / working_branch —— LLM / MCP endpoint 等 runner 内务不进 prompt。
- [x] **`internal/runtime/loop`**：主循环 + `Context` 管理。每收一条 `event` 进单轮内 `LLM → tool calls → 喂回 → 再 LLM` 的 round-trip 直到 LLM 不再发 tool call；上限 `maxToolRounds=16` 防失控。
- [x] **会话历史回放**：启动后**第一帧必须是 `kind:history`**，否则视为 runner bug 直接退出（不允许"半空 context"启动）。`messages:[]` 是合法的"新 session"。第二个 `history` 帧支持热替换 working context，给 agent 重启回流场景留口子。
- [x] **新消息回报**：每次 LLM 返回 assistant 消息走 `Outbound{Kind:"message"}` 出栈，每次 tool call + 结果走 `Outbound{Kind:"tool_call"}`（带 `tool_call_id`、`name`、`args`、`result`）出栈。**agent 不写盘**，runner 是消息日志的唯一持久化者。
- [x] **上下文窗口裁剪**：v1 用尾窗口截断（`trimMaxMessages=60`），裁剪只影响下一轮发给 LLM 的 messages，不丢 audit 日志。摘要 / RAG 留给 M9。
- [x] **`internal/ipc/jsonl`**：stdin `bufio.Scanner`（lift 到 16 MiB 容纳完整 history 帧）+ stdout `Mutex` 包的 writer，五种 outbound（status / message / tool_call / log / done）做成命名方法。
- [x] **`cmd/hangrix-agent/main` + ioc 装配**：`buildContainer()` 加载 `config` / `llm` / `mcp` / `tools` / `prompt` / `ipc` / `runtime` / `app` 八个 module，`ioc.Get[*app.App](c).Run(ctx)` 跑主循环。`internal/config` 集中读 env 并校验必填项（缺失 panic）；其余 module 通过 `*Deps` 结构体显式声明依赖。Provider 内 panic 由 main 的 `recover` 翻译成单行 stderr + exit 1，对外失败形状与 pre-ioc 一致。`signal.NotifyContext(SIGTERM, SIGINT)` graceful shutdown 不变。
- [x] **冒烟测试**：`internal/runtime/loop_test.go` —— 起本进程 `httptest` mock LLM（按调用次数返 tool call → 终止 message）+ mock MCP（`tools/list` 出 `stub.ping`、`tools/call` 回 `pong`）+ 真本地工具（`read` 一个 sandbox 文件），通 `io.Pipe` 喂 IPC，断言一轮 LLM round-trip 内同时跑了一个本地工具 + 一个 MCP 工具 + 收到 final assistant message + `done` 帧。`cmd/hangrix-agent/wiring_test.go` 设置必填 env 后端到端 `buildContainer()` + `ioc.Get[*App]`，确保任一 module Deps 名了未注册的类型都会被 CI 拦下。

#### 计划外但已经做了的事

- **MCP client 兼容 Streamable HTTP 单帧 SSE**：spec 说连 `tools/list` / `tools/call`，落地时发现 MCP 服务端可以按 `Accept` 协商出两种 Content-Type（`application/json` 一发一收 / `text/event-stream` 单 `data:` 帧）。客户端两种都吃，避免「平台 MCP server 选了 SSE 框就 break」的脆弱性。一边走全双工 SSE 事件循环不在 M6b 范畴（也没有 long-lived MCP 通知场景），先支持「单 final 事件」就够 `tools/call` 落地。
- **三个测试 file**：`internal/ipc/jsonl_test.go`（IPC 三种 inbound 形状 + 并发写）/ `internal/prompt/assemble_test.go`（三层顺序 + bundle misconfig 必报错）/ `internal/tools/local/local_test.go`（read-before-edit 守卫 + bash exit code 透传）。退出条件本身已被 `loop_test.go` 覆盖；这三个 file 是把易腐的局部不变量钉在 CI 上。
- **`Source` 字段挂在 `tools.CallResult`**：原 spec 没区分 local 和 mcp 工具的产源；落地时发现 audit 链路上 runner 想知道一次 commit 是 LLM 通过 `bash git push` 干的还是通过 `issue_*` 平台工具干的，把 source 标在 result 上几乎零成本就给 M6c / M7b 的 audit 分类留好钩子。

#### 不在 M6b 里的事

- **SSE 流式 LLM**：`internal/llm` 当前非流式，`stream:false` 写死。OpenAI Response API 流式协议事件类型多（`response.created` / `response.output_item.added` / `response.function_call_arguments.delta` / …），非流式跑通 round-trip 才是退出条件，流式留作后续 issue（最早 M9 上下文优化时一起做，能流式增量裁剪）。
- **平台 MCP server 真实现**：M6b smoke test 走 mock。真平台 MCP server（`issue_*` / `roster_*` 工具集）在 M7b。
- **Docker 镜像**：M6b 只产 `hangrix-agent` 二进制（`go build ./cmd/hangrix-agent` 即出）。容器镜像本身是 M6c runner 编排的事，二进制是被 bind-mount 进去的，不打进镜像。
- **agent 的 git 重试 / rebase 内置逻辑**：故意没做。靠 baseline prompt 教 LLM 走 `git pull --rebase` —— force-push 受 hook 禁，自然逼着流程。**这是 spec 明文设计**，写在 baseline 里。

#### 退出条件（已通过）

按 `loop_test.go` 验完整链路：mock LLM + mock MCP + 真本地工具 + 真 IPC pipe → 喂 history (空) + 一条 `issue.comment.mentioned` 事件 → 一轮 LLM 调用拿到两条 tool call（`read` 本地 + `stub.ping` MCP）→ 两个 tool call 各执行 + 结果回喂 → 第二轮 LLM 返 final assistant message（无 tool call）→ `done`。整条路径在本进程内闭环，docker 端到端验证留给 M6c runner 做（runner 才知道怎么拉镜像、配 credential helper、bind-mount agent binary）。**纯 agent binary 验证，不依赖真 runner、平台 MCP server 走 mock**。

#### M6b 后续打磨（仍在 M6b 范畴）

主干闭环、退出条件通过之后，又做了一轮运行时打磨。每项都已合到主干，但都不够独立到开一个新里程碑，记录在这里防它们隐没在 git log 里：

- **`pkg/` → `internal/`，进程走 `pkg/ioc` 装配。** Agent 内部所有子包从 `apps/hangrix-agent/pkg/...` 搬进 `apps/hangrix-agent/internal/...`，让可见性靠 Go 的 `internal/` 机制显式兜底（这些子包确实不对外消费）。每个子包就近放一份 `module.go` 注册 ioc provider（`Deps` 结构体 + `New*(deps *Deps) *T` + `Module() *ioc.Module`），不另起 wiring shim 包。`cmd/hangrix-agent/main.go` 收成 `buildContainer()` 加载八个 module（`config` / `llm` / `mcp` / `tools` / `prompt` / `ipc` / `runtime` / `app`）+ `ioc.Get[*app.App](c).Run(ctx)`，跟 `apps/hangrix` 主仓库装配范式一致。Init 错误（缺必填 env、坏的 tool catalog、读不到 agent bundle）由 provider 内 panic、main `recover` 翻成单行 stderr + exit 1，对外失败形状不变。`cmd/hangrix-agent/wiring_test.go` 钉住依赖图：任一 module 的 `Deps` 名了未注册的类型，CI 立刻挂。
- **System prompt 重写为 OS-level 契约。** `baseline.md` 引入 RFC 2119 关键词（**MUST / MUST NOT / SHOULD / SHOULD NOT / MAY** 加粗），新增「Operating principles」+「Work loop」（Orient → Plan → Act → Verify → Commit & push → Report）+ 「Reporting back」三节；工具纪律按 family（Files / Search & shell / Platform / Web）重新分组；明文声明 baseline 不可被上层 prompt weaken。Branch 策略放宽：agent **MAY** 临时切到别的分支看 history / cherry-pick / diff，但 work product **MUST** 落在 working branch，**MUST NOT** 推到别的分支，turn 结束前 **MUST** 回到 working branch 且工作树干净。`.hangrix/**` 不再禁写。所有 `HANGRIX_*` env 名都从 prompt body 里清掉 —— 顶部 runtime context KV 块已经把 role / session_id / host_repo / issue_number / base_branch / working_branch 写出来，prompt 引用概念不引用变量名；`prompt.Inputs` 同步删 `LLMEndpoint` / `MCPEndpoint` 字段（端点是 runner 内务）。
- **工具错误改 LLM 友好的「what / why / how」三段格式。** LLM 把 tool error 直接当文档用，所以每个错误都解释「发生了什么 / 规则为什么存在 / 怎么修」。Read-before-edit 现在说明「edit 要求先 read 才能保证看到当前内容并精准定位」+「调 `read` 再 retry」；replace find 不命中说明「匹配精确且 whitespace-sensitive」+「re-read 再 verbatim 复制」；`bash` task_id 冲突说明「task_id 是 tool-owned，不能跟 command 同时给」；`webfetch` 非 http/https scheme 提示「想读本地文件请用 `read`」。`internal/tools/local/local_test.go` 钉住 read-before-edit 的几个关键短语，未来 refactor 抹掉 hint 会被 CI 拦下。
- **`bash` 实际 exec `bash -c`（不是 `sh -c`）。** LLM 写脚本时高度依赖 bashism（`pipefail` / process substitution / `[[ … ]]` / 数组），降级到 dash 会静默坏；改 exec 路径同时把这点写进 tool description 和 baseline。
- **`bash` 的 `task_id` 协议收紧为 tool-owned。** `task_id` 由工具在 `run_in_background=true` 时生成 + 返回，LLM 只能原样传回来轮询，不能编造，也不能跟 `command` 同时给（互斥违反直接 error 出来并解释）。`internal/tools/local/local_test.go` 加了一个 `TestBashTaskIDMutualExclusion` 钉住这条规则。
- **`webfetch` 改成 HTML → Markdown（首次也是唯一一次第三方依赖）。** 原来 regex strip 成 plain text 把结构全丢了；现在借 `github.com/JohannesKaufmann/html-to-markdown/v2` 把标题 / 列表 / 链接 / 代码块都保下来。这是 M6b 二进制**唯一**的非 stdlib 库（不是 agent framework，只是一个工具库），M0/M6b 关于「无 third-party agent SDK 依赖」的承诺仍然成立。`internal/tools/local/webfetch_internal_test.go` 通过 `httptest` server 端到端跑「markdown 里要有 `# Title`、`[link](https://example.com)`、fenced code；同时 script/style/comment 内容要被清掉」，方便将来换库时回归保护。
- **devcontainer 调整。** Go 镜像锁到 `mcr.microsoft.com/devcontainers/go:1.26-bookworm`，docker-outside-of-docker feature 简化为默认配置 —— 给 M6c runner 在容器里跑容器留底盘。

### M6c — Runner & 容器底盘 ✅（**协议详见 [docs/runner-protocol.md](docs/runner-protocol.md)**）

把 agent 的部署 / 执行 / 凭证供给立起来：独立的 `hangrix-runner` 二进制以 outbound-only HTTP 长轮询连服务端，按 session 拉容器、bind-mount agent binary、注入凭证、转发 stdin/stdout。Agent 在容器里直接用 `git` CLI（平台**不**提供 `repo.tree` / `branch.commit` 这类 git 包装工具）。

**核心模型：**

- **Runner 节点：** 独立进程，部署在任何能跑容器的机器上；可见度分 platform / user 两级；自报 capabilities，server 按"可见度 + 容量"选 runner（user 级注册入口留到 M7a，DB 列已支持）。
- **Agent 容器：** 一 session 一隔离容器；`hangrix-agent` 二进制由 runner bind-mount 注入（不打进镜像，升级即换二进制）；启动时注入完整 env（LLM endpoint + model + MCP endpoint + 统一 session token + business context）。
- **统一 session token：** 同一张 `hgxs_<...>` 同时鉴权 LLM proxy / 平台 MCP / git push；plaintext cryptobox-seal 在 `agent_sessions.session_token_sealed`，runner pollTasks 时一次性下行。Session 终态时 sealed 字段置 NULL。

**实现：** `modules/runner` 四张表（runners / agent_sessions / agent_session_messages / agent_session_inputs）+ runner 端 stdlib-only 二进制 + 服务端 `//go:embed` 分发 `hangrix-agent` 和 `hangrix-runner`。Infra 走 sqlc 生成的 `runnerdb`；stateless token 校验（`hgxr_` / `hgxs_`）住在 `modules/runner/service`，infra 只暴露 `Get*ByPrefix` 行查询。

**已经历的重构（与 M6a 一起 ship）：**

- Session token 从 `modules/llm_provider` 搬到 `modules/runner` —— token 跟 agent_session 一对一，不再绑 LLM provider/model（见 [docs/agent-identity.md](docs/agent-identity.md)）。
- `admin createSession` 请求体 `{provider_name, model}` 改为只要 `model`；provider 由 LLM proxy server-side 反查决定。
- Runner bootstrap 的 `llm_endpoint` 改为完整 `<base>/api/llm/v1`（之前是 base url，agent 端要自己拼路径）。
- 手写 SQL 替换为 sqlc 生成；validator 拆出 service 层（符合 persistence vs. service 关切分离）。

**退出条件（已通过）：** `apps/hangrix-runner/internal/loop/session_test.go` 用 `httptest.Server` + `FakeOrchestrator` 跑：markRunning 命中 → pollInputs 喂 history + mock event → fake agent emit 四帧 stdout → runner POST 回 messages → terminate succeeded。整条 stdin/stdout/lifecycle 在 5 秒内闭环，不依赖真 docker。Real-docker / real-LLM 的端到端 smoke 留给 M7a。

### M7a — 多 role 基础设施

把 agent / role / team 立起来：识别 agent 仓库、解析 host yaml、起 per-role session、commit author 落 role key、audit 链跑通 —— **不接 mention 协议、不上完整工具集、不动 UI**。M7b 把协作层补齐，M7c 把 UI 和官方预设 agent 收尾。

**核心抽象：**

| 概念 | 定义 |
|---|---|
| **Agent** | 一个 Hangrix 仓库（根目录有 `agent.yml`），含 base prompt / 声明的工具集 / 元数据 |
| **Role** | host 仓库 `.hangrix/agents.yml` 里的本地标签 = agent 引用 + 触发器 + 工具白名单 + scope hint + 可选 host prompt addendum + mention 授权 |
| **Team** | 一个 issue 上所有已激活 role sessions 的集合（取代原“1 issue 1 session”）|
| **Mention** | `@agent-<role-key>` 评论语法，是唯一的 role 唤醒方式（协议本身在 M7b 实现）|

Agent 仓库结构、`agent.yml` 清单、host `.hangrix/agents.yml` schema 全部字段语义、Session 模型、Identity & Audit 落表细节，统一住 [docs/agent-config.md](docs/agent-config.md) —— 避免在 ROADMAP 里把 YAML schema 当 spec 维护。

工程拆成两个 phase：**Phase 1** 把 schema / 解析 / 仓库识别 / bundle 分发的地基铺好（已落地，详见下面"Phase 1 已通过"），**Phase 2** 接上 issue 生命周期 + session 编排 + identity + end-to-end 退出条件（仍在进行）。两个 phase 之间无外部接口耦合 —— Phase 2 拿 Phase 1 输出的 `agentsconfig` 解析结果 + `repos.kind` 列 + `/api/runner/agent-bundles` 端点直接拼装，不需要回头改 schema。

#### Phase 1 — Schema / 解析 / 仓库识别 / Bundle 分发（已通过）

- **`internal/agentsconfig` 包**：纯函数解析器，住在 `internal/` 而不是 `internal/modules/`（不是业务模块，是工具库；跟 `app/` / `config/` / `database/` / `web/` 平级）。覆盖 `agent.yml` / `.hangrix/agents.yml` / `.hangrix/agents.lock` 三份 schema，`KnownFields(true)` 严格拒未知键，agent.yml 拒 container/env/secrets/volumes/llm/image/build/roles 字段（原则 7）。`yaml.v3` 只在解码层用，types 包外不可见。**95.1% 测试覆盖**，14 个 agent-manifest 错误 / 26 个 host-config 错误 / 7 个 lock-file 解析错误 / 9 个 ref 错误全部 table-driven 钉住。
- **Repo `kind` discriminator**：迁移 `00004_repo_kind.sql` 加 `repos.kind TEXT NOT NULL DEFAULT 'standard' CHECK (kind IN ('standard', 'agent'))` + 部分索引 `WHERE kind='agent'`。`domain.Kind` + `Store.UpdateKind` + 列表 API 加 `?kind=agent|standard` 过滤。**Push 侧自动检测**：receive-pack post-receive 钩子读 `<default_branch>:agent.yml`（`git cat-file -p`）→ 调 `agentsconfig.ParseAgentManifest` → 通过则置 `KindAgent`，文件不存在或解析失败均退回 `KindStandard`（坏文件不锁仓库 push，让 owner 能修）。
- **Runner schema 演进**：迁移 `00002_agent_repo_and_snapshot.sql` 把 `agent_sessions.bundle_dir` 改名 `agent_repo`（语义从"runner 主机路径"换成"`<owner>/<name>@<sha>`"），加 5 列 snapshot：`agent_sha` / `repo_sha` / `role_key` / `cause_kind` / `cause_id` / `role_config JSONB`，状态机加 `idle` / `archived`（Phase 2 用）。`CreateSessionInput` / `taskResp` / `client.Task` 全链路传递新字段；M6c 老 admin smoke 路径继续工作（snapshot 字段允许空字符串）。
- **Agent bundle 分发**：服务端 `GET /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz`（`hgxr_` 鉴权），buffer-then-stream 出 `git archive --format=tar` 经过去时间戳的 gzip（`gz.Name/Comment/Extra/ModTime` 全零），`X-Hangrix-SHA256` 头给 runner 端校验，`Cache-Control: immutable`。Runner 端 `internal/bundles` 包：content-addressed 缓存（默认 1 GiB / 14 天），单 sha 单 flight 共享下载，atomic mkdir-tmp-rename inflation，**两段** sha256 校验（body vs 请求 sha AND body vs header），symlink-rejecting tar 提取，LRU + 容量 GC。`SessionDriver` 通过新的 `BundleResolver` 接口拿到 host 路径再交给 orchestrator bind-mount。
- **架构清理**（Phase 1 完工时打磨）：`isUniqueViolation` 从 5 个 infra/ 拷贝抽到 `internal/database.IsUniqueViolation`；`writeJSON` / `writeError` / `parseID` 从 9 个 handler/ 拷贝抽到 `internal/httpx`；删掉 `agentsconfig.Parser` 转发壳子（纯过度抽象，consumer 直接调包级函数）；删掉 `refreshRepoKind` 里 `agentsParser != nil` 的防御性回退（生产 ioc 必定注入，回退本身违反原则 7）。
- **测试覆盖（Phase 1 新增）**：`apps/hangrix/internal/modules/runner/handler/bundle_test.go`（8 case：happy path 用真 `git archive` + 真 tarball 解包验内容、byte-level 确定性跨调用、4 个 404 路径、2 个 400 路径）；`apps/hangrix/internal/modules/repo/handler/git_http_kind_test.go`（6 case：无 agent.yml→standard、合法 manifest→agent、**含 forbidden 字段→ standard**、坏 YAML→ standard、空 default branch→no-op、store 错误吞掉）；`apps/hangrix-runner/internal/bundles/bundles_test.go`（4 case，含 single-flight 并发协调 + 跨 sha 数据混淆检测 —— 这一条最初我没加，跑测试时才发现 cache 只校验 body↔header 而没校验 body↔请求 sha，立刻补上）。

#### Phase 2 — 生命周期 / 编排 / Identity / 端到端（进行中）

- [ ] **`modules/agent_session`**：per-role session 表 + 状态机（**复用 Phase 1 已加的 `idle` / `archived` 列**）；`issue.opened` 钩入生命周期，`issue.closed` / `issue.merged` 时全部 session 同步 `archived`（**无人工 archive** —— admin 停 agent 的力度是禁用 agent 仓库或从 host yaml 删 role，不是逐 session 戳）。**Snapshot 冻结**：session spawn 那一刻把 `agent_sha` + `repo_sha` + 解析后的 role 配置（prompt / can / llm / container spec）拍照进 Phase 1 已加的 snapshot 列，整 session 生命周期内不再重读 host yaml；同 issue 不同 role 各自冻结自己的 sha，中途新加的 role 在第一次被唤醒时拍自己的照。详见 [docs/agent-config.md](docs/agent-config.md)。
- [ ] **Session spawn 编排**：host yaml 解析（用 `agentsconfig.ParseHostConfig`）→ 选 runner（visibility + 容量）→ 解析 agent ref → sha（lock file，`agentsconfig.ParseLockFile` + 占位 resolver）→ 校验目标是 `kind=agent` 仓库（pre-spawn）→ 准备容器（pull image 或 build）→ 注入 M6 凭证 + 缓存好的 LLM 配置 → 写 session 行（snapshot 完整填充）→ 启动容器。
- [ ] **Identity 落地**：commit author = role key、email = `<role-key>@agents.<host-domain>`，receive-pack 路径下接受这种 author 写入。
- [ ] **Audit 链路**：role / agent_ref / agent_sha / repo_sha / session uuid / cause_id 落审计表（snapshot 列 Phase 1 已就位，缺一个跨 session 的查询视图）。
- [ ] **端到端退出条件**：写一份 `.hangrix/agents.yml` 声明一个测试 role + image 容器 + LLM 配置 → 开 issue → 该 role session 自动起、容器在某 runner 上拉起 → 容器内 agent 用本地工具完成 `git clone` + 改文件 + commit + push（author 显示为 role key）→ audit log 完整记录 session + agent_sha + repo_sha + cause `comment_id` → 关 issue → session 归档容器回收。**平台工具（`issue_*` / `roster_*`）在 M7b 起才真正可用**；M7a 不接 mention 路由、不动 UI。

### M7b — Mention 协议、完整工具集、事件总线

骨架立稳后铺协作层：mention 解析 + 完整工具集 + 事件总线 + 三层分发架构。让多 role 真正能协作（dispatcher 路由 + reviewer 投票 + maintainer 合并），但 UI 还是 M4 的单一时间线（swim-lane 留给 M7c）。

#### Mention 协议

- 语法：`@agent-<role-key>`（如 `@agent-backend`）。`agent-` 前缀预留未来人类 `@<username>` 不撞名。
- 评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块与引用块。匹配到的 role key 去 base 分支的 `.hangrix/agents.yml` 查；存在且通过 `mention_by` 校验则投递 `issue.comment.mentioned` 事件给该 role。
- 同评论 @ 多个 role 投递 N 个独立事件（同 comment_id），各 role 串自己的流。
- 人类直接 `@agent-backend please fix X` 跟 dispatcher 发同样评论效果完全一致 —— "评论 + mention"是人、dispatcher、其它 agent 三方共用的同一协议，没有第二种唤醒方式。

#### 工具集（v1）

| 工具 | 含义 | 典型持有者 |
|---|---|---|
| `issue_read` | 读时间线 | 几乎所有 role |
| `issue_diff` | issue 分支 vs base 的 diff | coder / reviewer / maintainer |
| `issue_children` | 列 sub-issue | dispatcher / maintainer |
| `issue_checks` | 当前 issue 所有 check 的最新 state（M8 起填充）| maintainer |
| `issue_comment` | 留言 | 几乎所有 role |
| `issue_review_vote` | 投票（approve / request_changes / abstain）→ 结构化事件 | reviewer |
| `issue_merge` | 合并到 base —— 默认无人能调，仅显式 `can:` 授权 | maintainer |
| `issue_close` | 关 issue | maintainer / dispatcher |
| `roster_list` | 列当前 team 已激活 role | dispatcher |
| `read` / `write` / `edit` / `glob` / `grep` | 本地文件读写 / 查找 / 替换（语义参考 Claude Code 同名工具） | 任何动代码的 role |
| `bash` | 容器内执行 shell（含 `git` / 测试 / 包管理）；`{command, working_dir?, timeout_seconds?, run_in_background?}` → `{stdout, stderr, exit_code, timed_out}` | 任何需要跑命令的 role |
| `webfetch` | 拉远端 URL，默认 HTML → markdown，`raw=true` 取原始 body | 需要查文档 / 外部 API 的 role |

**两种工具来源**：

- **本地工具**（`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`）—— 由 M6b 的 `hangrix-agent` 二进制内置，容器内 in-process 执行，**不经过 HTTP**。
- **平台工具**（`issue_*` / `roster_*`）—— 由 M7b 的 `modules/platform_mcp` 通过 **HTTP MCP server** 暴露在 `/api/mcp/v1/`，agent 用 MCP client 调；session-scope bearer token 鉴权。

git 凭证由 M6c runner 在容器启动时预配置（credential helper），agent 用 `bash` 调 `git push` 即可 —— **没有 git 专用工具**。

#### 事件集（v1，平台事件总线）

| 事件 | 时机 |
|---|---|
| `issue.opened` | 新 issue |
| `issue.closed` | issue 关闭 |
| `issue.comment.any` | 任何评论入时间线 |
| `issue.comment.mentioned` | 评论 @ 了某 role（每个 mentioned role 一个独立事件） |
| `commit.pushed` | issue 分支收到 push |
| `review_vote.posted` | 某 reviewer 投票 |
| `ci.status_changed` | CI 状态变化（由 M8 CI 子系统产生）|

#### 事件的三层分发

平台 schema 只定义第 1 层（事件总线 JSON payload），各层消费者分工：

```
[平台事件总线]                ← schema 在这一层（structured JSON，命名严谨、可版本化）
    ├→ [M6c runner → M6b agent stdin]  翻译成 agent 输入事件 → agent 看到
    ├→ [时间线 UI]            渲染成 swim-lane 条目     → 人类看到
    ├→ [audit log]            原样落表                  → 事后查询
    └→ [外部 webhook]         原样投递（M10+）          → 第三方
```

Agent stdin 看到的具体 prose 格式由 M6b 的 `hangrix-agent` 内部 `internal/prompt` + `internal/ipc` 决定，**不进 schema 也不在 ROADMAP 锁**。事件 payload 保留所有下游消费者可能要的字段（如 `comment_id` agent 用不上但 audit / webhook 要 —— **schema 偏全，adapter 偏简**）。

#### 需要做的

- [ ] **Mention 解析**：评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块和引用块。匹配 role key → 查 host yaml → 通过 `mention_by` 校验 → 投递 `issue.comment.mentioned` 事件。匹配但不通过校验的 chip 信息落 metadata（UI 在 M7c 渲染）。
- [ ] **`issue_comments.mentioned_roles JSONB[]` 列** + 入库时填充。
- [ ] **平台事件总线**：定义 v1 事件 payload schema（structured JSON），落 `event_log` 表 + in-process 分发到多 consumer（M6c runner 喂 agent stdin / audit log / UI 监听 / webhook stub）。
- [ ] **`modules/platform_mcp`**：HTTP MCP server，路径 `/api/mcp/v1/`（`tools/list` + `tools/call` 等 MCP 标准 RPC）。`Authorization: Bearer $HANGRIX_SESSION_TOKEN` —— 这跟 LLM proxy / git push 共用的同一张 session token；`tools/list` 返回当前 session role 的 `can:` 与已激活平台工具的交集；`tools/call` 走对应平台 handler。
- [ ] **平台工具实现**：`issue_diff` / `issue_children` / `issue_checks`（M8 前返空）/ `issue_review_vote` / `issue_merge` / `issue_close` / `roster_list` —— 每个都在 platform MCP server 后面挂 handler。本地工具（`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`）由 M6b agent 自带，**不走 MCP server**。
- [ ] **Agent ↔ 平台事件桥接**：M6c runner 把平台事件总线的事件翻译成 M6b agent 的 stdin JSON-Lines；反向把 agent stdout 的 tool call 报告写回事件总线 + audit（注：tool call 的执行已由 agent 自己经 MCP 完成，runner 这里只是落审计 + 转发状态）。
- [ ] **结构化事件 payload schema**：
  - `issue_review_vote`: `{state: approve|request_changes|abstain, body?: text}`
  - `ci.status_changed`: `{check_name, state, url?}`（M8 才有产生方，M7b 先定 schema 留口）

#### 退出条件

基于 M7a 的 host yaml，加上 dispatcher / backend / reviewer / maintainer 四个 role → 开 issue「加 health check 端点」→ dispatcher 自动起、调 `issue_comment` 发 `@agent-backend please add /healthz` → backend session 自动唤醒（无人手动触发）、写代码 + push → reviewer 因 `commit.pushed` 自动唤醒、调 `issue_review_vote` 投 approve → maintainer 因 `review_vote.posted` 唤醒、调 `issue_checks`（返空，stub 接受）+ `issue_merge` 合并 → issue 自动转 `merged`。**全程通过现有 M4 timeline tab 可见**（没有 swim-lane）。

### M7c — 前端 swim-lane + 平台预设 agent

最后一步把体验做出来：issue 详情页两个 tab + role swim-lane + mention chip + admin agent 仓库页；同时 ship 两个官方 agent 仓库让用户开盒即用。

#### 前端

- **Issue 详情页两个 tab：「智能体」/「人类」**。
- 「智能体」tab 按 role 分 swim-lane（dispatcher / backend / frontend / reviewer / maintainer / ...），每 role 一列；tool call / commit 触发 / 消息分块、可折叠。
- 「人类」tab：人类评论 + 真正由人触发的事件（人类 `git push` / 人类手动 merge 等）。
- 评论里的 `@agent-<role>` 渲染成 chip；点击跳到对应 swim-lane 当前位置；被 mention 的事件在 receiving role 的 swim-lane 起一段新工作。`mention_by` 校验未通过的 chip 灰色显示"未触发"。
- 两 tab 共享时间轴坐标，可跨 tab 跳同一时刻。
- Admin 后台新增 **"agent 仓库"列表 + 平台禁用表**视图，与"用户管理"并列。

#### 平台预设

Installer 首次启动 seed 两份官方 agent 仓库（进 db + 磁盘，owner 是 platform admin）：

- `hangrix/dispatcher` —— 路由器，识别任务 → @ 相关 role
- `hangrix/maintainer` —— 看 review / CI 状态 → 决定何时调 `issue_merge`；自带一份保守的 merge policy（要 reviewer approval + CI green + 文档可自合并），host 用 prompt addendum 调整。

其它角色（`coder` / `reviewer` 等）由用户自己写或装第三方。两个官方 agent 仓库后续走正常 git 流程升级 —— 平台吃自己的狗粮。

#### 需要做的

- [ ] **Issue 详情页改两 tab**：「智能体」/「人类」替换 M4 的单一时间线；两 tab 共享时间轴坐标。
- [ ] **「智能体」tab swim-lane**：按 role 分列；每 role 的 tool call / commit / 消息渲染为可折叠 block；跨 swim-lane 跳转。
- [ ] **「人类」tab**：人类评论 + 真正由人触发的事件；过滤掉 agent 的高频 tool call 噪音。
- [ ] **Mention chip 渲染**：评论体内 `@agent-<role>` 高亮 + 可点击跳 swim-lane；未通过 `mention_by` 校验的 chip 灰色显示。
- [ ] **Admin "agent 仓库" 列表**：列出系统内识别为 agent 的仓库 + 平台禁用表 + 启用 / 禁用开关，跟"用户管理"并列。
- [ ] **Installer seed `hangrix/dispatcher` + `hangrix/maintainer`**：首次启动 seed 进 db + 磁盘，带 base prompt + agent.yml + README。
- [ ] **`hangrix/maintainer` 的默认 merge prompt**：内置一份保守 merge policy。

#### 退出条件

用户在自己仓库写 host yaml 引用 `hangrix/dispatcher@latest` + `hangrix/maintainer@latest` + 自己写的 coder / reviewer → 开 issue → 全程在 UI 上看 swim-lane 流动：dispatcher 派单到 backend → backend 写代码 push → reviewer 投票 → maintainer 合并。「人类」tab 干净（只有用户开 issue 那条）；「智能体」tab 完整展示协作过程；admin 后台可看到三个 agent 仓库注册；`git log` 看到不同 role 的 commit author 区分。

### M8 — CI / Workflow 子系统

独立于 agent 协作的检查执行系统，类比 GitHub Actions。M7 通过两个接口跟 CI 协作：

- 平台事件 **`ci.status_changed`**（多 check 支持，每个 `(issue_id, commit_sha, check_name)` 一条独立状态：`green | red | pending | skipped`）
- 工具 **`issue_checks`**（maintainer 一次性拿到当前 issue 所有 check 的最新 state，决定是否 merge）

完整设计在本 milestone 展开 —— 包含 workflow 定义文件位置（候选 `.hangrix/workflows/*.yml`）、trigger 模型、job runner 是否复用 M6 的 agent runner pool、check 数据模型与 panel UI、credential 注入路径等。详细规划等 M7 落地后再拆。

### M9 — 围绕 AI 重塑 issue 体验

把 M7 的 agent 能力反过来打磨 issue 自身——这是 issue 真正"AI-Native"的部分，不只是把 chat 嵌进来。

- [ ] **结构化的 agent 时间线视图**：把 agent 的 tool call、思考、commit、问题分成可折叠的块，不要单纯流式文本。用户能快速扫到"agent 改了什么 / 卡在哪 / 在等我什么"。
- [ ] **Diff 的 AI 视角**：issue diff tab 除了行级 diff，提供按"意图块"分组的视图（agent 生成时附带语义标签）。人类直接 push 的 commit 退化到普通行级视图。
- [ ] **语义检索**：仓库级 embedding 索引（增量更新），同时服务于人类的代码搜索框和 agent 的 `repo.search` 工具。**索引层只做一个。**
- [ ] **Inline action**：在 issue diff 的某一行上一键让 agent "改这段 / 解释这段 / 补测试"；新的 agent 输入挂在同一 session 上。
- [ ] **Review agent**：一类特殊 agent，被某 issue 邀请后只发表结构化 review（不直接 commit），review 也是 issue 时间线里的事件，不是另一种实体。
- [ ] **Issue 模板与意图引导**：开 issue 时引导用户写"想达成什么"而不是"在哪行代码"，因为 agent 更擅长前者。模板可仓库级配置。

退出条件：用户在平台上的日常路径是"开 issue → 和 agent 来回几轮 → merge"，绕开 agent 反而更费劲。

### M10+ — 待定

候选方向，下一阶段再裁剪：

- SSO（org 成员的统一登录；M5 的 org 已经把成员模型立起来，SSO 是把"成员加入"换成"外部 IdP 决定"）
- Federation / mirror
- 桌面客户端（已有单二进制形态，做包装就行）
- Team / sub-group（M5 的 org 只有 owner / member 两档，真出现需要按子组分权的场景时再加 `teams` + `team_members` + `repo_team_access`）
- Outside collaborator / 多协作者（M3 的 owner-only 模型显出局限时再做 —— 跟 M5 的 org member 是两种正交关系）
- 外部 webhook 订阅（让事件总线对第三方开放）
- LLM 成本追踪 dashboard + per-host 配额（M6a 的 LLM proxy 已经在记用量表，到这一步是把它做成可视化 + 限额）
- User-BYOK（用户带自己的 API key 给某些 role 用）
- 更多 LLM provider 类型（Google Vertex / AWS Bedrock / Azure OpenAI 等需要自己签名协议的，单独翻译层）

## 不在路线图内的事

- **不做独立的 PR / review / discussion 实体。** 这些都是 issue 的不同切面，不再单独建模。如果将来真有需求把 review 拆出来，要先证明它在 issue 内做不了。
- **不允许游离的分支（M4 起强制）。** 产品稳态下任何非 default 分支必须挂在某个 issue 下，push 到没有 issue 的分支会被拒。**M3 是过渡期，允许直接 push**——M4 引入 issue 后 `BranchWriteGuard` hook 切换实现，把该规则强制开起来。
- **不做 GitHub/GitLab 的功能补全。** 缺什么功能要先回答"AI agent 怎么用它"；只对人有用的功能优先级靠后。M3 的 push / 分支 / 设置 / compare / README 渲染是 "agent 需要这些 git 抽象作为底座" 的最小集合，而不是为了对标其他平台。
- **不做通用 LLM 中台。** 平台只负责把 git 能力以工具形态暴露给 agent，不替 agent 选模型、不做 prompt 编排。
- **不做无沙箱的 agent 自治。** agent 跑在 runner 节点的隔离容器里，凭证（**一张统一的 session token**，同时鉴权 LLM / MCP / git push）按 session 维度一次性下发、过期回收；admin 能一键吊销某 agent 或关闭某仓库的自动 session。**agent 可以直接 commit / merge**（M7 起）—— 安全靠"可见 + 可停 + 可 revert"的事后约束，不靠"先批准再做"的事前门禁。
- **不让 agent 复用 users 表。** users 表只代表人类。Agent identity 走独立路径（M7 的 agent-as-repo）：commit author 是 role key、认证靠 runner 注入的 session 范围凭证、admin 视图是"agent 仓库列表 + 平台禁用表"——避免账号系统在 password / 邮箱 / 登录态这些地方对人和 agent 拧着说。
- **权限模型刻意简单。** 平台层只用 `user / admin`；M5 给 org 加 `owner / member` 二档（仅 org scope）—— 不引入 team / outside collaborator / repo-level role。仓库 owner 通过 handler 内部判断处理（`ResolveOwner` 决定走 user 还是 org，user 是 org 成员即视为该 org 仓库的 owner）。再细粒度的 ACL 真到必需时再设计，不预留字段。M3 的"分支保护规则"和 M7 的"role / can / mention_by"都是 repo-local 配置，**不是** user 级 RBAC。
- **不做 SSH 协议。** 本地优先 + agent-native 的形态下，HTTP + PAT 已经覆盖所有 git push/pull / API 调用场景：浏览器、git CLI、agent runtime 共用一种凭证模型。SSH 要再维护一套 key 管理 + auth_keys 配置 + 端口暴露，回报很低；用户真需要 SSH 体验时大概率说明用错了产品（应该选 Gitea / GitHub），不是该补的功能。
- **不做 Git LFS。** Hangrix 的主轴是 "agent 在 issue 里读 / 改 / 评论代码"——大文件（视频、模型权重、设计稿二进制）对 agent 工作流没有价值，agent 既不能 diff 也不能 patch。引入 LFS 意味着 storage 后端、pointer 文件协议、独立鉴权三层额外复杂度，且会鼓励用户把不该入 git 的资产塞进来。需要存大文件的项目应当外挂对象存储 + 在 issue 里引用链接。

## 工程基线（贯穿所有里程碑）

- 每个新功能走 `internal/modules/<name>/` 模块化单体约定（见 [AGENTS.md](AGENTS.md)）；跨模块依赖只能通过 ioc 容器和对方 `domain/` 接口。
- 所有 HTTP handler 和 agent 工具共用同一层 domain 接口；禁止 agent-only 或 UI-only 的 fast path 绕过 domain。
- 数据库变更走 goose 迁移（`internal/modules/<name>/infra/migrations/<NNNNN>_<name>.sql`），向前可应用、向后有 Down，禁止改老迁移。
- audit log、agent task log 是产品功能，不是运维日志，从 M6 起就要落库可查询。
