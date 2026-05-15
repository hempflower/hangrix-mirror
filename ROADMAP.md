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

## 当前状态（M5 完成）

**M5 全部闭环。** Owner 命名空间从「只有用户」扩成「用户 ∪ 组织」：一个 organization 是独立的 owner 实体，能拥有仓库、有成员、有自己的 profile 和 settings 页。所有 git / issue 能力对 org-owned 仓库无感复用 —— 仓库路由 `/{owner}/{name}` 的 `<owner>` 透明接收 user 用户名或 org 名，由 `ResolveOwner` 一次解析。M7 起官方预设 agent（`hangrix/dispatcher` / `hangrix/maintainer`）的稳定归属位现在可以坐实。下一步沿 **M6a（LLM proxy）→ M6b（agent runtime）→ M6c（runner & 容器）** 把 agent 执行链路立起来。

已就绪：
- **脚手架（M0）**：Go 1.26 + Nuxt 4 单二进制；`pkg/ioc` DI；chi、viper、air、Turborepo。
- **账号基础设施（M1）**：用户 / 角色 / 会话 / admin 后台。
- **Git 内核（M2）**：`modules/git`（go-git 读封装）+ `modules/repo`（元数据 + bare repo）+ smart HTTP `git-upload-pack`。
- **Git 平台（M3 核心）**：`modules/token` PAT + `git-receive-pack` 写路径 + 分支 / Tag CRUD + 仓库设置 + Compare + README 渲染。`resolveRef` 透明 peel annotated tag。
- **协作辅助（M3 stretch）**：`branch_protections` 表 + `pre-receive` 钩子（force-push / delete 拦截）+ commit 包含查询 + archive 下载（zip / tar.gz）。
- **Issue 容器（M4）**：`modules/issue` 完整模块 —— Issue / Comment / Event 三张表 + `issue_counters` per-repo 单调编号 + sub-issue（parent_id / parent_number）。Issue API：list / create / patch / merge / sync / timeline / diff / commits / children + 评论 create / list（**评论删除已撤回**——issue 时间线只追加不删除，删除按钮和后端路由都已移除）。**写入收紧**：`repodomain.BranchWriteGuard` + `PushObserver` 两个跨模块接口，issue 模块挂上去；`hangrix-issue-mode` sidecar 同步开放 issue 编号给 pre-receive 钩子，钩子里 base 锁定 + `issue/<n>` 校验双线生效；web API 的 `createBranch` / `deleteBranch` / receive-pack 也都跑同一份 guard。`MergeBranch` 三方合并实现进 `modules/git`（FF / merge-commit / up-to-date 三态 + 冲突哨兵 `ErrMergeConflict`）。前端：Issues 列表 / 详情 / 新建（含 `?parent=N` 子 issue 入口）；详情页 conversation + commits + diff 三 tab（tab 状态写进 `?tab=` URL 可分享 / 可回退）、GitHub 风格评论卡（avatar header strip + 相对时间 + tooltip 显示绝对时间戳）、评论 / 系统事件混排时间线、15s 自动刷新（hidden tab 自动暂停 —— 取代了原来的"手动 Sync 按钮"）、合并按钮、parent / children 侧栏 + 「Changes」(+N −M / files changed) 卡。FileDiffList 重写成 GitHub 风格：行号 gutter + emerald/red 行底 + sky hunk header + 折叠 + 每个文件 +N −M 徽章 + "view before / view after" blob 链接（commit 详情页和 issue diff tab 共用）。
- **组织（M5）**：`modules/org` 完整模块 —— `organizations` + `organization_members` 两张表 + 跨模块 `Resolver`（`ResolveOwner` / `Membership`）。`modules/repo` 重构成 owner_kind/owner_id 二元归属（user 或 org，DB CHECK 保证恰一；UNIQUE 拆 partial 索引按 kind 限定 name 唯一）。`POST /api/orgs` / 成员管理 / `POST /api/repos/{owner}/{name}/transfer` 全部就绪；transfer 走 DB swap + 磁盘 rename 的"先 DB 后磁盘 + 失败回滚 DB"策略。保留名（`admin` / `api` / `git` / `static` / `_` 等）+ 跨表 namespace 互斥校验（创建 user 撞 org 名 / 反之都返 409）。前端：导航栏「New organization」入口、`/orgs/new` 表单、`/{name}` 统一 profile 页（同一个 `[owner]/index.vue` 通过 `ResolveOwner` 渲染 user 或 org 视图）、`/{name}/settings` + `/{name}/settings/members`、新建仓库表单的 Owner 下拉、`/{owner}/{name}/settings` 的 Transfer 弹窗（type-to-confirm `<owner>/<name>`）。**Org visibility 字段在 ship 前主动撤回**：原计划 public / private 两档，落地后判断「私有 org 给非成员看什么」始终拐不出有意义的语义，干脆删列 + 删 UI，所有 org 一律登录可见，私密性靠仓库 visibility 兜底（见后文"计划外"）。
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

