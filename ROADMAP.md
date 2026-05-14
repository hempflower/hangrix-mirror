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
3. **agent 是 first-class identity，不是 webhook。** agent 有账号、有权限、有 audit log；它的提交、评论与人类账号同形。但 **agent identity 与 user 是不同实体**——用户表只代表人类，agent 走独立的模型（见 M5）。
4. **能力以工具暴露，不是以 prompt 注入。** 平台对外暴露明确的工具集（read repo / commit to issue branch / comment on issue / merge issue …），agent 通过工具调用与平台交互；不依赖把整个仓库塞进上下文。
5. **审计与可回滚优先。** agent 写权限受 policy 约束，所有写操作可被快速 revert；越是自动化的动作，越要可见、可暂停。
6. **本地优先的形态。** 当前以单二进制 + 嵌入式 SPA 形态运行；多租户/SaaS 形态是后续选项，不是前提。

## 当前状态（M3 完成）

**M3 全部闭环。** Hangrix 现在是一个能正常 push / pull / 建分支 / 打 tag / 改设置、还能配分支保护和下载 archive 的 git 平台 —— 形态接近 Gitea，只是还没有 issue 概念。

已就绪：
- **脚手架（M0）**：Go 1.26 + Nuxt 4 单二进制；`pkg/ioc` DI；chi、viper、air、Turborepo。
- **账号基础设施（M1）**：用户 / 角色 / 会话 / admin 后台。
- **Git 内核（M2）**：`modules/git`（go-git 读封装）+ `modules/repo`（元数据 + bare repo）+ smart HTTP `git-upload-pack`。
- **Git 平台（M3 核心）**：`modules/token` PAT + `git-receive-pack` 写路径 + 分支 / Tag CRUD + 仓库设置 + Compare + README 渲染。`resolveRef` 透明 peel annotated tag。
- **协作辅助（M3 stretch）**：`branch_protections` 表 + `pre-receive` 钩子（force-push / delete 拦截）+ commit 包含查询 + archive 下载（zip / tar.gz）。
- **数据库迁移系统**：`goose v3` 库模式 + 每模块独立 `goose_<module>` 版本表，启动时 sequential 应用。
- **前端基础**：shadcn-vue + Tailwind v4 + 5 套布局矩阵；vee-validate + zod + 全局 i18n errorMap；中英双语；独立 Admin Sidebar。新增组件 `dialog` / `textarea` 给 PAT / 设置 / 分支 / Tag / Compare 用，新依赖 `marked` + `dompurify` 给 README 渲染。

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
- 原计划在 user 表里加 `kind = human | agent` 字段（当时为 agent 那一步铺路），已删除。决定改为"users 只代表人类，agent identity 在 M5 用独立模型"——避免账号系统在 password / 邮箱 / 登录等地方对人和 agent 拧着说。

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
- **Web UI 直接编辑文件 / 在线 commit** — agent 接入后（M5）才有自动化写入路径，人类先用 git CLI。
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

### M4 — Issue 作为唯一工作单元

把 issue 立成产品主入口，但 agent 还没接入；先让 issue 的对话 + 分支 + 合并都能用人类账号跑通，下个里程碑再把 agent 塞进同一个容器。

**核心模型——一个 issue 同时是三样东西：**

| 切面 | 内容 |
| --- | --- |
| 对话 | 标题 + 描述 + 评论流（按时间线，含人类评论、agent 消息、系统事件如 commit/merge） |
| 分支 | 自动绑定一条 git 分支 `issue/<id>`（懒创建——首次有提交时才建） |
| 会话 | 一个 agent session（懒创建——首次 @agent 时才建；纯讨论 issue 可以没有） |

需要做的：

- [ ] `modules/issue`：issue 实体（id、repo_id、author_id、title、body、state `open | merged | closed`、branch_name、base_branch、created_at、updated_at）。
  - `domain/`：`Issue` + `IssueComment` + `IssueEvent`（系统事件，比如 commit-pushed、branch-merged，与人类评论混在同一时间线里）。
  - 评论流是 union 视图：人类评论 + 系统事件 + （M5 起）agent 消息按时间序排列。
- [ ] Issue API：
  - `POST /api/repos/{owner}/{name}/issues` — 开 issue（自动分配 `issue/<id>` 分支名，不立即建分支）
  - `GET /api/repos/{owner}/{name}/issues` — 列表（按状态/作者/关键字筛选）
  - `GET /api/repos/{owner}/{name}/issues/{id}` — 详情（含时间线）
  - `POST /api/repos/{owner}/{name}/issues/{id}/comments` — 评论
  - `PATCH /api/repos/{owner}/{name}/issues/{id}` — 改标题/正文/状态
  - `POST /api/repos/{owner}/{name}/issues/{id}/merge` — 把 issue 分支合并回 `base_branch`，成功后 state 转 `merged`
  - `GET /api/repos/{owner}/{name}/issues/{id}/diff` — issue 分支 vs base 的 diff（即"PR 视图"，但只是 issue 的一个 tab）
