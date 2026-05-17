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

## 当前状态

**M0 – M7a Phase 2 已完成。** 平台从账号、git 内核、push / 协作辅助、issue 容器、组织一路立到 LLM proxy、agent 二进制、runner 容器底盘，最后到多 role 编排：开 issue 即按 host yaml 自动起 per-role 会话，会话行带完整 snapshot（agent / repo sha + cause）；关 issue 即归档。Playwright + 真 git CLI + 真 Postgres 已端到端跑通 M7a 退出条件，真容器 + 真 LLM 的人工 smoke 步骤住 [docs/agent-session-smoke.md](docs/agent-session-smoke.md)。下一步是 **M7b**（mention 协议 + 完整工具集 + 事件总线）。

子系统级别的 spec 拆出去：
- agent 身份模型 → [docs/agent-identity.md](docs/agent-identity.md)
- LLM proxy 翻译规则 → [docs/llm-proxy.md](docs/llm-proxy.md)
- Runner ↔ 服务端协议 → [docs/runner-protocol.md](docs/runner-protocol.md)
- Agent / host yaml schema → [docs/agent-config.md](docs/agent-config.md)
- 技术栈与前端布局 → [docs/tech-stack.md](docs/tech-stack.md)

## 里程碑

### M1 — 账号与权限基础设施 ✅

先把"人"立起来。后面所有 git / agent 能力都挂在用户身份上。

**权限模型刻意简单：只分 `user` 和 `admin` 两级。** 仓库级、组织级权限延后到真正需要时再加。

- [x] 用户实体 + 注册 / 登录 / 登出 / 当前用户接口。会话用服务端 session + cookie + Redis KV（便于禁用账号时一键下线），不上 JWT。注册首个账号自动 admin。
- [x] `RequireAuth` / `RequireAdmin` 中间件 + 管理员页面（改角色 / 启用状态、软删除）。
- [x] 前端：登录 / 注册 / 个人资料 / admin 用户管理。

**计划里删掉的事**：原计划在 user 表里加 `kind = human | agent` 字段，已删除。决定改为"users 只代表人类，agent identity 走独立路径"——避免账号系统在 password / 邮箱 / 登录等地方对人和 agent 拧着说。

**退出条件（已通过）**：第一个账号注册自动 admin，能禁用第二个账号；未登录 401、非管理员访问 admin 接口 403。

### M2 — Git 内核（read-only） ✅

让平台能"看"仓库。

- [x] 仓库元数据 + bare repo 落盘；私有仓库 owner + admin 可见，public 任何登录用户可见，**不引入仓库级 ACL 表**。
- [x] go-git 读封装：`Init` / `SeedReadme` / refs / commits / commit-with-diff / tree / blob / diff。其他模块**唯一**的 git 入口。
- [x] 仓库管理 API（创建可选 `init_readme` 让 git clone 立即可用）+ 读 API（refs / commits / tree / blob / diff）。
- [x] 前端：仓库列表 / 新建 / 详情（文件 + 提交记录两 tab）/ 文件查看 / commit 详情 + 文件级 diff。
- [x] **Smart HTTP clone**：服务端 shell-out 到系统 `git upload-pack --stateless-rpc`。Public 免认证，private 走 cookie 或 HTTP Basic（PAT 在 M3）。

**退出条件（已通过）**：UI 创建仓库（勾上 init_readme）→ `git clone` 拿到 README + initial commit → 浏览器看到文件树 / commit 列表 / commit diff。

### M3 — Git 平台（push / 分支 / Tag / 设置 / 协作辅助） ✅

把 M2 的"只读 git"升级成完整 git 协作平台。**这是 M4 之前最后一步通用 git 形态**——之后所有写入都要挂在 issue 下。

> **过渡说明**：M3 允许直接 push 任意分支、删任意分支、改默认分支——这是有意的过渡形态。M4 引入 issue 后会**收紧**：默认分支只能由 issue merge 推进，非 default 分支必须挂在某个 issue 下，直接 push 到游离分支会被拒。M3 的写入路径预埋了 `BranchWriteGuard` hook，M4 接入时无需挖抽象。

#### 核心
- [x] **HTTP smart push** + **Personal Access Token**（一次性返回明文，scope 只分 `repo:read` / `repo:write` 两档）。
- [x] **分支 / Tag 管理**：API + UI（新建 / 删除 / 设默认；annotated tag 走 tag 对象 + 关联 tagger）。删当前 HEAD 分支返回 409；切默认分支同步写盘 HEAD。
- [x] **仓库设置页**：基础信息 + 危险区（type-to-confirm 删除）。
- [x] **Compare 视图** + **README Markdown 渲染**（GFM + DOMPurify sanitize，仅在仓库 root 渲染）。

