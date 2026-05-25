# Runner ↔ 服务端协议

[← ROADMAP](../ROADMAP.md)

`hangrix-runner` 是独立二进制，跟服务端用 outbound-only HTTP 说话（不要求 runner 暴露端口）。一台机器跑一个进程，负责：拉容器、bind-mount agent 二进制、注入凭证、转发 stdin/stdout、上报状态。

## 二进制布局

```
apps/hangrix-runner/
├── cmd/hangrix-runner/main.go
└── internal/
    ├── cli/                Enroll / Serve 两个 subcommand
    ├── config/             CLI 解析 + 默认值
    ├── store/              ~/.hangrix/state.json 原子写
    ├── client/             覆盖所有 /api/runner/* 端点的 HTTP 客户端
    ├── orchestrator/       Orchestrator 接口 + Docker 实现 + Fake 实现（测试）
    └── loop/               外层心跳/取任务循环 + 单 session 驱动器
```

依赖原则：**stdlib only**（外加平台共享的 `pkg/ioc`）。Runner 不打算长出 agent framework / SDK 抓依赖。

## Token 三联

```
hgxe_<...>   enrollment       一次性     POST /api/runner/enroll 换 hgxr_
hgxr_<...>   agent (runner)    长期      Bearer 在所有其它 /api/runner/* 上
hgxs_<...>   session (agent)   一会话    server 在 pollTasks 下行给 runner，runner 注入容器
```

详细 token 设计见 [agent-identity.md](agent-identity.md)。

## Bootstrap 契约

**"`enroll` 一次性下发，`serve` 零必填参数"**。Runner CLI 只有 `--server` + `--token` 给 enroll，`serve` 只接 `--state-dir` 和 `--docker`。其余从服务端拿。

`POST /api/runner/enroll` 响应或 `GET /api/runner/bootstrap` 都返：

```json
{
  "binaries": {
    "hangrix-agent":   {"url": "/api/runner/binaries/hangrix-agent",   "sha256": "...", "size": 12345},
    "hangrix-runner":  {"url": "/api/runner/binaries/hangrix-runner",  "sha256": "...", "size": 67890}
  },
  "base_url":              "http://server",
  "default_agent_image":   "ghcr.io/...",
  "poll_wait_sec":          20,
  "heartbeat_sec":          20
}
```

`base_url` 是 agent 调平台一切后端的统一锚点。Agent 自己拼 `<base>/api/llm/v1/responses`（chat completions）和 `<base>/api/agent/tools/<name>`（POST 调用每个平台工具）。Runner 把 base 注入容器 env (`HANGRIX_PLATFORM_BASE_URL`)；用 sha256 作 content-addressed 缓存 key 维护 `~/.hangrix/agent-binaries/<sha>/`。`serve` 启动时 `GET /bootstrap` 一次刷新，sha 变了重新下载。

## 端点表

| Method + Path | 鉴权 | 用途 |
|---|---|---|
| `POST /api/runner/enroll` | `hgxe_` | 换 `hgxr_` + bootstrap |
| `GET  /api/runner/bootstrap` | `hgxr_` | 重新拉一次 endpoints + binary 元数据 |
| `POST /api/runner/heartbeat` | `hgxr_` | 上报 capabilities（os/arch/go 等） |
| `GET  /api/runner/tasks` | `hgxr_` | 长轮询 20s 拉 pending session（含 `session_token` plaintext） |
| `POST /api/runner/sessions/{sid}/running` | `hgxr_` | agent 首帧 `kind:ready` 时触发 |
| `POST /api/runner/sessions/{sid}/messages` | `hgxr_` | 一行 stdout 一次 POST |
| `GET  /api/runner/sessions/{sid}/inputs` | `hgxr_` | 长轮询 stdin frames（history + 平台事件） |
| `POST /api/runner/sessions/{sid}/terminate` | `hgxr_` | 终态上报 `status + exit_code + message` |
| `GET  /api/runner/binaries` | `hgxr_` | 列 `{name → metadata}` |
| `GET  /api/runner/binaries/{name}` | `hgxr_` | 流式下载 |

> ~~`GET /api/runner/agent-bundles/...`~~ M7a 上线时存在的 agent 仓库 tarball 端点已随 agent-as-repo 设计取消，M7c cleanup 阶段下线。

服务端 `pollTasks` 走 `ClaimNextSession`（`FOR UPDATE SKIP LOCKED`），多 runner 并发不撞同一行。Session token plaintext 在这一次响应里下行 —— 这是唯一允许 plaintext 离开 server 的时机。

## SessionDriver

`SessionDriver.Run(ctx, task)` 起一次容器后 3 个 goroutine 扇出：

```
                ┌─► shipStdin     poll /inputs → 写容器 stdin
                │
container ──────┼─► shipStdout    扫容器 stdout → POST /messages（一行一次）
                │
                └─► shipStderr    扫容器 stderr → POST /messages kind=log
```

