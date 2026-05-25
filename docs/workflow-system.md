# Workflow 系统设计

[← ROADMAP](../ROADMAP.md)

目标：为 host 仓库提供一套**类似 GitHub Actions、但与现有 agent session 调度解耦**的 workflow 机制。Workflow 定义存放在仓库内 `.hangrix/workflows/*.yml`，运行时复用现有 runner 节点与容器执行能力，但不复用 `agent_sessions` 语义，不侵入现有 issue/评论触发的 agent 流。

---

## 设计原则

1. **仓库内声明**：workflow 定义固定来自 `.hangrix/workflows/*.yml`。
2. **容器环境单一来源**：workflow job 直接复用 `.hangrix/agents.yml` 的 `container` 定义，不再引入第二套 image/build/volume schema。
3. **与 agent 调度解耦**：workflow run / job run / 日志 / 终态记录独立建模；agent session 仍只服务于角色协作。
4. **runner 复用、协议分流**：runner 继续复用同一个 `/api/runner/tasks` 长轮询和 Docker orchestration，但任务类型要显式区分 `agent_session` 与 `workflow_job`。
5. **先做最小可用闭环**：v1 只支持平台内事件触发、仓库内 shell step、文本日志、串行 job 执行；不追求 GitHub Actions 全量兼容。

---

## 与现有系统的边界

### 复用哪些现有能力

- **容器定义来源**：复用 `docs/agent-config.md` 中 `.hangrix/agents.yml` 的 `container.image` / `container.build` / `container.entrypoint` / `container.env` / `container.volumes`。
- **runner 通道**：复用 `docs/runner-protocol.md` 里的 runner enroll / heartbeat / `/api/runner/tasks` 长轮询 / DockerOrchestrator / stdout/stderr 回传通道。
- **仓库变量展开**：复用 runner 已有的 `${VAR_NAME}` 整值展开规则。
- **代码 checkout 基础能力**：复用 runner 已有 clone / checkout 能力，但 workflow 使用一次性工作目录，不复用 agent 的长生命周期 issue workspace。

### 明确不复用的语义

- **不复用 `agent_sessions`**：workflow 不是角色会话，不写入 agent 对话历史，也不拥有 `hgxs_` agent session 身份。
- **不进入 role spawner**：workflow 触发不会投递到 `issue.opened` / `issue.comment` 的 role trigger 选择逻辑。
- **不复用 agent loop**：workflow 容器里执行的是 workflow job driver，而不是 `hangrix-agent` 的 LLM/tool loop。
- **不共享审计模型**：workflow run、job run、step 日志、触发原因、checkout ref、容器快照要独立落库。

### 与现有 automation 模块的关系

`apps/hangrix/internal/modules/automation/` 当前是“扫描 `.hangrix/automation.yml` 后创建 issue 并唤醒 agent”的调度能力。workflow 系统**不应把自己建模成 automation task 的变体**，但可以借用其以下经验：

- repo 扫描/读取配置的方式；
- 调度器与执行器拆分；
- run 记录与后台 job 的组织方式。

建议 workflow 使用**独立模块**，例如：

- `apps/hangrix/internal/workflowsconfig/`：解析 `.hangrix/workflows/*.yml`
- `apps/hangrix/internal/modules/workflow/`：run / job / 调度 / API / 存储

而不是继续扩展 `automation` 现有 schema。

---

## 文件布局

```text
host-repo/
├── .hangrix/
│   ├── agents.yml
│   └── workflows/
│       ├── ci.yml
│       ├── lint.yml
│       └── release.yml
└── ...repo files...
```

约束：

- 仅扫描 `.hangrix/workflows/*.yml` 和 `.hangrix/workflows/*.yaml`
- workflow 文件名仅用于人类识别，不作为稳定主键
- workflow 的稳定标识来自文件内 `name:`，同一仓库下必须唯一
- 缺少 `.hangrix/agents.yml` 时，workflow 系统对该仓库视为**不可用**，因为缺少唯一容器定义来源

---

## Schema（v1）

### 顶层结构