#### 协作辅助
- [x] **分支保护规则**：禁止 force-push / delete / 直接 push 三类开关。强制点既在 web API 也在 receive-pack 钩子。
- [x] **分支包含查询**：commit 详情页显示"在以下 ref 中"。
- [x] **Archive 下载**：`.zip` / `.tar.gz`，shell-out `git archive`。

#### 不在 M3 内
- 多协作者 / 组织（M5）。Web UI 直接编辑文件（agent 接入后才有自动化写入路径）。issue / PR / discussion（M4）。SSH 协议（永久不做）。

**退出条件（已通过）**：完整 PAT → push → branch / tag CRUD → annotated tag compare → 分支保护拒 force-push / delete → archive 下载。**全程 UI 中没有"issue / PR / agent"这些词。**

### M4 — Issue 作为唯一工作单元 ✅

把 issue 立成产品主入口；agent 还没接入，先让 issue 的对话 + 分支 + 合并都能用人类账号跑通。

**核心模型——一个 issue 同时是三样东西：**

| 切面 | 内容 |
| --- | --- |
| 对话 | 标题 + 描述 + 评论流（人类评论、agent 消息、系统事件如 commit / merge） |
| 分支 | 自动绑定 `issue/<n>`（create 时即落库，首次 push 才出 commit） |
| 会话 | 一个 agent session（M7a 起接入） |

- [x] Issue 持久化：issue / comment / event 三张表 + per-repo 单调编号 + sub-issue（合并子 issue = 推进父分支）。
- [x] Issue API：list / create / patch / merge / sync / timeline / diff / commits / children + 评论 create / list。**评论不允许删除**（issue 时间线视作 append-only 审计流）。
- [x] **Issue 分支与 push 的关系（核心收紧）**：跨模块 `BranchWriteGuard` + `PushObserver` 两接口；web API 的 `createBranch` / `deleteBranch` / receive-pack 跑同一份 guard。Pre-receive 钩子读 issue mode sidecar：推到 base / 不匹配 `issue/<n>` / 推到非开放 issue 全拒。Merge 端点内部豁免。
- [x] **三方合并**进 `modules/git`：fast-forward / merge-commit / up-to-date 三态 + 冲突哨兵。
- [x] **前端**：Issues 列表 + 新建 + 详情页（conversation / commits / diff 三 tab，tab 状态写进 URL query）；GitHub 风格评论卡 + 系统事件混排时间线 + 15s 自动刷新（hidden tab 暂停）；parent / children 侧栏 + Changes 卡 + merge / close / reopen 操作。FileDiffList 重写为 GitHub 风格 unified diff。

**计划里改掉的事**：
- **手动 Sync 按钮替换成自动轮询**。`POST /sync` 端点保留给 agent / 外部脚本用。
- **行内评论 UI 暂未做**。API 字段（file_path / line）已留口，前端 diff tab 暂时只渲染 patch。
- **评论删除已撤回**：审计可追溯优先，需要纠正再发一条评论说明。

**退出条件（已通过）**：开 issue 拿到 `issue/1` → CLI checkout / push 被 hook 放行 → diff tab 看到分支 vs base → merge 后 timeline 多两条 event → 直接 push 任意分支或 main 都被拒。**全程 UI 中没有"PR"这个词。**

### M5 — 组织 / Organizations ✅

把"个人账号 + 仓库"模型扩展到组织。一个 organization 是独立的 owner 实体，能拥有仓库、有多个成员。所有 git / issue 能力对 org-owned 仓库无感复用。

> **为什么先做（在 M6 agent 链路之前）：** M7 起官方预设 agent 仓库（`hangrix/dispatcher` / `hangrix/maintainer`）需要稳定的 owner 命名空间 —— 不能让平台预设挂在某个具体 admin 个人名下。

#### 设计原则
1. **Owner 命名空间统一。** `<owner>` 既可以是 user 用户名也可以是 org 名；同 namespace 互斥；所有 `/{owner}/{name}` 路由透明支持两类。
2. **Org 不是 identity。** Org 没有密码 / session / PAT —— org 是 namespace + ACL 容器，做事的总是它的成员。
3. **权限继续刻意简单。** Org-level role 只两档：`owner` / `member`。不引入 team / outside collaborator / repo-level role。
4. **Repo 归属二选一，可转移。** Transfer 是 owner-only 操作；DB 字段切换 + 磁盘 bare repo rename 落同一事务。