### M6a — LLM provider & proxy

平台第一步要能跟 LLM 说话。这一步**完全独立于 runner、agent、issue**：admin 配 provider → 平台跑代理 → 任何能发 HTTP 的客户端用 OpenAI SDK 都能调 → 用量落表。

> **为什么先做：** LLM proxy 是个零依赖的纯 HTTP 子系统，能用 curl 全验完。M6b 写 agent binary 时直接对着 proxy 写，不用同时调试两层；M6c 起 runner 才把它接进容器里。

核心模型：

- **Platform LLM 端点 registry**：admin-only 资源，配置多个 provider 实例（不同 type、不同 key、不同允许模型集）。
- **平台 LLM proxy**：HTTP 端点 `/api/llm/<provider_name>/v1/*`，OpenAI Response API 兼容前端。后端按 provider type 翻译到上游 —— agent 端零 provider 知识、平台拿完整观测面、master key 永远不进容器。
- **Provider 类型（v1 ship 三种）**：
  - `openai` —— OpenAI 原生 Response API，零翻译
  - `anthropic` —— OpenAI Response API ↔ Messages API 翻译层
  - `openai-compat` —— 转发到自定 `base_url`（覆盖 OpenRouter / vLLM / Ollama-via-openai / Together / Groq 等绝大多数第三方）

需要做的：

- [ ] **`modules/llm_provider`**：admin-only 资源。
  - 字段：name（唯一，进 URL path）、type（`openai` / `anthropic` / `openai-compat`）、base_url、api_key（db 加密）、allowed_models（list）、visibility（`platform | restricted`）、allowed_repos（restricted 时的 glob 列表）、rate_limit_rpm。
  - Admin 表单**必填**一个 platform default provider + default model —— host 仓库不写 `llm:` 时 fallback 到这组。
- [ ] **`modules/llm_proxy`**：HTTP 端点 `/api/llm/<provider_name>/v1/*`，OpenAI Response API 兼容。
  - 路由：`<provider_name>` 查 provider → 按 type 走翻译层（v1 ship `openai` 零翻译 / `anthropic` OpenAI↔Messages 翻译 / `openai-compat` 转自定 base_url）。
  - Auth：`Authorization: Bearer $HANGRIX_SESSION_TOKEN` 验三件事 —— token 绑 session + session role 的 `llm.provider` 跟 URL path 匹配 + 请求 body 的 model 在 provider `allowed_models` 里；任一失败 403。
  - 落用量表：每次请求记 session_id / role / token usage / latency / 错误码，供 M10+ 成本 dashboard 用。
- [ ] **测试用 session token 颁发**：M6a 阶段没有真 agent session，提供一个 admin API 颁发"假 session token"绑死到一对 (provider, model)，让 curl / 测试客户端能调 proxy 通完整鉴权链。这同一种 token 在 M7b 的 MCP server + M6c 的 git push 都通用，M6c 起这个测试口让位给真 session 颁发。

退出条件：admin 配一个 LLM provider（勾选 platform default）+ 用 admin API 颁一张测试 session token → 本机 `curl` 用任意 OpenAI 客户端连 `/api/llm/<provider_name>/v1/responses` 拿到一句模型响应 → proxy 路由到正确上游 + 鉴权三件事都验过 + 用量落表。