```yaml
version: 1
name: ci
on:
  repo.push:
    branches: [main]
    paths: ["apps/**", "go.work", "go.work.sum"]
    paths_ignore: ["docs/**"]
env:
  GOFLAGS: -mod=readonly
jobs:
  lint:
    steps:
      - run: gofmt -w .
      - run: git diff --exit-code
  test:
    steps:
      - run: go test ./...
```

### 字段定义

#### `version`

- 必填，当前固定为 `1`
- 未识别版本直接拒绝

#### `name`

- 必填，仓库内唯一
- 约束：`[a-z][a-z0-9-]*`
- 用作 API、run 记录、手动触发入口中的 workflow key

#### `on`

事件订阅映射，至少一个 key。v1 支持以下事件：

##### `repo.push`

仓库收到新的 push 时触发。

```yaml
on:
  repo.push:
    branches: [main, release/*]
    branches_ignore: [release/wip-*]
    paths: ["apps/**", ".hangrix/workflows/**"]
    paths_ignore: ["docs/**"]
```

字段：

- `branches: []string`：仅匹配这些分支 glob；空/省略表示不限制
- `branches_ignore: []string`：命中则忽略
- `paths: []string`：改动文件至少命中一个 glob 才触发
- `paths_ignore: []string`：若所有改动都被忽略则不触发

判定规则：`branches` AND `!branches_ignore` AND `paths` AND `paths_ignore`。

##### `repo.push_tag`

仓库收到新的 tag push 时触发。

```yaml
on:
  repo.push_tag:
    tags: ["v*", "release-*"]
    tags_ignore: ["*-rc*"]
```

字段：

- `tags: []string`：仅匹配这些 tag glob；空/省略表示不限制。匹配时使用 **short tag name**（例如 `v1.2.3`，而非 `refs/tags/v1.2.3`）。
- `tags_ignore: []string`：命中则忽略
- v1 **不支持** `branches` / `branches_ignore` / `paths` / `paths_ignore`

判定规则：`tags` AND `!tags_ignore`。

##### `issue.opened`

issue 创建时触发。

```yaml
on:
  issue.opened: {}
```

v1 不带过滤器。

##### `issue.comment`

issue 评论时触发。

```yaml
on:
  issue.comment:
    mentioned_only: false
    from_roles: [maintainer]
    from_users: [hempflower]
```

字段语义与 `docs/agent-config.md` 的 comment filter 保持一致：

- `mentioned_only: bool`：仅在评论里显式 mention 当前 workflow 名称时触发。v1 mention 语法为 `@workflow-<name>`。
- `from_roles: []string`：仅响应来自这些 agent role 的评论
- `from_users: []string`：仅响应来自这些人类账号的评论
- 多个条件为 AND

> 说明：这里刻意不复用 `@agent-<role>` 协议，避免与 agent 唤醒语义混淆。

##### `workflow.dispatch`

手动触发入口，供 API / UI 按 workflow 名手动运行。

```yaml
on:
  workflow.dispatch:
    inputs:
      ref:
        required: false
      reason:
        required: false
```

v1 支持字符串型输入。字段：

- `inputs.<name>.required: bool`
- 未声明的输入不允许传入

#### `env`

- 可选，workflow 级环境变量
- 仅允许 `map[string]string`
- 与 `.hangrix/agents.yml` 的 `container.env` 合并，**workflow 级同名 key 覆盖 container.env**
- 仍支持 `${VAR_NAME}`，沿用 repo variable expansion 规则

#### `jobs`

- 必填，非空 map，key 约束 `[a-z][a-z0-9-]*`
- v1 **按声明顺序串行执行**，不支持 `needs`、并行、matrix
- 第一个失败的 job 会终止本次 workflow run，后续 job 标记为 `skipped`

job schema：

```yaml
jobs:
  <job-key>:
    name: Optional human title
    env:
      FOO: bar
    timeout_minutes: 30
    dir: /workspace
    steps:
      - name: Optional step title
        run: pnpm install --frozen-lockfile
      - run: pnpm test
```

字段：