- [ ] **Issue 分支与 push 的关系——核心收紧**（兑现"Issue 是工作流最小单位"原则）：
  - 用户用 git CLI push 到 `issue/<id>` 分支时，自动在该 issue 时间线写入 `commit-pushed` 事件并附 commit 列表。
  - **push 到不存在 issue 的分支：拒绝。** `main` / 默认分支也只能通过 issue merge 进入，**不允许直接 push**。M3 预埋的 `BranchWriteGuard` hook 在这里换成"必须有 issue"的实现。
  - 在 receive-pack 服务端 hook 里检查目标分支名 → 若不是 `issue/<id>` 或不是受保护的 merge-only 分支，返回 pre-receive 拒绝。
  - 仓库设置里的"直接 push 默认分支"开关被强制关闭并隐藏；M3 的"分支保护规则"实质上**全部 default-on**。
- [ ] 前端：issue 列表页、issue 详情页（左侧时间线 + 右侧元数据/分支/diff tab）、新建 issue 表单、合并按钮、行级评论（行评论也是 IssueComment 的一种，带 `file_path` + `line` 字段）。Sidebar 工作区组加 "Issues" 入口；仓库详情页加 Issues tab，与文件 / 提交记录并列。
- [ ] 权限：依然只用 M1 的 `user / admin` 二分。仓库 owner 对自己仓库的 issue / 分支 / merge 有完全权限；其他登录用户在 public 仓库可读、可评论、可开 issue，但 push / merge 仍仅限 owner（或 owner 明确指派——多协作者延后）。

**退出条件：** 用户登录 → 在自己仓库开一个 issue「修一下登录页 bug」→ 用 git CLI checkout `issue/<id>` 分支 → 改代码 → push → issue 时间线出现 commit 事件 → 在 issue 里看 diff → 点 merge → main 分支收到改动，issue state 变 `merged`。**尝试 push 一个没有 issue 的分支被拒**。全程没有"PR"这个词。

### M5 — Agent 一等公民（在 issue 内响应）

把 agent 塞进 M4 的 issue 容器。**这是项目区别于其他 git 平台的核心。** Agent 的工作完全发生在某个具体 issue 里：读这个 issue 的对话、在这个 issue 的分支上写代码、把进展回流到这个 issue 的时间线。

- [ ] **Agent identity（独立模型）**：新建 `modules/agent` 维护 agent 实体（id、name、owner_user_id、runtime、policy、disabled、created_at）。agent **不复用 users 表**——它没有 email / password，认证靠平台颁发的 agent token，签名规则与 personal access token 不同。agent 的提交以独立 author 身份出现（不冒充人类）。所有跟"账号"相关的概念（登录、邮箱、密码修改、admin 面板里"用户"列表）继续只代表人类。
- [ ] **`modules/agent_session`**：每个 issue 至多一个 active session（懒创建）。Session 持有：关联 issue id、关联 agent id、runtime 类型、累积的对话上下文、tool call 历史、当前状态（idle / running / waiting-for-input / failed）。Session 与 issue 是 1:1，关闭 issue 时 session 归档。
- [ ] **触发方式**：用户在 issue 评论里 `@agent` 或点"让 agent 处理"按钮；这条评论被路由进 agent_session 作为新一轮 input。
- [ ] **平台工具集（platform tools）**：暴露给 agent 调用，**作用域绑死在 session 所属的 issue 上**——agent 不能跨 issue 写：
  - `issue.read` / `issue.comment`（在当前 issue 时间线留言）
  - `repo.tree` / `repo.read_file` / `repo.search`（在当前 issue 所属仓库内）
  - `branch.commit`（只能 commit 到当前 issue 的分支）
  - `issue.diff`（看自己改了什么）
  - `issue.request_merge`（请求人类 merge——agent 自己不能 merge）
  - 协议：MCP 兼容是首选；具体形式在 M5 启动时确定。
- [ ] **Agent runtime 适配层**：平台不内置 LLM provider，只定义 runtime 抽象（接收 session input、返回结果、流式状态）。第一版接入一种 runtime（Claude Agent SDK 或本地子进程），其它后续。
- [ ] **时间线统一**：agent 的每条消息、每次 tool call 都作为 `IssueEvent` 落入 issue 时间线，与人类评论混排。用户随时能看到 agent "正在做什么"。
- [ ] **policy 与 audit**：agent 的写操作可配置门禁（最大 diff 行数、改文件白名单、必须先 propose 再 commit）；所有 tool call 落 audit log。Admin 能一键停用某 agent 或暂停某 issue 的 session。前端 admin 面板新增"agent 管理"——和"用户管理"并列、但是独立的视图，因为它们是不同实体。

退出条件：用户在 issue 里写「帮我把登录按钮居中」 → @agent → agent 在该 issue 的时间线里逐步发消息（"我在看 LoginPage.vue …""我改了样式 …"）→ commit 自动出现在 issue 分支 → 用户点 merge。全程在同一个 issue 页面内完成，无需切到别的页面。

### M6 — 围绕 AI 重塑 issue 体验

把 M5 的能力反过来打磨 issue 自身——这是 issue 真正"AI-Native"的部分，不只是把 chat 嵌进来。