Wait 拿到 exit code 后 POST `/terminate` 带 `status + exit_code`。整个生命周期 5 秒内闭环（fake 链路；真 docker 加上 image pull 时间）。

## Docker 启动命令

```
docker run --rm -i
  --network bridge           # 默认隔离；HANGRIX_RUNNER_DOCKER_NETWORK 可覆盖
  --entrypoint /usr/local/bin/hangrix-agent
  --workdir /workspace
  -v <runner-cache>/hangrix-agent:/usr/local/bin/hangrix-agent
  -v <prompt-file>:/opt/hangrix/role_prompt.md:ro
  -v <session-tmp>:/workspace
  -e HANGRIX_SESSION_TOKEN=...
  -e HANGRIX_PLATFORM_BASE_URL=...
  -e HANGRIX_SESSION_ID=...
  -e HANGRIX_LLM_MODEL=...
  -e HANGRIX_ROLE=...
  -e HANGRIX_MCP_SERVERS=...
  ...
  <image>
```

> 默认网络改为 Docker 内置 `bridge`，确保 agent 容器与宿主、与其他 session 互相隔离。Agent 通过 `HANGRIX_PLATFORM_BASE_URL` 访问平台 —— 该地址必须从 bridge 网络内可达（Docker Desktop 用 `host.docker.internal`、裸机用宿主可路由 IP、docker-compose / devcontainer 部署设 `HANGRIX_RUNNER_DOCKER_NETWORK=<server 所在的 user-defined bridge>`）。需要回到旧的"共享宿主网络栈"行为时显式设 `HANGRIX_RUNNER_DOCKER_NETWORK=host`。

`role_prompt.md` 是从 session snapshot 写出的临时文件 —— 内容就是 host yaml 的 `roles.<key>.prompt` inline body 或 `prompt_file` 解析后的 markdown。env 指过去给 agent 读。

## Role prompt 供给

Spawn 时 server 已经把 role 解析后的 prompt 内容快照进 `agent_sessions.role_config.prompt`（见 [agent-config.md](agent-config.md)）。Runner 拉任务时直接拿到 prompt 字符串，写一份临时文件 bind-mount 进容器。**没有独立的 agent bundle 分发链路** —— prompt 在 session row 里，跟着 task 一起下行。

## MCP 服务器白名单下发

Task payload 新增 `mcp_servers` 字段（`[]string`），是 role 在 `agents.yml` 中声明的 MCP 服务器白名单（如 `mcp: ['playwright']`）。Runner 在 `buildAgentEnv` 阶段将其编码为逗号分隔的 `HANGRIX_MCP_SERVERS` 环境变量注入 agent 容器。

Agent 侧行为：
- `HANGRIX_MCP_SERVERS` 为空或未设置：不加载任何 MCP 服务器（包括 `.mcp.json` 已有的）。
- 设置了服务器名列表：仅加载白名单内的服务器；若某个服务器名在 `.mcp.json` 中不存在，session 显式失败（panic），因为这是 host 配置错误。

> 历史注记：M7a 上线时设计过「agent 仓库 bundle 分发」（content-addressed 缓存、sha256 校验、pre-spawn agent 仓库可达性验证）。随 agent-as-repo 设计取消，整段链路（`GET /api/runner/agent-bundles/...` 端点 / `~/.hangrix/agent-bundles/` 缓存 / `agent_sessions.agent_repo` 列）在 M7c cleanup 一并下线。

## Fake orchestrator（测试用）

`internal/orchestrator/fake.go` 用三对 `io.Pipe` 实现 `Orchestrator` 接口；测试侧把"agent"换成一个 goroutine 写 stdout，可以脱离 docker 跑端到端验收（见 `internal/loop/session_test.go`）。

## Task payload — repo variables 下发

Runner 在 `GET /api/runner/tasks` 拉到的 task 载荷中，除 `Env`（session 原始 env map，含 spawner 写入的 `${VAR_NAME}` 引用）、`SessionToken`、`ContainerID` 外，还包含：

```json
{
  "repo_variables": {
    "OPENAI_API_KEY": "sk-abc123",
    "NPM_AUTH_TOKEN": "npm_xxx"
  },
  "mcp_servers": ["playwright"]
}
```

- `repo_variables` 是 `map[string]string`，key 为变量名，value 为明文（机密变量已在服务端解密后下发）。
- Runner 在 `buildAgentEnv()` 之前调用 `expandEnv(env, repoVars)` 对 `Env` 做 `${VAR_NAME}` 整值展开（仅 `FOO: ${BAR}` 形状；`FOO: prefix-${BAR}` 不做部分替换）。展开失败（引用不存在的变量名）时 session 明确失败并返回缺失名，不静默注入空串。
- `repo_variables` 为 `nil` 表示服务端尚未升级（向后兼容，`${...}` 引用不做展开也不报错）；空 non-nil map 表示服务端已升级但仓库无变量（`${...}` 引用明确报错）。
- `mcp_servers` 是 `[]string`，从 session 冻结的 `role_config` 中提取。非空时 runner 将其注入 agent 容器（如 `HANGRIX_MCP_SERVERS=playwright`），agent 按此白名单过滤 `.mcp.json` 中的服务器加载；空/nil 时 agent 不加载任何 MCP 服务器。