- `name: string`：可选展示名，默认等于 job key
- `env: map[string]string`：job 级 env，覆盖 workflow env 与 container env 中的同名 key
- `timeout_minutes: int`：可选，默认 60，范围 `1..1440`
- `dir: string`：默认 `/workspace`；job 级工作目录，必须落在 `/workspace` 下。step 可用同名 `dir` 覆盖本步骤的工作目录
- `steps: []Step`：至少 1 个

#### `steps`

v1 支持两种 step 类型，由 `type` 字段区分：

##### 通用字段

所有 step 共享以下字段：

- `id: string`：可选，约束 `[a-z][a-z0-9-]*`。用于 step outputs 引用（如 `${{ steps.build.outputs.version }}`）。未指定时 runner 自动分配 1-based 序号（`"1"`, `"2"`, …）。
- `name: string`：可选展示名，最大 200 字符。
- `type: string`：可选，值为 `run` 或 `release`。**省略时等价于 `type: run`**，保证向后兼容。

##### `type: run`（shell 步骤，默认）

```yaml
steps:
  - name: Install deps
    run: pnpm install --frozen-lockfile
  - name: Build
    run: go build -o ../../dist/app ./cmd/app
    env:
      CGO_ENABLED: "0"
      GOOS: linux
    dir: apps/app
```

专属字段：

- `run: string`：必填，使用 `bash -lc` 执行。支持 `${{ steps.<id>.outputs.<key> }}` 模板插值引用前序步骤的输出。
- `env: map[string]string`：可选，本 step 的环境变量，合并并覆盖 job/container 级 env。
- `dir: string`：可选，覆盖本 step 的工作目录。相对路径相对 job 工作目录（默认 `/workspace`）解析；绝对路径原样使用。

##### `type: release`（创建 Release 步骤）

内置类型 step 的参数放在 `with:` 下（对齐 GitHub Actions 的 action inputs）：

```yaml
steps:
  - id: build
    run: |
      make dist
      echo "tag=$HANGRIX_TAG" >> "$HANGRIX_STEP_OUTPUT_FILE"
  - name: Publish release
    type: release
    with:
      tag: ${{ steps.build.outputs.tag }}
      notes: |
        Release for ${{ steps.build.outputs.tag }}
      assets:
        - dist/hangrix-linux-amd64.tar.gz
        - path: dist/checksums.txt
          name: SHA256SUMS
      draft: false
```

`with:` 字段：

- `tag: string`：**必填**，要创建的 release 对应的 tag 名。该 tag 必须已存在于 bare repo 中（`refs/tags/<tag>`）。注意 `with.tag` 只做 `${{ steps.<id>.outputs.<key> }}` 插值，不读取 shell 环境变量——push_tag 触发时把 `$HANGRIX_TAG` 写成前序 step 的 output 再引用。
- `notes: string`：可选，release 说明（markdown）。
- `draft: bool`：可选，默认 `true`。`false` 时创建后自动 publish。
- `assets: []asset`：可选，要上传的附件列表（不支持 glob，逐个列出）。每个 asset 可以是：
  - 纯字符串：文件路径（asset 名默认为文件 basename）
  - 对象形式：`path`（必填）+ `name`（可选，覆盖 asset 文件名）

`with.tag`、`with.notes`、`with.assets[].path`、`with.assets[].name` 均支持 `${{ steps.<id>.outputs.<key> }}` 插值。

`release` 步骤是 **runner 内建步骤**：runner 直接调用平台 release API（`POST /api/repos/{owner}/{name}/releases` 等），不经过 docker exec shell。文件从当前 job checkout/workdir 读取。

**固定 outputs**（成功后自动写入，可供后续 step 引用）：

| output key | 类型 | 说明 |
|---|---|---|
| `release_id` | string | 创建的 release ID |
| `tag` | string | release 的 tag 名 |
| `draft` | string | `"true"` 或 `"false"` |
| `published` | string | `"true"` 或 `"false"` |
| `release_url` | string | release 页面 URL |

##### 未知 `type`

指定 `type` 为 `run` / `release` 之外的任意字符串会触发解析错误。

内置类型 step 的参数通过 `with:` 传入（见上）；不支持外部 `uses:` action。