### M6b — Agent runtime（Go binary）

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
├── cmd/hangrix-agent/main.go        # 入口：读 env，初始化 loop
├── pkg/
│   ├── llm/                         # OpenAI Response API HTTP 客户端（hand-rolled）
│   ├── mcp/                         # HTTP MCP client（连平台 MCP server 拉 issue.* / roster.*）
│   ├── tools/                       # 工具注册表 —— 本地工具 in-process + 平台工具走 pkg/mcp
│   │   └── local/                   # read / write / edit / glob / grep / bash / webfetch 本地实现
│   ├── prompt/                      # 三层 system prompt 拼装
│   │   ├── baseline.md              # 内置 runtime baseline（//go:embed）
│   │   └── assemble.go              # baseline + agent base_prompt + host addendum 叠加
│   ├── runtime/                     # 主循环 + 上下文管理
│   └── ipc/                         # runner 通信（stdin/stdout JSON-Lines）
└── go.mod
```

工具分两类，LLM 看到的是一个扁平 function-call 列表：

- **本地工具**（agent binary 内置，容器内执行）：`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`，参考 Claude Code 同名工具的语义（`read` 行号 + offset/limit / `edit` 强制先读 / `bash` 含 stdout-stderr-exit_code / `grep` 用 ripgrep / 等）。Agent 不包装 git —— 仓库操作（clone / pull --rebase / commit / push）由 LLM 通过 `bash` 直接调 `/usr/bin/git`，凭证由 M6c runner 在容器启动时通过 credential helper 预配置。
- **平台工具**（Hangrix 平台通过 HTTP MCP 暴露）：`issue.*` / `roster.*`，agent 用 `pkg/mcp` 的标准 MCP client 调远端服务端点（M7b 起 ship 的平台 MCP server）。M6b 期间这类工具走 stub —— 没有真平台 MCP server 也能跑本地工具自检。

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
{"kind": "tool_call", "name": "issue.comment", "args": {...}, "result": {...}, "tool_call_id": "tc-1"}
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

#### 需要做的

- [ ] **`pkg/llm`**：手写 OpenAI Response API HTTP 客户端 + SSE 流解析（增量 token / tool call delta）+ exponential backoff retry。**不引第三方 LLM SDK**。
- [ ] **`pkg/mcp`**：HTTP MCP client。`Authorization: Bearer $HANGRIX_SESSION_TOKEN`，连 `$HANGRIX_PLATFORM_MCP_ENDPOINT`，启动时调 `tools/list` 拿当前 session 可见的平台工具列表，运行时调 `tools/call` 触发。失败 / 超时按 exponential backoff 重试。
- [ ] **`pkg/tools/local`**：本地工具实现，参考 Claude Code 语义：
  - `read(path, offset?, limit?)` —— UTF-8 文件 + 行号 prefix（`<n>\t<line>`），默认前 2000 行
  - `write(path, content, overwrite?)` —— 创建文件
  - `edit(path, mode, ...)` —— `replace` / `insert` / `delete` 三种 mode；强制要求同 session 先 `read` 过
  - `glob(pattern)` —— glob 文件发现，按 mtime 排序
  - `grep(pattern, path?, ...)` —— 优先 ripgrep + .gitignore-aware
  - `bash(command, working_dir?, timeout_seconds?, run_in_background?)` —— `{stdout, stderr, exit_code, timed_out}`；后台模式返 `taskId` 让 agent 后续轮询
  - `webfetch(url, raw?)` —— 默认 HTML → markdown；`raw=true` 取原始 body
- [ ] **`pkg/tools` registry**：合并本地 + 远端工具源，按 `HANGRIX_TOOL_CATALOG` 过滤；统一 `Call(name, args) → (result, error)` 签名；非法调用返 `ToolNotAllowed` 让 LLM 自我修正。Tool descriptor schema 跟 OpenAI function-calling 对齐，启动时生成给 `pkg/llm` 用。
- [ ] **"push 前自行 rebase"靠 prompt 教**：不在 agent 代码里做 git 重试 —— host prompt addendum 或 agent base prompt 写明"push 失败先 `git pull --rebase` 再重推"，LLM 通过 `bash` 完成。force-push 已被 receive-pack 钩子禁掉，自然逼着 agent 走这套流程。
- [ ] **`pkg/prompt/baseline.md`**：runtime 内置的 baseline prompt（通过 `//go:embed` 编译进二进制）—— 跟版本绑死、跨所有 agent 实例共用。内容覆盖以下要点：
  - **工具使用纪律**：`edit` 前必先 `read`；长输出靠 `grep` / `read offset+limit` 收口；`bash` 长任务用 `run_in_background`；用平台工具（`issue.*` / `roster.*`）做跨 role 协调而不是 `bash` 直连。
  - **Git 协作约定**：凭证已预配置（credential helper 用 `HANGRIX_SESSION_TOKEN` 作 HTTP Basic password），`git push` 直接走 HTTPS；force-push 被钩子拒，push 失败先 `git pull --rebase origin issue/<n>` 再重推；commit author 由 runner 强制，不要自己改 `--author`；不要切到其它分支（已 checkout 到 `HANGRIX_WORKING_BRANCH`）。
  - **行为约束**：不修改 `.hangrix/**` 除非任务明确要求；不写 secret 到任何文件 / commit；所有 tool call 落 audit，行为要 deliberate。
  - **环境感知**：工作目录是 `/workspace`、host repo 已在目标分支、平台 MCP 在 `$HANGRIX_PLATFORM_MCP_ENDPOINT`、LLM 在 `$HANGRIX_LLM_ENDPOINT`。
  - 这份内容是平台对所有 agent 的"操作系统约束"，agent 作者和 host 都不能在上层 prompt 里覆盖。
- [ ] **`pkg/prompt/assemble`**：按固定顺序叠三层 system prompt：
  ```
  <baseline.md 内置内容>                       ← runtime 强制纪律
  
  ===== AGENT BASE PROMPT =====
  <HANGRIX_AGENT_BUNDLE 里 entry.base_prompt 的文件内容>   ← agent 作者写的身份与方法论
  
  ===== HOST REPO ADDENDUM =====
  <HANGRIX_HOST_ADDENDUM 指向的文件内容>        ← host 给本次部署的微调
  ```
  外加 role / scope / branch / 工具描述等 runtime 上下文。**前一层不能被后一层 override**（schema 层面没法防，靠 baseline 提示词和 audit 兜底）。