#### 已完成
- [x] Org CRUD（创建者自动 owner、type-to-confirm 删除）+ 成员管理（直接加，**不走 invitation 流程**）。
- [x] 跨模块 `Resolver`（`ResolveOwner` / `Membership`）：repo 模块和所有路由都走它，不再各自查 user 表。
- [x] 仓库归属二选一：DB CHECK 保证 owner 是 user 或 org 但不能两个都是。
- [x] 仓库 transfer：DB swap → 磁盘 rename，磁盘失败回滚 DB。
- [x] 保留名（`admin` / `api` / `git` / `static` / `_` 等）+ 跨表 namespace 互斥校验。
- [x] 前端：导航栏 Organizations section、统一 owner profile 页（一个组件按命中渲染 user 或 org）、Transfer 弹窗（type-to-confirm）、新建仓库 Owner 下拉。

**计划外但已经做了的事**：
- **撤回 Org visibility 字段**：原计划 public / private 两档，落地后判断"私有 org 给非成员看什么"始终拐不出有意义的语义，干脆删列 + 删 UI。所有 org 一律登录可见，私密性靠仓库 visibility 兜底。
- **面包屑机制重构**：从 130 行 `route.path` 大 switch 换成基于 composable 的 `setBreadcrumbs(supplier)`，supplier 跟随 locale / params / fetched 数据自动重算。

#### 不在 M5 里的事
Team / sub-group、outside collaborator、org-level PAT / OAuth、invitation 流程、billing —— 全部留待真有需求时再加。

**退出条件（已通过）**：建 org → 加成员 → 把私人仓库 transfer 给 org → 路径 rename 后老路由 404 + 新路由生效；最后一个 owner 不能被移除 / 降级；保留名拒收；M4 全部退出条件在 org-owned 仓库上无修改通过。

### M6a — LLM provider & proxy ✅

平台第一步要能跟 LLM 说话。Admin 配 provider → 平台跑代理 → 任何 OpenAI SDK 客户端都能调 → 用量落表。

**当前形态：** 端点合并为单一 `POST /api/llm/v1/responses`；provider 路由由请求体 `model` 字段反查 `allowed_models` 决定；provider 字段裁剪到 name / type / base_url / api_key / allowed_models 五项；api_key 走 AES-256-GCM 落盘加密。三种 adapter（`openai` Responses 原生 / `openai-compat` Chat-Completions 翻译 / `anthropic` Messages 翻译）都走 typed 请求 / 响应；reasoning effort 在 Anthropic 翻成 thinking budget；stream=true 一律 501。详细规则与边界条件住 [docs/llm-proxy.md](docs/llm-proxy.md)。

**重构小记**：session token 在 M6c 一起 ship 时从这个模块剥离搬进 runner —— token 现在是 agent 身份，跟 LLM provider / model 解耦（见 [docs/agent-identity.md](docs/agent-identity.md)）。

**退出条件（已通过）**：admin 注册 provider → 用 session token 调代理 → 上游 mock 返回 → 用量落表（含 reasoning_tokens 拆分）→ revoke session 立即 403。

### M6b — Agent runtime（Go binary） ✅

立一个独立的 agent 二进制（**用 Go 从头写，不依赖任何现成的 agent SDK**），跑在容器里跟 LLM proxy + 平台 MCP server + 容器内 `git` CLI 三方说话。M6c 把这个二进制 bind-mount 进 runner 调度的容器里。

> **为什么从头写：** 现有 SDK（Claude Agent SDK / OpenAI Assistants / LangChain agent runner）都是 opinionated 的 high-level 抽象。Hangrix 要把 audit、role identity、prompt 来源（base + host addendum）、git 工作流深度嵌进 loop，control 全攥手里更划算。

#### 角色与边界
- 每个 role 在容器里跑一个 agent 进程，存活到 issue 归档或 idle 超时。
- 通过 **stdin / stdout JSON-Lines** 跟外层 runner 通信：runner 喂事件，agent 报告 tool call / 状态 / 日志。
- 通过 HTTP 跟 LLM proxy + 平台 MCP server 通信，凭证从 env 拿。
- 通过 **shell-out** 调容器内 `git` CLI 处理仓库读写——**没有 git 专用工具**。
- **无状态执行器**：session 状态归平台管，agent 进程重启从平台拉历史重建上下文。