## Workflow job task payload

当 task `kind` 为 `"workflow_job"` 时，载荷中包含 `workflow_job` 字段。其中 `steps` 数组的每一项是一个 **typed step**：

```json
{
  "kind": "workflow_job",
  "workflow_job": {
    "steps": [
      {
        "id": "build",
        "name": "Build artifacts",
        "type": "run",
        "run": "make release"
      },
      {
        "id": "create-release",
        "name": "Create release",
        "type": "release",
        "tag": "v1.0.0",
        "notes": "Release notes",
        "draft": false,
        "assets": [
          {"path": "dist/app.tar.gz", "name": "app-linux-amd64.tar.gz"}
        ]
      }
    ]
  }
}
```

### Step 类型

| type | 执行方式 | 专属字段 |
|---|---|---|
| `run`（默认） | `docker exec bash -lc <run>` | `run` |
| `release` | 内建：调用平台 release API | `tag`, `notes`, `draft`, `assets[]` |

- `type` 省略或为空时等价于 `"run"`。
- `release` step 的 `assets[]` 中每项包含 `path`（必填）和 `name`（可选，覆盖上传后的文件名）。文件从当前 checkout/workdir 读取。
- `release` step 执行成功后将 outputs（`release_id`, `tag`, `draft`, `published`, `release_url`）写入 `/step-result` 回调。

### 回调端点

Workflow job 使用以下 runner 回调（Bearer `hgxr_` token）：

| Method + Path | 用途 |
|---|---|
| `POST /api/runner/workflow-jobs/{jobRunID}/running` | 标记 job 进入 running |
| `POST /api/runner/workflow-jobs/{jobRunID}/logs` | 追加日志行 |
| `POST /api/runner/workflow-jobs/{jobRunID}/step-result` | 上报单个 step 的输出 |
| `POST /api/runner/workflow-jobs/{jobRunID}/terminate` | 上报 job 终态 |

## 不在本设计里的事

- **多 session 并发：** v1 一 runner 一时刻一 session，串行消化。M7a 起按 `runner.capabilities.parallelism` 扩。
- **podman / containerd：** v1 只支持 docker（`os/exec docker run`）。换运行时实现新的 `Orchestrator`。
- **Runner-side autoscaling：** runner 不主动伸缩自己；admin / 用户自己起多个进程，server 端按 capabilities 调度。
- **Inbound webhook：** runner 永远不开端口，所有通信 outbound。

## Agent stdin 投喂与 mid-loop 吸收

### 三个边界

| 信号 | 含义 | 发送方 | 接收方动作 |
|---|---|---|---|
| `done` | **单次 turn/event 边界**。Agent 完成了一轮 LLM⇄tool 循环（assistant 返回无 `tool_calls` 且无新输入待处理）。 | agent stdout | runner 记录，等待下一个 `idle` 或新输出 |
| `idle` | **容器可复用/可退役边界**。Agent 完成了当前事件的一切处理，park 在 inbox select 上等待下一个 frame。 | agent stdout | runner 启动退役计时器；若超时则 `control:shutdown` |
| mid-loop 吸收 | Agent **非 idle 状态下**接收到的新 `event` / 异步通知，被折叠进当前上下文在安全边界（LLM 请求返回后、下一次请求前）被模型看见。 | (runner 投喂 stdin，agent 内部处理) | agent loop 在 `drainPending` 时消费，不影响 `idle` 语义 |

### Runner 侧约束

- `shipStdin` **不关心 agent 是否正在一轮 loop 内** —— 只要 session 存活且 poll context 未被取消，就持续 `POST /inputs` → 写容器 stdin。
- `watchIdle` 退役语义**不变**：只有 agent 显式发出 `idle` 后，runner 才进入空闲退役计时。
- 若退役已触发并停止 polling，新到输入仍留在平台 `/inputs` 队列中，后续由重启/重建的 agent 容器继续消费。

### Agent 侧安全边界

1. **LLM 请求进行中**：新 event 被 `applyInboxItem` 追加为 user-role 消息到上下文，**不取消**正在进行的 HTTP 调用。新输入在下一个 round 开始时被模型看见。
2. **tool results 全部回填后、下一次 LLM 请求前**：`drainPending` 在每轮顶部消费所有已排队的 inbox items。
3. **禁止插入点**：新输入**绝不会**插入到 `assistant(tool_calls=…)` 与紧随的 `tool(result)` 之间 —— `applyInboxItem` 追加到当前消息列表末尾，而 tool 结果在 `dispatch` 循环中紧随 assistant 消息被追加。
4. **No-tool-call 扩轮**：若某轮 LLM 返回无 `tool_calls`，但该轮期间有新输入被折叠进上下文（`postCallLen > preCallLen`），agent **不会**立即输出 `done`，而是继续至少再跑一轮 LLM 以响应该新输入。