- [ ] **`pkg/runtime/loop`**：主循环 + 上下文管理 + 单轮 token 上限 + 重试 + fatal error 处理。
- [ ] **会话历史回放**：启动时**先**读 stdin 上的 `{"kind": "history", ...}` 一帧（runner 保证作为首条），用它预填 `pkg/runtime/context` 的消息列表；之后才进入事件循环。新 session 时 `messages: []` 也合法。
- [ ] **新消息回报**：每次 LLM 返回 assistant 消息 / 每次 tool call + result 都通过 stdout 的 `message` / `tool_call` 实时写给 runner —— runner 是消息日志的唯一持久化者，agent 自己**不写盘**。
- [ ] **上下文窗口裁剪**：组装下一轮 LLM 请求时按"最近 N 条 + 总 token 上限"裁剪发送的消息（裁剪只影响发给 LLM 的内容，不丢日志）。具体策略 v1 用简单的尾窗口截断，更聪明的摘要 / RAG 留给 M9。
- [ ] **`pkg/ipc/jsonl`**：stdin 行读 + stdout 行写；事件、tool call、状态、日志四种结构化 message。
- [ ] **`cmd/hangrix-agent/main`**：env 解析 + 模块组装 + 信号处理（SIGTERM → graceful shutdown）。
- [ ] **冒烟测试**：本机起一个 mock HTTP MCP server（只实现 `tools/list` 返几个 stub 平台工具）+ `docker run -v $PWD/hangrix-agent:/agent -e HANGRIX_LLM_ENDPOINT=... -e HANGRIX_PLATFORM_MCP_ENDPOINT=... ... hangrix-agent` 注入 mock env + stdin 喂一条事件 → 完成一轮 LLM + 调本地工具（`bash` 跑 `git push` / `read` 读文件等）+ stdout 报 done。**M6b 期间只验本地工具 + MCP 客户端握手**，平台真工具实现在 M7b。

退出条件：M6a 配好一个 provider + 本机起 mock HTTP MCP server + sandbox host repo → 本机 `docker run` 启动 `hangrix-agent` + 注入完整 env + stdin 喂一条事件 → agent 拉系统 prompt + 调一次 LLM + 解析 tool call → 调本地工具（如 `bash git push` / `read` 看文件）+ 调 mock 平台工具（`tools/call`）成功 + stdout 报告 done。**纯 agent binary 验证，不依赖 runner、平台 MCP server 走 mock**。

### M6c — Runner & 容器底盘

把 agent 的部署 / 执行 / 凭证供给立起来。让 M6b 的 `hangrix-agent` 真正以"一个 runner 上一个隔离容器"的方式跑起来，凭证（**一张统一的 session token** 同时供 LLM proxy / 平台 MCP / git push 三处使用，外加 repo secrets）由 runner outbound 注入，目标 host repo 和 issue 由调度层指定。

核心模型：

- **Runner 节点**：独立进程，部署在任何能跑容器（Docker / containerd）的机器上，**outbound** 连接服务端注册并领任务（不要求 runner 暴露端口）。
  - 可见度分两级：
    - **平台级（platform）**：所有用户的 agent 都可调度到上面，由 admin 注册。
    - **用户级（user）**：只服务注册它的用户自己的 agent。
  - Runner 自报能力（CPU / 内存 / 并发上限 / 容器运行时），服务端按"可见度 + 容量"选 runner。
- **Agent 容器**：runner 为每个 agent session 拉起一个隔离容器。
  - **第一版固定一个容器镜像**（含 `git` CLI + 必要依赖），M7 起改成由 host 仓库 `.hangrix/agents.yml` 的 `container:` 块声明（image 或 build）。
  - **M6b 的 `hangrix-agent` 二进制 + 配套文件由 runner 通过 bind mount 注入到容器**，不打进镜像。agent 升级走 runner 拉新二进制即可，不重建镜像。
  - 容器启动时由 runner 注入完整 env（M6b 启动期约定那张表）—— LLM endpoint + 已 resolve model + 平台 MCP endpoint + **统一 session token**（同时鉴权 LLM / MCP / git push）+ repo secrets + 业务上下文。
- Agent 在容器里 **直接用 `git` CLI** 操作仓库（clone / commit / push）—— 平台 **不** 提供 `repo.tree` / `repo.read_file` / `branch.commit` 这类 git 包装工具。

需要做的：