#### 工具集
工具分两类，LLM 看到的是一个扁平 function-call 列表：
- **本地工具**（agent 二进制内置，容器内执行）：`read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch`，语义参考 Claude Code 同名工具。
- **平台工具**（HTTP MCP 暴露）：`issue_*` / `roster_*`。M6b 期间走 mock；真平台 MCP server 在 M7b。

#### 启动期环境
Runner 在容器启动时注入：统一 session token、LLM proxy endpoint + resolved model、平台 MCP endpoint、role / session / host repo / issue 上下文、agent bundle 路径、host addendum 文件路径、role 的工具白名单。

#### 主循环
首条 stdin 帧必须是 `kind:history`（可能为空数组，新 session）；之后每收一个 event 进单轮 `LLM → tool calls → 喂回 → 再 LLM` 的 round-trip 直到 LLM 不再发 tool call，上限 16 轮防失控。Assistant 消息 + tool call 实时通过 stdout 出栈，runner 是消息日志的唯一持久化者。**Agent 不写盘**。

#### Prompt 三层
runtime 上下文 KV 块 → 平台 baseline（`//go:embed`）→ agent 仓库的 `entry.base_prompt` → host yaml 的 prompt addendum。Baseline 按 RFC 2119 关键词（MUST / SHOULD / MAY）写规则，明文声明 baseline 不可被上层 prompt weaken。Bundle 误配走错而非静默跌回。

#### 重要约束
- **"push 前自行 rebase" 靠 prompt 教**，代码侧零 git 重试逻辑。force-push 受 hook 禁，自然逼着流程。
- **`bash` exec `bash -c`（不是 `sh -c`）**：LLM 写脚本时高度依赖 bashism，降级到 dash 会静默坏。
- **工具错误按"what / why / how"三段写**，LLM 把 tool error 直接当文档用。
- **`edit` 强制先 `read`**：保证 LLM 看到当前内容并精准定位。
- **Streaming 不做**：v1 非流式跑通 round-trip 是退出条件；流式留待 M9 上下文优化时一起做。

#### 依赖原则
**无 third-party agent framework 依赖。** 唯一非 stdlib 库是 `html-to-markdown` 给 `webfetch` 用。LLM 客户端 / MCP client / IPC / 主循环 / prompt 拼装全部手写。

**退出条件（已通过）**：本进程 mock LLM + mock MCP + 真本地工具 + 真 IPC pipe → 一轮 round-trip 内同时跑了一个本地工具 + 一个 MCP 工具 + 收到 final assistant message + `done` 帧。Docker 端到端验证留给 M6c。

### M6c — Runner & 容器底盘 ✅

把 agent 的部署 / 执行 / 凭证供给立起来：独立的 `hangrix-runner` 二进制以 outbound-only HTTP 长轮询连服务端，按 session 拉容器、bind-mount agent binary、注入凭证、转发 stdin/stdout。协议细节住 [docs/runner-protocol.md](docs/runner-protocol.md)。

**核心模型**：
- **Runner 节点**：独立进程，部署在任何能跑容器的机器上；可见度分 platform / user 两级；自报 capabilities，server 按"可见度 + 容量"选 runner。
- **Agent 容器**：一 session 一隔离容器；agent 二进制由 runner bind-mount 注入（不打进镜像，升级即换二进制）。
- **统一 session token**：同一张 token 同时鉴权 LLM proxy / 平台 MCP / git push；plaintext 加密落盘，runner 拉任务时一次性下行；session 终态时清零。

**已经历的重构（与 M6a 一起 ship）**：
- Session token 从 LLM provider 模块搬到 runner 模块 —— token 跟 agent_session 一对一，不再绑 provider / model。
- `createSession` 请求体只要 model，provider 由 LLM proxy server-side 反查决定。

**退出条件（已通过）**：本进程跑 `httptest` server + fake docker orchestrator → markRunning 命中 → 喂 history + mock event → fake agent emit 四帧 stdout → runner POST 回 messages → terminate succeeded。整条 stdin / stdout / lifecycle 在 5 秒内闭环。真 docker / 真 LLM 端到端 smoke 留给 M7a。

### M7a — 多 role 基础设施 ✅

