# 端到端 smoke 流程

[← ROADMAP](../ROADMAP.md) · [← agent-config.md](agent-config.md) · [← M7a smoke](agent-session-smoke.md)

[`scripts/smoke/`](../scripts/smoke/) 是一套自包含的 docker compose smoke 环境：postgres + redis + server + runner 一键起，agent 容器加入同一张网桥；run.sh 把建账号 / 推 fixture / 开 issue / 跟 timeline 全部脚本化。

当前验证 **M7b 退出条件**——dispatcher → backend → reviewer → maintainer 四 role 在真容器 + 真 LLM 下走完整圈。harness 本身跟里程碑无关，后续 M7c / M8 / M9 加新 role / 工具时复用同一套 compose。

整个 smoke 完整一趟约 1.5–2 分钟（取决于 LLM 延迟），4 个 LLM round-trip（每 role 一次）。

## 0. 前置环境

- 能跑 docker 的机器，`docker info` 不报错（compose v2 已捆绑在 docker CLI 中）。
- jq、curl、git 命令可用（用 apt/brew 装）。
- 一份能用的 OpenAI-compat LLM key（默认 DeepSeek，可改其它）。
- 8080 端口闲置（compose 把 server 暴露在 :8080）。

## 1. 配 `.env`

```bash
cd scripts/smoke
cp .env.example .env
$EDITOR .env   # 至少填 LLM_API_KEY；HANGRIX_SMOKE_STATE_DIR 可不填，run.sh 自动算
```

`HANGRIX_SMOKE_STATE_DIR` 必须是 **绝对路径**——runner 容器把这个目录 bind-mount 到自己内部的同名路径，docker daemon 顺着同一条 host path 找到 cache 和 binaries。run.sh 默认填 `<repo-root>/data/smoke`，跨平台一致。

`LLM_MODEL=deepseek-v4-pro` 走 DeepSeek 的 reasoner 系；要更便宜可换 `deepseek-chat`（无 thinking mode）。

## 2. 一键 up + setup

```bash
bash scripts/smoke/run.sh setup
```

底下做了什么：

1. `docker compose build` —— Dockerfile 多阶段构建：
   - builder 阶段编译 `hangrix-agent` → 拷进 `apps/hangrix/.../payload/` → 编译 `hangrix` + `hangrix-runner`。
   - server 阶段：ubuntu:24.04 + git + ca-certificates，bake 进 `/usr/local/bin/hangrix`。
   - runner 阶段：ubuntu:24.04 + docker.io（CLI）+ ca-certificates，bake 进 `/usr/local/bin/hangrix-runner`。
2. `docker compose up -d postgres redis server agent-image-bake` —— 启 DB / Redis / 平台 server，并 bake `hangrix-smoke-agent:latest`（agent 容器基镜像）。
3. 在 server 上注册 `smoke-admin` + `smoke` org + LLM provider `deepseek`。
4. 调 admin API 申请 enroll token，跑 `docker compose run --rm runner enroll`，state.json 落到 bind-mount 的 `$HANGRIX_SMOKE_STATE_DIR`。
5. `docker compose up -d runner` —— 长跑 runner，捡 pending session。
6. 推 4 个 agent 仓库（dispatcher/backend/reviewer/maintainer）+ host 仓库；每个走「开 issue → push 到 issue/N → API merge」绕过 M4 IssueGuard（详见 §4）。

整个 setup 是幂等的——重跑会跳过已经存在的资源。

## 3. 跑 smoke

```bash
bash scripts/smoke/run.sh smoke
```

脚本会：

1. 在 `smoke/host` 上 POST 一个 issue「smoke: add /healthz」。
2. 每 5 秒拉一次 `/api/admin/agent-sessions/by-issue/{repo_id}/{n}` + `/api/repos/.../timeline`，打印当前 session roster + 最近 12 条 timeline。
3. 检测到 issue 状态变 `merged` 时退出。

预期序列（具体时间因 LLM 而异）：

| 阶段 | session roster | timeline |
|---|---|---|
| issue.opened | dispatcher: pending → running | （issue 主体） |
| dispatcher 完成 | dispatcher: succeeded | `comment by agent:dispatcher: @agent-backend please add /healthz` |
| comment.mentioned | + backend: pending → running | 同上 |
| backend push 完成 | backend: succeeded | `event commit_pushed by agent:backend` |
| commit.pushed | + reviewer: pending → running | （diff 出现） |
| reviewer vote | reviewer: succeeded | `event review_vote by agent:reviewer (value=approve)` |
| review_vote.posted | + maintainer: pending → running | |
| maintainer merge | maintainer: succeeded；其它全部 archived | `event branch_merged`, `event state_changed open→merged` |

到 issue 转 `merged` 即视为 M7b 退出条件通过。

## 4. M4 分支保护对 fixture push 的影响

M4 IssueGuard 强制每次代码变更都走 issue。run.sh 推 agent / host 仓库时也得遵循这个规则：