- [ ] `modules/runner`：runner 实体（id、name、owner_user_id NULL=平台级、visibility `platform | user`、status、last_heartbeat_at、capabilities JSON、enroll_token_hash、created_at）。Admin 注册平台级；普通用户注册自己的 user 级。
- [ ] Runner enrollment：web 上点"新建 runner" → 拿一次性 enroll token → 目标机器跑 `hangrix runner enroll --token <...>` → runner 落盘长期凭证后 outbound 连回服务端。
- [ ] Runner ↔ 服务端协议（outbound-only，websocket 或 long-poll）：
  - 心跳（capabilities + 当前负载）
  - 任务下发：`agent.session.start` / `agent.session.stop` / `agent.session.input`
  - 事件上报：agent stdout 的 tool call / status / log / done 转发给平台事件总线 + audit
- [ ] **容器编排**：拉镜像（v1 固定）→ bind mount M6b 的 `hangrix-agent` binary → bind mount agent bundle 到 `HANGRIX_AGENT_BUNDLE` 路径（agent 仓库 pinned sha 解出的目录）→ **写 host prompt addendum 到容器内临时文件**（路径填到 `HANGRIX_HOST_ADDENDUM` env，**不假设 prompt 长度**）→ 注入完整 env（含 `HANGRIX_SESSION_TOKEN` 统一凭证）→ **预配置 git credential helper**（用 `HANGRIX_SESSION_TOKEN` 作 HTTP Basic password，让容器内任何 `git push` 走 HTTPS 推到目标 issue 分支）→ **预 clone host repo 到工作目录**（`/workspace`，checkout 到 `HANGRIX_WORKING_BRANCH`）→ 启动容器、attach stdio。
- [ ] **凭证调度**：服务端在 session 启动时为该次 session 颁发**一张统一的短期 session token**（同时鉴权 LLM proxy / 平台 MCP / git push 三处；token 跟 session 绑死、issue 关闭即过期、admin 可一键吊销）。三类资源各自的授权检查走服务端 session resolver：LLM proxy 查 provider+model 命中、MCP 查 `can:` 过滤、receive-pack 查目标分支限定 `issue/<n>`。token 本身不编码权限，只标识 session。
- [ ] **Agent 二进制分发**：runner 持有版本化的 `hangrix-agent` binary（从服务端拉），启动容器时 bind mount 到固定路径，容器入口直接 exec。
- [ ] **stdin / stdout 转发**：runner 把"平台事件 → agent stdin"和"agent stdout → 平台事件总线 + audit"两条管道接通；agent 退出码 / 异常上报。
- [ ] **会话历史持久化 + 回放**：
  - Runner 维护 per-session 消息日志（OpenAI Response API 格式的扁平消息数组，含 user 事件 / assistant 消息 / tool call + result / 系统事件混排，按时间排）。
  - Agent stdout 的 `message` / `tool_call` 实时 append 到日志；语义事件（commit / merge / review_vote）也插入到对应时间点。
  - **容器启动时**：runner 从平台读出该 session 的完整日志 → 转换成 `{"kind": "history", "messages": [...]}` → 作为首条 IPC 消息写到 agent stdin。新 session 也发，只是 `messages: []`。
  - 日志存储格式：v1 走 Postgres JSONB 列（`agent_session_messages` 表 per row 一条消息，或 `agent_session.messages JSONB[]`，按性能选）；session 归档时只标记，不删，留作 audit。
- [ ] **Session 任务参数模型**：M6c 的 runner 协议参数 = 容器镜像 + agent binary 版本 + 完整 env（M6b 那张表）+ 目标仓库 + 目标分支。M7a 起 agent-as-repo 解析层坐在这之上，把 host yaml + role 配置翻译成这组参数。

退出条件：admin 注册一个平台 runner → admin API 触发一次测试 session → runner 拉起容器、bind mount M6b 的 `hangrix-agent`、注入完整 env、把一条 mock `issue.comment.mentioned` 事件通过 stdin 喂进去 → 容器内 agent 完成一轮 LLM 调用 + tool call（`issue.comment`）+ `git push` + 通过 stdout 把 tool call / done 报回 runner → runner 转发到平台 audit + 事件总线。**全链路 verified，不碰 issue UI，不接 host yaml 解析**（那是 M7a）。

### M7a — 多 role 基础设施

把 agent / role / team 这套抽象立起来：识别 agent 仓库、解析 host yaml、起 per-role session、commit author 落 role key、audit 链跑通 —— **不接 mention 协议、不上完整工具集、不动 UI**。M7b 把协作层补齐，M7c 把 UI 和官方预设 agent 收尾。

#### 核心抽象