把 agent / role / team 立起来：识别 agent 仓库、解析 host yaml、起 per-role session、commit author 落 role key、audit 链跑通 —— **不接 mention 协议、不上完整工具集、不动 UI**。M7b 把协作层补齐，M7c 把 UI 和官方预设 agent 收尾。

**核心抽象：**

| 概念 | 定义 |
|---|---|
| **Agent** | 一个 Hangrix 仓库（根目录有 `agent.yml`），含 base prompt / 声明的工具集 / 元数据 |
| **Role** | host 仓库 `.hangrix/agents.yml` 里的本地标签 = agent 引用 + 触发器 + 工具白名单 + scope hint + 可选 host prompt addendum + mention 授权 |
| **Team** | 一个 issue 上所有已激活 role sessions 的集合（取代原"1 issue 1 session"）|
| **Mention** | `@agent-<role-key>` 评论语法，是唯一的 role 唤醒方式（协议本身在 M7b 实现）|

Schema 全字段语义住 [docs/agent-config.md](docs/agent-config.md)。

工程拆成两个 phase，两边解耦：

#### Phase 1 — Schema / 解析 / 仓库识别 / Bundle 分发 ✅

- [x] **agentsconfig 解析包**：纯函数解析 `agent.yml` / `.hangrix/agents.yml` / `.hangrix/agents.lock` 三份 schema，严格拒未知键；agent.yml 拒任何"host 才能说的话"字段（container / env / secrets / volumes / llm / image / build / roles —— 落实原则 7）。
- [x] **Repo `kind` 标识符**：每个仓库带 standard / agent 二值。Push 侧自动检测：post-receive 钩子读默认分支根目录 `agent.yml`，通过 strict parse 则升级为 `kind=agent`；坏文件不锁仓库 push，让 owner 能修。
- [x] **Runner schema 演进**：session 行加 snapshot 列（agent_sha / repo_sha / role_key / cause_kind / cause_id / role_config）+ idle / archived 两个状态。老 admin smoke 路径继续工作（snapshot 字段允许空）。
- [x] **Agent bundle 分发**：服务端按 `<owner>/<name>/<sha>.tar.gz` 出去时间戳的确定性 tarball；runner 端 content-addressed 缓存 + 单 sha 单 flight + 两段 sha256 校验 + symlink-rejecting 提取。

#### Phase 2 — 生命周期 / 编排 / Identity / 端到端 ✅

Phase 2 把 Phase 1 接到 issue 生命周期上。退出条件已通过 Playwright + 真 git CLI + 真 Postgres 跑通；真容器 + 真 LLM 让 agent 干活的人工步骤住 [docs/agent-session-smoke.md](docs/agent-session-smoke.md)。

- [x] **`modules/agent_session`**：新模块，三个 surface —— Spawner（issue 事件 → role 会话）、Archiver（issue 关闭 → 全部会话归档）、Auditor（按 issue 列出会话）。持久化继续复用 runner 模块的会话表。**无人工 archive 入口** —— 唯一归档触发器是 issue.closed / issue.merged，admin 想停某 agent 的力度是从 host yaml 删 role 或禁用 agent 仓库。
- [x] **Session spawn 编排**：issue.opened 触发后，spawner 读 host yaml + lock 文件，按 trigger 过滤 role；为每个匹配 role 解析 `<owner>/<name>@<ref>` 到 sha（lock 优先，回退 live git resolve）、校验目标是 agent 仓库、解析有效 LLM model（per-role > host 默认）、把整套 role 配置 snapshot 进会话行、mint + seal session token、enqueue history + cause event 两帧给 runner。Runner 选择策略是 "unpinned"，第一个 eligible runner 接管。
- [x] **Identity 落地**：commit author = `<role-key>` / `<role-key>@agents.<host-domain>`，通过容器 env 注入；agent 调 `git commit` 时 git 自动用这套身份。审计的权威是会话行 —— 即便容器外伪造作者，row 仍然是真相之源。
- [x] **Audit 链路**：admin 接口按 issue 列出所有会话的 snapshot 三元组（agent_sha / repo_sha / cause_id）+ 解析后的 role config。把 `(agent_sha, repo_sha)` 一对 checkout 出来就能精确复现 agent 当时看到的整套 prompt + 工具集 + 代码状态。
- [x] **端到端退出条件（Playwright smoke 实跑通）**：fresh-DB 启服 → Playwright 注册 admin → UI + git CLI 推 agent 仓库（含 `agent.yml`）→ UI + git CLI 推 host 仓库（含 host yaml + lock）→ UI 开 issue → 会话行生成、snapshot 三元组对得上、inputs queue 两帧到位、env 含完整 audit 元数据；admin 审计接口返回完整 snapshot → UI 关 issue → 会话归档、token 清零。
- [x] **Smoke 翻出来的两个潜伏 bug 顺手修了**：① 模块装配的依赖图缺一条边，fresh-DB 启动可能把 runner 迁移排到 repo 之前；老 dev DB 因为表早就在所以没暴露。② 仓库 kind 刷新只在 push 路径跑，merge 端点不走 push 路径，结果一次 merge 引入 `agent.yml` 后 kind 还是 standard。两处都修了。