```
create_repo init_readme=true  → main 有 1 个 README commit
open_bootstrap_issue          → issue/1
git push issue/1              → fixture commit 落到 issue 分支
POST /api/repos/.../issues/1/merge → fast-forward 进 main
```

这是有意为之的设计取舍（roadmap M4 章节）。如果未来想放宽（让 owner 直接 push main），方向是把 IssueGuard 的 "default branch 受保护" 改成可配置的 branch_protections 字段，默认开。

## 5. 失败排查

| 现象 | 排查 |
|---|---|
| `setup` 中 server 起不来 | `bash run.sh logs server`；常见原因：8080 端口已被占用 / LLM_ENCRYPTION_KEY 解码失败。 |
| runner enroll 失败 | `bash run.sh logs runner`；agent_image_bake 没成功的话 runner 不会启动 —— 看 `bash run.sh logs agent-image-bake`。 |
| session 卡在 `pending` 几分钟不动 | runner 没起来，或者 docker daemon socket 挂载有问题。`bash run.sh shell runner` 进去 `docker ps` 看 daemon 是否可达。 |
| session 进 `running` 立刻 `failed` exit=126 | agent 二进制没有执行权限或架构不匹配。`bash run.sh shell runner` 进去看 `$HANGRIX_SMOKE_STATE_DIR/agent-binaries/<sha>` 是 ELF amd64 静态二进制。 |
| LLM 退 400 `reasoning_content must be passed back` | 用了 reasoner 模型但 agent 没正确 round-trip `reasoning` 帧 —— v0.1+ 已修；若 fork 改过 agent/llm 代码先回滚检查。 |
| `commit.pushed` 后没看到 reviewer 起来 | server.log 找 `spawner:` 行：`spawn role "reviewer"... violates foreign key`。说明 `iss.AuthorID` 没传，检查 `issue/handler/sync.go::fireCommitPushed`。 |
| backend push 失败 `403 forbidden` | `hgxs_` git Basic auth 没通；`bash run.sh shell server` 进去 `curl -u "x:<token>" localhost:8080/api/repos/smoke/host/refs` 复现，看是否 SessionTokenValidator 配错。 |

## 6. teardown

```bash
bash scripts/smoke/run.sh down
```

`docker compose down -v`，干净拆所有 service + 名字卷（postgres + repos）。下次 setup 从零开始。

只想停 server 不毁数据：`bash run.sh logs server` 改成 `docker compose stop server`（参考 compose 文档）。

## 7. compose 文件结构

```
scripts/smoke/
├── compose.yml               # 5 个 service，bridge network hangrix-smoke
├── Dockerfile                # multi-stage: builder → server → runner targets
├── server-config.yaml        # 服务端配置，DSN 走 service name
├── .env.example              # 复制到 .env 填 key / 模型
├── run.sh                    # build / up / setup / smoke / down / logs / shell
├── fixtures/                 # 4 个 agent 仓库 + host 仓库
│   ├── dispatcher/{agent.yml, prompts/system.md}
│   ├── backend/{...}
│   ├── reviewer/{...}
│   ├── maintainer/{...}
│   └── host/.hangrix/agents.yml
└── agent-image/Dockerfile    # ubuntu:24.04 + git + curl + ca-certificates
```

Compose service:

```
postgres            ← Postgres 17
redis               ← Redis 7
agent-image-bake    ← 一次性服务，构建 hangrix-smoke-agent:latest 镜像，然后退出
server              ← 平台 server，端口 8080 暴露到 host
runner              ← 跑 hangrix-runner serve；通过挂载 docker.sock 起 agent 容器
```

设计取舍：

- **path mirror**：runner 容器把 `${HANGRIX_SMOKE_STATE_DIR}` bind-mount 到容器内 **同一个绝对路径**。这样 runner 内部用什么路径，docker daemon 就在 host 同一条 path 上找到文件。避免了旧 devcontainer 版本里 `HANGRIX_RUNNER_HOST_PATH_MAP` 这类映射 hack。
- **agent container 网络**：runner 启 agent 时加 `--network hangrix-smoke`，agent 通过 docker DNS 拿到 `server`/`postgres`/`redis` 主机名。比 `--network host` 干净（host 网络在 devcontainer 套 docker 里根本到不了 server）。
- **状态持久化**：postgres-data + repos 两个 named volume；run.sh down -v 把它们都干掉。`$HANGRIX_SMOKE_STATE_DIR`（runner state + 缓存）是 host bind-mount，方便人工 inspect / `tail -F runner.log`。

## 8. 不在 smoke 内的事

- Swim-lane UI（M7c）：当前 smoke 只验后端链路；timeline 在 M4 单列视图里能看到 agent_role chip，但不分泳道。
- 真实 webhook 出口：v1 piggy-back 在 `issue_events` 上，没有独立 event_log 表。
- 大规模多 runner / 并发：v1 一 runner 串行消化；smoke 验单 runner 单容器一次一 trigger。