| 概念 | 定义 |
|---|---|
| **Agent** | 一个 Hangrix 仓库（根目录有 `agent.yml`），含 base prompt / 声明的工具集 / 元数据 |
| **Role** | host 仓库 `.hangrix/agents.yml` 里的本地标签 = agent 引用 + 触发器 + 工具白名单 + scope hint + 可选 host prompt addendum + mention 授权 |
| **Team** | 一个 issue 上所有已激活 role sessions 的集合（取代原"1 issue 1 session"）|
| **Mention** | `@agent-<role-key>` 评论语法，是唯一的 role 唤醒方式（协议本身在 M7b 实现）|

#### Agent-as-repo

Agent 以 Hangrix 仓库形态维护、版本化、复用 —— 跟代码仓库走同一套 visibility / git log / issue 流程。

仓库结构：

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
runtime: claude-agent-sdk
entry:
  base_prompt: prompts/system.md
declared_tools:                    # 推荐 / 文档，host 的 can: 才是真授权
  - issue.read
  - issue.comment
  - issue.review_vote
```

仓库识别：根目录有 `agent.yml` 即识别为 agent；可见度规则跟普通 repo 一致（public 谁都引、private 同 owner 仓库引）。`agent.yml` 的 schema **拒绝**任何镜像 / 环境 / secret 字段 —— 那是 host 仓库的事，落实原则 7。

#### Host 仓库配置：`.hangrix/agents.yml`

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

llm:                              # team 默认 LLM（可选；省略则走 admin 配的 platform default）
  provider: anthropic-prod        # 引用 admin 配的 provider name
  model: claude-sonnet-4-6

roles:
  dispatcher:
    agent: hangrix/dispatcher@v1.2.0
    triggers: [issue.opened, issue.comment.any]
    can: [issue.read, issue.comment, roster.list]

  backend:
    agent: acme/backend-coder@v0.3.1
    triggers: [issue.comment.mentioned]
    scope: { paths: ["apps/api/**", "internal/**"] }
    can:
      - issue.read
      - issue.diff
      - issue.comment
      - read                      # 本地文件读
      - write                     # 本地文件创建
      - edit                      # 本地文件改
      - glob                      # 找文件
      - grep                      # 搜内容
      - bash                      # 跑命令 / git push
    mention_by: collaborators
    prompt: |                     # host addendum，追加到 agent base prompt
      Always git pull --rebase against issue/<n> before push.
      Cross-module imports MUST go through pkg/ioc DI.

  reviewer:
    agent: hangrix/reviewer@v1.0.0
    triggers: [commit.pushed, issue.comment.mentioned]
    can:
      - issue.read
      - issue.diff
      - issue.comment
      - issue.review_vote
      - read                      # 看完整文件不仅是 diff
      - glob
      - grep
    mention_by: collaborators
    prompt_file: .hangrix/prompts/reviewer.md
    llm:                          # per-role 覆盖：reviewer 要更强推理
      model: claude-opus-4-7

  maintainer:
    agent: hangrix/maintainer@v1.0.0
    triggers: [review_vote.posted, ci.status_changed, commit.pushed, issue.comment.mentioned]
    can: [issue.read, issue.diff, issue.comment, issue.checks, issue.merge]
    mention_by: owner
    prompt: |
      Merge policy: ≥1 reviewer approval for apps/api/**, all required CI checks green,
      docs-only self-merge, "urgent"-labeled bypass review if CI green.
    llm:                          # 整组 provider+model 都换
      provider: openai-team-a
      model: gpt-5
```

字段语义：

- `agent: <owner>/<name>@<ref>` —— **必须有 `@<ref>`**（拒空 ref，避免跟着上游漂移）。`<ref>` 可以是 tag / branch / sha；lock 文件统一解析到 sha。
- `triggers:` —— 事件订阅。Dispatcher 通常订 `issue.comment.any`（充当路由器）；其它 role 订 `issue.comment.mentioned` 等待被 @。强制列具体事件。
- `can:` —— 平台工具白名单。Agent 仓库的 `declared_tools` 是文档，host 的 `can:` 才是真授权。
- `scope.paths:` —— 软约束（写进 role 的初始 prompt 让 dispatcher 知道分派给谁），不在 pre-receive 强制。
- `mention_by:` —— 谁的 @ 能唤醒：`owner` / `collaborators`（默认）/ `anyone`。违反者的 @-mention 进时间线但不投递事件，UI chip 灰色提示"未触发"。
- `prompt:` 或 `prompt_file:` —— host 给该 role 的 prompt addendum，**追加**到 agent base prompt 末尾。二选一（schema mutual exclusive）；`prompt_file:` 必须以 `.hangrix/prompts/` 开头。
- `llm:` —— 团队级 + per-role 两层；role 级覆盖团队级，团队级覆盖 admin 的 platform default。字段 `provider`（必须是 host 看得见的 provider name）+ `model`（必须在该 provider 的 `allowed_models` 里）+ 可选 `max_tokens` / `temperature` / `top_p`。Spawn session 时把"resolved provider + model"缓存到 session 元数据，runner 注入 env 时直接读。
- Runner 默认给每个 role 容器注入一张**统一的 session token**（同时鉴权 LLM proxy / 平台 MCP / git push，目标 issue 分支可写）—— **每个 role 默认有 push 权限**，无需显式配置。LLM endpoint + model 也是默认注入。

