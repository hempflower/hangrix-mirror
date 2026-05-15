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
  "llm_endpoint":          "http://server/api/llm/v1",
  "mcp_endpoint":          "http://server/api/mcp/v1",
  "default_agent_image":   "ghcr.io/...",
  "poll_wait_sec":          20,
  "heartbeat_sec":          20
}
```

Runner 把 endpoints 注入容器 env；用 sha256 作 content-addressed 缓存 key 维护 `~/.hangrix/agent-binaries/<sha>/`。`serve` 启动时 `GET /bootstrap` 一次刷新，sha 变了重新下载。

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
| `GET  /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz` | `hgxr_` | 流式下载 agent 仓库 tarball（M7a 起） |

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
  --network host
  --entrypoint /usr/local/bin/hangrix-agent
  --workdir /workspace
  -v <runner-cache>/hangrix-agent:/usr/local/bin/hangrix-agent
  -v <runner-cache>/agent-bundles/<agent_sha>:/opt/hangrix/bundle:ro
  -v <addendum-file>:/opt/hangrix/host_addendum.md:ro
  -v <session-tmp>:/workspace
  -e HANGRIX_SESSION_TOKEN=...
  -e HANGRIX_LLM_ENDPOINT=...
  -e HANGRIX_PLATFORM_MCP_ENDPOINT=...
  -e HANGRIX_SESSION_ID=...
  -e HANGRIX_LLM_MODEL=...
  -e HANGRIX_ROLE=...
  ...
  <image>
```

`host_addendum` 是临时文件（不假设 prompt 长度），env 指过去；bundle 由下面这套机制按需物化（M6c 整段 mount 留空也行）。

## Agent bundle 分发（M7a）

Task payload 里 server 下发 `agent_repo: <owner>/<name>@<sha>`（与 host yaml `agent:` 字段同形）。Runner 按 sha 在主机维护 content-addressed 缓存 `~/.hangrix/agent-bundles/<sha>/`：

- **命中**：直接 `-v <path>:/opt/hangrix/bundle:ro` 喂 docker。
- **未命中**：`GET /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz` → server 后台 `git archive --format=tar.gz <sha>` 流回（**`gzip -n`** 保 sha256 稳定、**无 `<repo>-<short>/` 包装**，解出即 `agent.yml` + `prompts/`）→ runner 收完算 sha256 对 response 头 `X-Hangrix-Sha256` → 解压到 `<sha>/.tmp` → 原子 `rename` 到 `<sha>/`。

只读挂载是契约：agent 看 bundle 不写 bundle。Server 端 endpoint 与现有 web `getArchive` 复用同一段 `git archive` exec，但 handler 层鉴权换成 runner `hgxr_` token、剥掉 prefix 包装。

**Pre-spawn 校验**（server 责任）：写 session 行前先验 `<owner>/<name>` 是合法 agent 仓库（根有 `agent.yml`、schema 合规）且 sha 在仓库内可达——否则拒绝 spawn，不让 runner 拉了个废 tarball 才报错。

**GC**：runner 启动期扫缓存目录，按 LRU + 容量上限（默认 1 GiB / 14 天，可调）触发清理。淘汰单位是整个 `<sha>/` 子目录。

> M6c → M7a 迁移：现行 `agent_sessions.bundle_dir TEXT` column 改名 `agent_repo`，含义从「runner 主机路径」变成「`<owner>/<name>@<sha>` 三元组」。M6c 没正式 ship 给外部用户，直接走 migration 改名 + drop 旧含义，不留 dead field。

## Fake orchestrator（测试用）

`internal/orchestrator/fake.go` 用三对 `io.Pipe` 实现 `Orchestrator` 接口；测试侧把"agent"换成一个 goroutine 写 stdout，可以脱离 docker 跑端到端验收（见 `internal/loop/session_test.go`）。

## 不在本设计里的事

- **多 session 并发：** v1 一 runner 一时刻一 session，串行消化。M7a 起按 `runner.capabilities.parallelism` 扩。
- **podman / containerd：** v1 只支持 docker（`os/exec docker run`）。换运行时实现新的 `Orchestrator`。
- **Runner-side autoscaling：** runner 不主动伸缩自己；admin / 用户自己起多个进程，server 端按 capabilities 调度。
- **Inbound webhook：** runner 永远不开端口，所有通信 outbound。