### M7b — Mention 协议、完整工具集、事件总线

骨架立稳后铺协作层：mention 解析 + 完整工具集 + 事件总线 + 三层分发架构。让多 role 真正能协作（dispatcher 路由 + reviewer 投票 + maintainer 合并），但 UI 还是 M4 的单一时间线（swim-lane 留给 M7c）。

#### Mention 协议
- 语法：`@agent-<role-key>`。`agent-` 前缀预留未来人类 `@<username>` 不撞名。
- 评论入库时 tokenize，跳过 markdown 代码块和引用块。匹配 role key → 查 host yaml → 通过 `mention_by` 校验 → 投递 `issue.comment.mentioned` 事件给该 role。
- 同评论 @ 多个 role 投递 N 个独立事件，各 role 串自己的流。
- 人类直接 `@agent-backend please fix X` 跟 dispatcher 发同样评论效果完全一致 —— "评论 + mention"是人、dispatcher、其它 agent 三方共用的同一协议，没有第二种唤醒方式。

#### 工具集（v1）

| 工具 | 含义 | 典型持有者 |
|---|---|---|
| `issue_read` / `issue_diff` / `issue_children` / `issue_comment` | 读时间线 / diff / sub-issue / 留言 | 几乎所有 role |
| `issue_checks` | 当前 issue 所有 check 的最新 state（M8 起填充）| maintainer |
| `issue_review_vote` | 投票（approve / request_changes / abstain）→ 结构化事件 | reviewer |
| `issue_merge` | 合并到 base —— 默认无人能调，仅显式 `can:` 授权 | maintainer |
| `issue_close` | 关 issue | maintainer / dispatcher |
| `roster_list` | 列当前 team 已激活 role | dispatcher |
| `read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch` | M6b 已实现 | 任何动代码 / 跑命令 / 查文档的 role |

**两种工具来源**：
- **本地工具**由 agent 二进制内置，容器内 in-process 执行，**不经过 HTTP**。
- **平台工具**由 `modules/platform_mcp` 通过 HTTP MCP server 暴露；session-scope bearer token 鉴权。

git 凭证由 runner 在容器启动时预配置（credential helper），agent 用 `bash` 调 `git push` 即可 —— **没有 git 专用工具**。

#### 事件集（v1）

`issue.opened` / `issue.closed` / `issue.comment.any` / `issue.comment.mentioned` / `commit.pushed` / `review_vote.posted` / `ci.status_changed`（M8 才有产生方，M7b 先定 schema 留口）。

#### 事件的三层分发

平台 schema 只定义事件总线 JSON payload，各层消费者分工：

```
[平台事件总线]                ← schema 在这一层（structured JSON，命名严谨、可版本化）
    ├→ [runner → agent stdin]   翻译成 agent 输入事件 → agent 看到
    ├→ [时间线 UI]               渲染成 swim-lane 条目     → 人类看到
    ├→ [audit log]               原样落表                  → 事后查询
    └→ [外部 webhook]            原样投递（M10+）          → 第三方
```

Agent stdin 看到的具体 prose 格式由 agent 二进制决定，**不进 schema 也不在 ROADMAP 锁**。事件 payload 保留所有下游消费者可能要的字段（schema 偏全，adapter 偏简）。