#### Session 模型

- **`modules/agent_session`**：一个 issue 内每个被唤醒过的 role 各一个 session（取代原"1 issue 1 session"）。
  - 字段：issue id、role key、agent_ref + agent_sha、host_prompt_sha、runtime_version、runner id、container id、状态（`pending | running | idle | archived | failed`）。
  - 配套 `agent_session_messages`（或 JSONB 数组）存完整对话历史 —— OpenAI Response API 风格的消息序列（user 事件 / assistant 消息 / tool call + result / 系统事件混排），按 `created_at` 排序；session 归档时只标记不删，长留作 audit。
  - **issue 关闭时全部 session 同步归档**，state → `archived`，容器回收。已归档不允许人为重启 —— 想继续就开新 issue。
- **单 role 单容器串行（v1）**：同 role 在同 issue 上同一时刻只跑一个容器，多 trigger 排队消化。多并发后续 milestone。
- **冲突自治**：多 role 同时在 `issue/<n>` 上工作时，agent push 前自行 `git pull --rebase`。force-push 已禁，push 失败自然逼迫先 rebase —— 无需平台层协调机制。

#### Identity 与 Audit

- commit author name = role key（如 `backend`），email = `<role-key>@agents.<host-domain>`
- 每次 session 启动落三件版本信息进 audit：
  - `runtime_version`：`hangrix-agent` 二进制版本（baseline prompt 跟它绑死）
  - `agent_sha`：agent 仓库 pinned 版本
  - `host_prompt_sha`：host 仓库 base 分支当时的 commit（含 `.hangrix/agents.yml` + `prompts/`）
- audit 完整链路：`role=backend → runtime=hangrix-agent@v1.2 → agent=acme/backend-coder@<sha> → host_prompt=<sha> → session=<uuid>`
- 任何 commit / merge 都能 trace 回 cause `comment_id` —— M4 时间线 append-only 审计流的覆盖面延伸到 agent 全部动作。三层 prompt 内容都可按 sha 精确复原。

#### 需要做的

- [ ] **Agent 仓库识别**：repo 模块加"根有 `agent.yml` 判定为 agent"逻辑；仓库列表 / 搜索过滤支持 `kind=agent`。`agent.yml` schema 校验拒容器 / 环境 / secret 字段（原则 7）。
- [ ] **`modules/agents_config`**：解析 host 仓库 `.hangrix/agents.yml`（container / env / secrets / volumes / llm / roles 各块）+ `.hangrix/agents.lock` 自动维护（agent ref → sha 解析）。
- [ ] **`modules/agent_session`**：per-role session 表 + 状态机；issue 创建 / 关闭事件钩进生命周期；session 启动时落 runtime_version / agent_sha / host_prompt_sha 进 audit。
- [ ] **Session spawn 编排**：host yaml 解析 → 选 runner（visibility + 容量）→ 准备容器（pull image 或 build）→ 注入 M6 凭证 + 缓存好的 LLM 配置 → 启动容器。
- [ ] **Identity 落地**：commit author = role key、email = `<role-key>@agents.<host-domain>`，receive-pack 路径下接受这种 author 写入。
- [ ] **Audit 链路**：role / agent_ref / agent_sha / host_prompt_sha / runtime_version / session uuid / cause_id 落审计表。

#### 退出条件

写一份 `.hangrix/agents.yml` 只声明一个测试 role（agent 指向自己仓库里临时塞的一个 `agent.yml`）+ image 容器 + LLM 配置 → 开 issue → 该 role session 自动起、容器在某 runner 上拉起 → 容器内 agent 用本地工具完成 `git clone` + 改文件 + `git commit` + `git push`（commit author 显示为 role key）→ audit log 完整记录 session + runtime_version + agent_sha + host_prompt_sha + cause `comment_id` → 关 issue → session 归档容器回收。**平台工具（`issue.*` / `roster.*`）在 M7b 起才真正可用**；M7a 不接 mention 路由、不动 UI。

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
| `issue.read` | 读时间线 | 几乎所有 role |
| `issue.diff` | issue 分支 vs base 的 diff | coder / reviewer / maintainer |
| `issue.children` | 列 sub-issue | dispatcher / maintainer |
| `issue.checks` | 当前 issue 所有 check 的最新 state（M8 起填充）| maintainer |
| `issue.comment` | 留言 | 几乎所有 role |
| `issue.review_vote` | 投票（approve / request_changes / abstain）→ 结构化事件 | reviewer |
| `issue.merge` | 合并到 base —— 默认无人能调，仅显式 `can:` 授权 | maintainer |
| `issue.close` | 关 issue | maintainer / dispatcher |
| `roster.list` | 列当前 team 已激活 role | dispatcher |
| `read` / `write` / `edit` / `glob` / `grep` | 本地文件读写 / 查找 / 替换（语义参考 Claude Code 同名工具） | 任何动代码的 role |
| `bash` | 容器内执行 shell（含 `git` / 测试 / 包管理）；`{command, working_dir?, timeout_seconds?, run_in_background?}` → `{stdout, stderr, exit_code, timed_out}` | 任何需要跑命令的 role |
| `webfetch` | 拉远端 URL，默认 HTML → markdown，`raw=true` 取原始 body | 需要查文档 / 外部 API 的 role |