v1 **不支持**：

- `uses:` marketplace/reusable action（仅支持内置 `type`）
- `if:` 条件执行
- `services:` sidecar/service containers
- `artifacts` / `cache`
- `matrix`

> **解析容错**：配置解析对未知字段是宽松的——step / job / `on` 触发器下的未知键会被忽略而非报错。仅强制结构性要求（必填项、格式、未知 `type`/未知 event）。

#### Step outputs

每个 step 在成功退出（exit code 0）后可产出若干 key=value 输出，供后续 step 通过 `${{ steps.<id>.outputs.<key> }}` 引用。

**产出方式**：step 将 `key=value` 行写入 `$HANGRIX_STEP_OUTPUT_FILE`（runner 注入的环境变量，指向 `/tmp/hangrix/step-output-<id>`）：

```bash
echo "version=$(cat version.txt)" >> "$HANGRIX_STEP_OUTPUT_FILE"
echo "release_id=$RELEASE_ID" >> "$HANGRIX_STEP_OUTPUT_FILE"
```

**引用方式**：后续 step 的 `run` 中使用 `${{ steps.<id>.outputs.<key> }}`：

```yaml
steps:
  - id: build
    run: |
      make build
      echo "version=$(cat VERSION)" >> "$HANGRIX_STEP_OUTPUT_FILE"
  - id: deploy
    run: |
      echo "Deploying version ${{ steps.build.outputs.version }}"
      ./deploy.sh --version "${{ steps.build.outputs.version }}"
```

**约束**：
- 仅成功 step（exit code 0）的输出被捕获
- 引用不存在的 step id 或 output key 会导致 job 直接失败（不静默替换为空字符串）
- output key 约束 `[a-zA-Z_][a-zA-Z0-9_-]*`
- 输出值中若匹配到 repo variable/secret 的值，会被标记为 `masked`，server 端展示为 `***`
- runner 仅展开 `steps.<id>.outputs.<key>`；`env.<KEY>`、`inputs.<name>`、`jobs.<job>.outputs.<key>` 由 server 在 dispatch 时展开

### 严格校验规则

- 顶层、事件、job、step 都**严格拒绝未知键**
- `version: 1` 必填
- `name` 仓库内唯一
- `on` 至少包含一个事件
- `jobs` 至少一个 job
- 每个 job 至少一个 step
- `dir` 必须在 `/workspace` 子树内
- env key 必须匹配 `[A-Z_][A-Z0-9_]*`

---

## 完整示例

### 示例 1：push 触发 CI

```yaml
version: 1
name: ci
on:
  repo.push:
    branches: [main]
    paths: ["apps/**", "pkg/**", "go.work", "go.work.sum"]
env:
  GOFLAGS: -mod=readonly
jobs:
  fmt:
    steps:
      - run: gofmt -w .
      - run: git diff --exit-code
  test:
    timeout_minutes: 20
    steps:
      - run: go test ./...
```

语义：

- `main` 分支 push 且命中指定路径时触发
- 先跑 `fmt`，成功后再跑 `test`
- 任一 step 非零退出码即该 job 失败，run 失败

### 示例 2：手动 dispatch 指定 ref

```yaml
version: 1
name: release-check
on:
  workflow.dispatch:
    inputs:
      ref:
        required: true
      reason:
        required: false
jobs:
  verify:
    steps:
      - run: git rev-parse "$WORKFLOW_INPUT_REF"
      - run: go test ./...
```

语义：

- 仅能通过手动触发运行
- dispatch 请求中的 `ref` 会注入为 `WORKFLOW_INPUT_REF`

### 示例 3：issue comment 触发仓库脚本

```yaml
version: 1
name: comment-check
on:
  issue.comment:
    from_users: [hempflower]
jobs:
  check:
    steps:
      - run: ./scripts/check-comment-workflow.sh
```

语义：

- 指定用户发表评论时运行
- event payload 通过环境变量提供给脚本读取

---

## 容器与环境继承规则

workflow job 的运行环境直接从 `.hangrix/agents.yml` 的顶层 `container` 继承。

### `image` / `build`