- [ ] **结构化的 agent 时间线视图**：把 agent 的 tool call、思考、commit、问题分成可折叠的块，不要单纯流式文本。用户能快速扫到"agent 改了什么 / 卡在哪 / 在等我什么"。
- [ ] **Diff 的 AI 视角**：issue diff tab 除了行级 diff，提供按"意图块"分组的视图（agent 生成时附带语义标签）。人类直接 push 的 commit 退化到普通行级视图。
- [ ] **语义检索**：仓库级 embedding 索引（增量更新），同时服务于人类的代码搜索框和 agent 的 `repo.search` 工具。**索引层只做一个。**
- [ ] **Inline action**：在 issue diff 的某一行上一键让 agent "改这段 / 解释这段 / 补测试"；新的 agent 输入挂在同一 session 上。
- [ ] **Review agent**：一类特殊 agent，被某 issue 邀请后只发表结构化 review（不直接 commit），review 也是 issue 时间线里的事件，不是另一种实体。
- [ ] **Issue 模板与意图引导**：开 issue 时引导用户写"想达成什么"而不是"在哪行代码"，因为 agent 更擅长前者。模板可仓库级配置。

退出条件：用户在平台上的日常路径是"开 issue → 和 agent 来回几轮 → merge"，绕开 agent 反而更费劲。

### M7+ — 待定

候选方向，下一阶段再裁剪：

- CI / runner（agent 的"跑测试"工具需要它，但可以先借外部 CI）
- 多租户、组织、SSO
- Federation / mirror
- 桌面客户端（已有单二进制形态，做包装就行）
- 多协作者 / collaborator 表（M3 的 owner-only 模型显出局限时再做）
- Transfer ownership（M3 仓库设置里占位的"coming soon"按钮）

## 不在路线图内的事

- **不做独立的 PR / review / discussion 实体。** 这些都是 issue 的不同切面，不再单独建模。如果将来真有需求把 review 拆出来，要先证明它在 issue 内做不了。
- **不允许游离的分支（M4 起强制）。** 产品稳态下任何非 default 分支必须挂在某个 issue 下，push 到没有 issue 的分支会被拒。**M3 是过渡期，允许直接 push**——M4 引入 issue 后 `BranchWriteGuard` hook 切换实现，把该规则强制开起来。
- **不做 GitHub/GitLab 的功能补全。** 缺什么功能要先回答"AI agent 怎么用它"；只对人有用的功能优先级靠后。M3 的 push / 分支 / 设置 / compare / README 渲染是 "agent 需要这些 git 抽象作为底座" 的最小集合，而不是为了对标其他平台。
- **不做通用 LLM 中台。** 平台只负责把 git 能力以工具形态暴露给 agent，不替 agent 选模型、不做 prompt 编排。
- **不做无沙箱的 agent 自治。** 任何 M5 之后的 agent 写权限都必须能在 admin 面板里一键吊销；agent 自己不能 merge，只能 request merge。
- **不让 agent 复用 users 表。** users 表只代表人类。agent 在 M5 走独立实体（独立认证、独立 admin 视图、独立 audit log），避免"账号系统"在 password / 邮箱 / 登录态这些地方对人和 agent 拧着说。
- **权限模型在 M5 之前不复杂化。** 一直只用 `user / admin`；仓库 owner 通过 handler 内部判断处理。真到了多协作者、组织、分支保护这类需求时再设计 ACL，不预留字段。M3 的"分支保护规则"是 repo-local 的简单规则，**不是** RBAC。
- **不做 SSH 协议。** 本地优先 + agent-native 的形态下，HTTP + PAT 已经覆盖所有 git push/pull / API 调用场景：浏览器、git CLI、agent runtime 共用一种凭证模型。SSH 要再维护一套 key 管理 + auth_keys 配置 + 端口暴露，回报很低；用户真需要 SSH 体验时大概率说明用错了产品（应该选 Gitea / GitHub），不是该补的功能。
- **不做 Git LFS。** Hangrix 的主轴是 "agent 在 issue 里读 / 改 / 评论代码"——大文件（视频、模型权重、设计稿二进制）对 agent 工作流没有价值，agent 既不能 diff 也不能 patch。引入 LFS 意味着 storage 后端、pointer 文件协议、独立鉴权三层额外复杂度，且会鼓励用户把不该入 git 的资产塞进来。需要存大文件的项目应当外挂对象存储 + 在 issue 里引用链接。

## 工程基线（贯穿所有里程碑）

- 每个新功能走 `internal/modules/<name>/` 模块化单体约定（见 [AGENTS.md](AGENTS.md)）；跨模块依赖只能通过 ioc 容器和对方 `domain/` 接口。
- 所有 HTTP handler 和 agent 工具共用同一层 domain 接口；禁止 agent-only 或 UI-only 的 fast path 绕过 domain。
- 数据库变更走 goose 迁移（`internal/modules/<name>/infra/migrations/<NNNNN>_<name>.sql`），向前可应用、向后有 Down，禁止改老迁移。
- audit log、agent task log 是产品功能，不是运维日志，从 M5 起就要落库可查询。