**两种工具来源**：

- **本地工具**（`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`）—— 由 M6b 的 `hangrix-agent` 二进制内置，容器内 in-process 执行，**不经过 HTTP**。
- **平台工具**（`issue.*` / `roster.*`）—— 由 M7b 的 `modules/platform_mcp` 通过 **HTTP MCP server** 暴露在 `/api/mcp/v1/`，agent 用 MCP client 调；session-scope bearer token 鉴权。

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

Agent stdin 看到的具体 prose 格式由 M6b 的 `hangrix-agent` 内部 `pkg/prompt` + `pkg/ipc` 决定，**不进 schema 也不在 ROADMAP 锁**。事件 payload 保留所有下游消费者可能要的字段（如 `comment_id` agent 用不上但 audit / webhook 要 —— **schema 偏全，adapter 偏简**）。

#### 需要做的

- [ ] **Mention 解析**：评论入库时 tokenize body 匹配 `@agent-([a-z0-9-]+)`，跳过 markdown 代码块和引用块。匹配 role key → 查 host yaml → 通过 `mention_by` 校验 → 投递 `issue.comment.mentioned` 事件。匹配但不通过校验的 chip 信息落 metadata（UI 在 M7c 渲染）。
- [ ] **`issue_comments.mentioned_roles JSONB[]` 列** + 入库时填充。
- [ ] **平台事件总线**：定义 v1 事件 payload schema（structured JSON），落 `event_log` 表 + in-process 分发到多 consumer（M6c runner 喂 agent stdin / audit log / UI 监听 / webhook stub）。
- [ ] **`modules/platform_mcp`**：HTTP MCP server，路径 `/api/mcp/v1/`（`tools/list` + `tools/call` 等 MCP 标准 RPC）。`Authorization: Bearer $HANGRIX_SESSION_TOKEN` —— 这跟 LLM proxy / git push 共用的同一张 session token；`tools/list` 返回当前 session role 的 `can:` 与已激活平台工具的交集；`tools/call` 走对应平台 handler。
- [ ] **平台工具实现**：`issue.diff` / `issue.children` / `issue.checks`（M8 前返空）/ `issue.review_vote` / `issue.merge` / `issue.close` / `roster.list` —— 每个都在 platform MCP server 后面挂 handler。本地工具（`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`）由 M6b agent 自带，**不走 MCP server**。
- [ ] **Agent ↔ 平台事件桥接**：M6c runner 把平台事件总线的事件翻译成 M6b agent 的 stdin JSON-Lines；反向把 agent stdout 的 tool call 报告写回事件总线 + audit（注：tool call 的执行已由 agent 自己经 MCP 完成，runner 这里只是落审计 + 转发状态）。
- [ ] **结构化事件 payload schema**：
  - `issue.review_vote`: `{state: approve|request_changes|abstain, body?: text}`
  - `ci.status_changed`: `{check_name, state, url?}`（M8 才有产生方，M7b 先定 schema 留口）

#### 退出条件

基于 M7a 的 host yaml，加上 dispatcher / backend / reviewer / maintainer 四个 role → 开 issue「加 health check 端点」→ dispatcher 自动起、调 `issue.comment` 发 `@agent-backend please add /healthz` → backend session 自动唤醒（无人手动触发）、写代码 + push → reviewer 因 `commit.pushed` 自动唤醒、调 `issue.review_vote` 投 approve → maintainer 因 `review_vote.posted` 唤醒、调 `issue.checks`（返空，stub 接受）+ `issue.merge` 合并 → issue 自动转 `merged`。**全程通过现有 M4 timeline tab 可见**（没有 swim-lane）。

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
- `hangrix/maintainer` —— 看 review / CI 状态 → 决定何时调 `issue.merge`；自带一份保守的 merge policy（要 reviewer approval + CI green + 文档可自合并），host 用 prompt addendum 调整。

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
- 工具 **`issue.checks`**（maintainer 一次性拿到当前 issue 所有 check 的最新 state，决定是否 merge）

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