- 若 `container.image` 存在：workflow job 使用该镜像
- 若 `container.build` 存在：workflow job 使用同一 build 规范构建出的镜像
- workflow 文件**禁止**单独声明 image/build

这样能确保：agent 与 workflow 在同一仓库下共享同一开发环境，不出现“agent 在 A 镜像、workflow 在 B 镜像”的分裂。

### `entrypoint`

- workflow job 复用 `container.entrypoint`
- 若未配置，runner 仍使用其默认长驻 PID 1（当前为 `sleep infinity`）
- 实际 job step 通过 `docker exec` 在容器中执行 `bash -lc <run>`

### `env`

合并顺序：

```text
container.env
  <- workflow.env
  <- job.env
  <- platform runtime env
```

说明：

- 后者覆盖前者同名 key
- platform runtime env 包括 workflow 运行上下文，例如：
  - `HANGRIX_WORKFLOW_RUN_ID`
  - `HANGRIX_WORKFLOW_NAME`
  - `HANGRIX_WORKFLOW_JOB_KEY`
  - `HANGRIX_REPO_OWNER`
  - `HANGRIX_REPO_NAME`
  - `HANGRIX_EVENT_NAME`
  - `HANGRIX_EVENT_CAUSE_ID`
  - `HANGRIX_CHECKOUT_REF`（checkout ref，例如 `refs/heads/main` 或 `refs/tags/v1.2.3`）
  - `HANGRIX_COMMIT_SHA`（对应 commit sha）
  - `HANGRIX_TAG`（仅 `repo.push_tag` 事件注入，short tag name，如 `v1.2.3`）
  - `HANGRIX_PLATFORM_BASE_URL`（平台 API 地址，供 step 内 `curl` 调用平台 API）
  - `HANGRIX_WORKFLOW_TOKEN`（workflow 级 scoped token，供 step 内调用平台 API 时的认证）
  - `HANGRIX_STEP_OUTPUT_FILE`（当前 step 的输出文件路径，step 将 `key=value` 行写入该文件即可产出 outputs）
- `workflow.dispatch.inputs` 以 `WORKFLOW_INPUT_<UPPER_SNAKE_NAME>` 注入

### `volumes`

- workflow job 复用 `container.volumes`
- 语义与 agent session 相同：仓库级 named cache
- v1 不允许 workflow 自己追加额外 volume，避免引入第二套挂载权限面

### 工作目录

- 容器内工作目录固定挂到 `/workspace`
- `job.dir` 默认 `/workspace`
- 允许设置为 `/workspace/subdir`
- 禁止逃逸到 `/` 或其它挂载点

---

## 触发模型

### 事件来源

workflow 系统消费的是**平台事件总线中的另一条分支**，而不是 agent spawner 的 role trigger。

建议事件面：

- `repo.push`
- `repo.push_tag`
- `issue.opened`
- `issue.comment`
- `workflow.dispatch`

其中：

- `repo.push` 是新增平台事件
- `issue.opened` / `issue.comment` 可复用现有 issue 事件源，但由 workflow scheduler 独立订阅与判定

### 触发到执行的流程

```text
platform event
  -> workflow scheduler scans repo workflow definitions at target ref
  -> match workflows by `on`
  -> create workflow_run
  -> expand first job into pending workflow_job
  -> runner claims workflow_job task
  -> runner executes steps in container
  -> server records logs + job status
  -> if success, enqueue next job; if failed, mark remaining skipped
  -> mark workflow_run terminal
```

### checkout ref 规则

v1 统一为“事件对应 ref 的一次性 checkout”：

- `repo.push`：checkout 到被 push 的 commit sha
- `repo.push_tag`：checkout 到 `refs/tags/<tag>`，commit sha 为该 tag 指向的 commit
- `issue.opened` / `issue.comment`：checkout 到该 issue 所属仓库的默认分支最新 sha
- `workflow.dispatch`：
  - 若传 `ref`，checkout 指定 ref
  - 否则 checkout 默认分支最新 sha

与 agent session 的关键差异：