#### 需要做的
- [x] Mention 解析：`agentsconfig.ParseMentions` tokenize body 跳过代码块 / 引用块；issue handler.createComment 加载 host yaml、按 `mention_by` 校验后通过 `Spawner.OnTrigger(RoleKey=...)` 投递 `issue.comment.mentioned` + `issue.comment.any`。
- [x] Spawner v1 编排（替代独立事件总线 v0）：`TriggerInput` 增加 `RoleKey` / `Payload` 字段；同 (role, cause_kind, cause_id) 幂等；非 archived live session 走 EnqueueInput 复用容器；archived 角色静默跳过。`commit.pushed` 由 `SyncIssueBranch` 触发。**独立 event_log 表延后到 M7c**（v1 piggy-back 在 `issue_events` + agent_session_messages 上 —— audit 闭环可用，但还没有 UI 监听 / webhook stub 的统一表）。
- [x] `modules/platform_mcp`：JSON-RPC over `/api/mcp/v1`，`hgxs_` session-token bearer 鉴权；按 session.role_config 的 `can:` 过滤 `tools/list`，未授权工具的 `tools/call` 返 `isError:true`。
- [x] 平台工具实现（9 个 v1）：`issue_read` / `issue_diff` / `issue_children` / `issue_checks`（M8 前返空）/ `issue_comment` / `issue_review_vote` / `issue_merge` / `issue_close` / `roster_list`。Identity 用 spawner 的 `IdentityForRole` 落 agent commit author。
- [x] Issue 持久化扩展：`issue_comments.author_id` nullable + 新 `agent_role` 列；同名列加在 `issue_events`；CHECK XOR 强制 human/agent 二选一。新 `EventReviewVote` event kind + `ReviewVotePayload`。Issue infra 整体迁到 sqlc。
- [x] Agent ↔ 事件桥接（v1）：`runner.EnqueueInput` 是事件喂 agent stdin 的渠道；反向 tool call 的事件回流由各 tool 直接调 `Spawner.OnTrigger`（`issue_review_vote` → `review_vote.posted`、`issue_merge` → archive）。统一的事件总线抽象延后到 M7c 跟 swim-lane UI 一起做。
- [x] **真容器 + 真 LLM 的端到端 smoke**：[scripts/smoke/](scripts/smoke/) 一键 docker compose 起 server + postgres + redis + runner，agent 容器加入同一张网桥；dispatcher → backend → reviewer → maintainer 整圈在真 DeepSeek（或任意 OpenAI-compat provider）+ 真 docker 下完整跑通——issue 自动 merged、4 个 session 全部 archived，约 76 秒。手册 [docs/smoke.md](docs/smoke.md)。

#### 退出条件

基于 M7a 的 host yaml，加上 dispatcher / backend / reviewer / maintainer 四个 role → 开 issue「加 health check 端点」→ dispatcher 自动起 → 调 `issue_comment` 发 `@agent-backend please add /healthz` → backend 自动唤醒、写代码 + push → reviewer 因 `commit.pushed` 自动唤醒、投 approve → maintainer 因 `review_vote.posted` 唤醒、调 `issue_merge` 合并 → issue 自动转 `merged`。**全程通过现有 M4 timeline tab 可见**（没有 swim-lane）。

### M7c — 前端 swim-lane + 平台预设 agent

最后一步把体验做出来。

#### 前端
- **Issue 详情页两 tab：「智能体」/「人类」**。
- 「智能体」tab 按 role 分 swim-lane（dispatcher / backend / frontend / reviewer / maintainer / …），每 role 一列；tool call / commit 触发 / 消息分块、可折叠。
- 「人类」tab：人类评论 + 真正由人触发的事件（人类 push / 人类手动 merge 等）。
- 评论里的 `@agent-<role>` 渲染成 chip；点击跳到对应 swim-lane；未通过 `mention_by` 校验的 chip 灰色显示"未触发"。
- 两 tab 共享时间轴坐标，可跨 tab 跳同一时刻。
- Admin 后台新增"agent 仓库"列表 + 平台禁用表视图。

#### 平台预设
Installer 首次启动 seed 两份官方 agent 仓库：
- `hangrix/dispatcher` —— 路由器，识别任务 → @ 相关 role。
- `hangrix/maintainer` —— 看 review / CI 状态 → 决定何时调 `issue_merge`；自带保守的 merge policy。

其它角色（`coder` / `reviewer` 等）由用户自己写或装第三方。两个官方 agent 仓库后续走正常 git 流程升级 —— 平台吃自己的狗粮。

#### 退出条件

用户在自己仓库写 host yaml 引用 `hangrix/dispatcher@latest` + `hangrix/maintainer@latest` + 自己写的 coder / reviewer → 开 issue → 全程在 UI 上看 swim-lane 流动：dispatcher 派单 → backend 写代码 → reviewer 投票 → maintainer 合并。「人类」tab 干净；「智能体」tab 完整；admin 后台看到三个 agent 仓库注册；`git log` 看到不同 role 的 commit author 区分。