- workflow **不复用长期 issue working tree**
- 每个 workflow job 使用**一次性工作目录**
- 同一个 workflow run 内的多个 job 默认共享同一 checkout 快照，但不要求共享同一长驻容器

v1 为了最小实现，可以采用：**每个 job 独立新建容器 + 独立 checkout 同一 ref**。这样实现简单，也避免把 agent 的长生命周期容器语义硬搬进 workflow。

---

## 运行时模型

### 数据模型

建议新增独立模块与表：

- `workflow_definitions`（可选，不必首期落库；首期可现读现算）
- `workflow_runs`
- `workflow_job_runs`
- `workflow_job_logs`（或复用 append-only log 表）

建议字段：

### `workflow_runs`

```text
id
repo_id
workflow_name
source_file
status            -- pending | running | success | failed | cancelled
event_name        -- repo.push / repo.push_tag / issue.opened / issue.comment / workflow.dispatch
cause_id          -- push id / comment id / dispatch id
ref
commit_sha
default_branch_snapshot
container_snapshot_json
trigger_payload_json
started_at
finished_at
created_at
```

### `workflow_job_runs`

```text
id
workflow_run_id
job_key
display_name
status            -- pending | running | success | failed | skipped | cancelled
sequence_index
working_directory
timeout_minutes
runner_id
container_id
started_at
finished_at
exit_code
error_message
created_at
```

### `workflow_job_logs`

```text
id
workflow_job_run_id
stream            -- stdout | stderr | system
line
created_at
```

> 若实现时新增表需要跨模块引用 `repos`、`runners`、`issues` 等，请复读 `.hangrix/knowledge/sqlc-and-migrations.md`，按现有 sqlc schema-union 方式处理跨模块 FK 与代码生成边界。

### 容器快照

为保证审计与复现，`workflow_runs.container_snapshot_json` 应缓存：

- image 或 build 解析结果
- entrypoint
- env（不含 secret 明文，可保留 key 列表和解析后的非 secret 值）
- volumes
- source ref / commit sha

原则与 `agent_sessions` 的配置冻结点一致：**run 创建时拍快照，后续不追随仓库配置变化**。

---

## Runner 协议扩展

参考 `docs/runner-protocol.md`，建议做最小扩展而不是另起一套 workflow runner API。

### `/api/runner/tasks`

现有响应扩展为带 `kind` 的联合类型：

```json
{
  "kind": "workflow_job",
  "workflow_job": {
    "job_run_id": 42,
    "workflow_run_id": 9,
    "repo_id": 6,
    "workflow_name": "ci",
    "job_key": "test",
    "checkout_ref": "refs/heads/main",
    "commit_sha": "abc123",
    "container": {
      "image": "ghcr.io/acme/dev:1.2.3",
      "build": null,
      "entrypoint": ["/usr/bin/sleep", "infinity"],
      "env": {
        "GOFLAGS": "-mod=readonly"
      },
      "volumes": [
        {"name": "go-mod", "mount": "/go/pkg/mod"}
      ]
    },
    "working_directory": "/workspace",
    "steps": [
      {"name": "Run tests", "run": "go test ./..."}
    ],
    "timeout_minutes": 20,
    "repo_variables": {
      "OPENAI_API_KEY": "..."
    }
  }
}
```

agent 任务保持：

```json
{ "kind": "agent_session", "agent_session": { ...现有 task shape... } }
```

这样 runner 可以在同一 worker loop 中按 `kind` 分发到不同 driver。

### 新增 runner driver

runner 侧建议新增：

- `internal/loop/workflow.go`：`WorkflowJobDriver`

职责：

1. 准备一次性工作目录
2. clone 仓库并 checkout 指定 `commit_sha/ref`
3. 启动容器（复用 `orchestrator.Start`）
4. 逐 step `docker exec bash -lc <run>`
5. 按行回传 stdout/stderr
6. 上报 job terminal 状态

### 日志与终态上报

建议新增 workflow 专用端点，而不是复用 session 消息端点：

- `POST /api/runner/workflow-jobs/{id}/running`
- `POST /api/runner/workflow-jobs/{id}/logs`
- `POST /api/runner/workflow-jobs/{id}/terminate`

原因：

- 避免把 workflow 日志混进 agent message history
- 避免 agent session 终态枚举与 workflow job 终态枚举强行对齐

---

## Server 侧建议落点

### 新模块

建议新增：

```text
apps/hangrix/internal/modules/workflow/
  domain/
  service/
  infra/
  handler/
  module.go
```

### 主要职责

- `domain/`
  - workflow run / job run / trigger model
  - store / scheduler / dispatcher interface
- `service/`
  - workflow 文件解析与校验
  - 事件匹配
  - run 创建
  - job 串行推进
  - 手动 dispatch
- `infra/`
  - migrations
  - sqlc queries
  - repo 文件读取
- `handler/`
  - 管理/查看 API
  - 手动触发 API
  - runner workflow job callback API

### 配置解析

不要把 workflow schema 塞进现有 `agentsconfig.HostConfig` 里。建议：

- 保留 `apps/hangrix/internal/agentsconfig/` 专注 `agents.yml` 与现有 `automation.yml`
- 新增 `apps/hangrix/internal/workflowsconfig/` 或 `agentsconfig/workflow.go` 的独立 parser/validator

若放进 `agentsconfig/`，也必须保持**类型与入口独立**，避免 `HostConfig` 变成“大一统仓库配置对象”。

---

## Web / API 边界

v1 不做 workflow YAML 可视化编辑器。

Web 首期建议只做可观测性：

- workflow run 列表
- run 详情页（jobs、状态、日志）
- 手动 dispatch 按钮（仅对声明了 `workflow.dispatch` 的 workflow）

可参考现有：

- `apps/web/app/components/repo/AutomationSettings.vue`
- `apps/web/app/types/automation.ts`

但 workflow 不应复用 automation 的编辑式页面模型。更合适的是新增只读/触发型类型，例如：

- `apps/web/app/types/workflow.ts`
- `apps/web/app/components/repo/WorkflowRuns.vue`

---

## 错误处理与状态语义

### workflow run 状态

- `pending`
- `running`
- `success`
- `failed`
- `cancelled`

### workflow job 状态

- `pending`
- `running`
- `success`
- `failed`
- `skipped`
- `cancelled`

### 失败语义

- step 命令退出码非 0：当前 job `failed`，workflow run `failed`
- job 超时：当前 job `failed`，`error_message=timeout`
- checkout 失败 / 镜像构建失败 / runner 启动失败：当前 job `failed`
- 配置解析失败：不创建 run，只在后台日志与仓库检查面暴露错误

---

## 安全边界

- workflow shell step 本质上是仓库代码执行能力，因此权限边界应与当前 agent 容器等价
- 不允许 workflow 自定义额外 host mount、privileged、service container
- repo variable / secret 继续通过 runner 下发并在启动前展开，不回写到日志
- 手动 dispatch 需要与 repo 写权限或管理权限绑定

---

## v1 明确不做

- GitHub Actions marketplace `uses:`
- reusable workflows
- matrix / 并行 job DAG
- artifact / cache 新系统（仅复用现有 container.volumes）
- service containers
- 条件表达式 `if:`
- workflow 可视化编辑器
- 与 agent session 共享同一长驻容器

---

## 演进路径

### Phase 1

- `.hangrix/workflows/*.yml` schema 与 parser
- `workflow.dispatch` + `repo.push` 两类触发
- 串行 jobs、shell steps
- 独立 run/job/log 持久化
- runner `kind=workflow_job` 分流
- Web run 列表/详情

### Phase 2

- `issue.opened` / `issue.comment` 触发
- cancel run
- 更丰富的过滤器与日志检索

### Phase 3

- `needs` DAG
- 并行 job
- artifact / outputs
- 更强的权限与并发控制

---

## 一句话结论

workflow 系统应被设计为：**仓库内 YAML 声明、容器环境复用 `agents.yml`、运行模型独立于 agent session、执行层复用现有 runner/orchestrator 的另一条任务通道**。这样既能满足“像 GitHub workflow”的用户心智，又不会把现有 agent 调度链条搅乱。