### M8 — CI / Workflow 子系统

独立于 agent 协作的检查执行系统，类比 GitHub Actions。M7 通过两个接口跟 CI 协作：

- 平台事件 `ci.status_changed`（多 check 支持，每个 `(issue_id, commit_sha, check_name)` 一条独立状态）。
- 工具 `issue_checks`（maintainer 一次性拿到当前 issue 所有 check 的最新 state）。

完整设计在本 milestone 展开 —— workflow 定义文件位置、trigger 模型、job runner 是否复用 M6 的 agent runner pool、check 数据模型与 panel UI、credential 注入路径等。详细规划等 M7 落地后再拆。

### M9 — 围绕 AI 重塑 issue 体验

把 M7 的 agent 能力反过来打磨 issue 自身 —— 这是 issue 真正"AI-Native"的部分，不只是把 chat 嵌进来。

- [ ] **结构化 agent 时间线视图**：把 agent 的 tool call、思考、commit、问题分成可折叠的块。
- [ ] **Diff 的 AI 视角**：按"意图块"分组的视图（agent 生成时附带语义标签）。
- [ ] **语义检索**：仓库级 embedding 索引，同时服务于人类的代码搜索框和 agent 的 `repo.search` 工具。**索引层只做一个**。
- [ ] **Inline action**：在 issue diff 的某一行上一键让 agent "改这段 / 解释这段 / 补测试"。
- [ ] **Review agent**：被某 issue 邀请后只发表结构化 review，不直接 commit。
- [ ] **Issue 模板与意图引导**：开 issue 时引导用户写"想达成什么"而不是"在哪行代码"。

退出条件：用户在平台上的日常路径是"开 issue → 和 agent 来回几轮 → merge"，绕开 agent 反而更费劲。

### M10+ — 待定

候选方向：SSO、Federation / mirror、桌面客户端包装、Team / sub-group、Outside collaborator、外部 webhook、LLM 成本追踪 dashboard + per-host 配额、User-BYOK、更多 LLM provider 类型（Vertex / Bedrock / Azure OpenAI 等需要签名协议的）。

## 不在路线图内的事

- **不做独立的 PR / review / discussion 实体。** 这些都是 issue 的不同切面。
- **不允许游离的分支（M4 起强制）。** 任何非 default 分支必须挂在某个 issue 下。
- **不做 GitHub / GitLab 的功能补全。** 缺什么功能要先回答"AI agent 怎么用它"。
- **不做通用 LLM 中台。** 平台只负责把 git 能力以工具形态暴露给 agent，不替 agent 选模型、不做 prompt 编排。
- **不做无沙箱的 agent 自治。** agent 跑在隔离容器里，凭证按 session 维度一次性下发、过期回收；admin 能一键吊销。**agent 可以直接 commit / merge** —— 安全靠"可见 + 可停 + 可 revert"，不靠"先批准再做"。
- **不让 agent 复用 users 表。** users 表只代表人类。Agent identity 走独立路径，避免账号系统在 password / 邮箱 / 登录态这些地方对人和 agent 拧着说。
- **权限模型刻意简单。** 平台层只用 `user / admin`；M5 给 org 加 `owner / member` 二档；M7 起 host yaml 配 role / can / mention_by。**不引入** team / outside collaborator / repo-level role / user 级 RBAC。
- **不做 SSH 协议。** HTTP + PAT 已覆盖所有 push / pull / API 场景：浏览器、git CLI、agent 共用一种凭证模型。
- **不做 Git LFS。** 大文件对 agent 工作流没价值（既不能 diff 也不能 patch）。需要存大文件的项目应当外挂对象存储 + 在 issue 引用链接。

## 工程基线（贯穿所有里程碑）

- 每个新功能走 `internal/modules/<name>/` 模块化单体约定（详见 [AGENTS.md](AGENTS.md)）；跨模块依赖只能通过 ioc 容器和对方 `domain/` 接口。
- 所有 HTTP handler 和 agent 工具共用同一层 domain 接口；禁止 agent-only 或 UI-only 的 fast path 绕过 domain。
- 数据库变更走 goose 迁移，向前可应用、向后有 Down，禁止改老迁移。
- audit log、agent task log 是产品功能，不是运维日志，从 M6 起就要落库可查询。
