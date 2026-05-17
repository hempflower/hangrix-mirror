# M7a Phase 2 端到端 smoke 流程

[← ROADMAP](../ROADMAP.md) · [← agent-config.md](agent-config.md)

Phase 2 退出条件全程涉及真容器 + 真 LLM + 真 runner，无法纯单测覆盖。下面这份手册是把"开 issue → role 自动起容器 → push 回来 → 关 issue 归档"完整跑一遍的最小复现步骤。代码层全部就位（见 `apps/hangrix/internal/modules/agent_session/`），跑通这份脚本即视为 M7a 完成。

## 0. 前置环境

- 一台能跑 docker 的机器，`docker info` 不报错。
- Server 进程跑起来（`config.yaml` 至少配好 DB / repos_path / llm.encryption_key）。
- 一台或多台 runner，至少有一台 visibility=platform 的处于 active。
- 一个有效的 LLM provider 已通过 `/api/admin/llm-providers` 注册，`allowed_models` 包含下面会用到的模型名（如 `claude-sonnet-4-6`）。

## 1. 准备 agent 仓库

最小可用 agent 仓库：

```
hangrix/test-agent/
├── agent.yml
└── prompts/
    └── system.md
```

`agent.yml`:

```yaml
version: 1
kind: agent
entry:
  base_prompt: prompts/system.md
declared_tools:
  - issue_read
  - issue_comment
  - read
  - write
  - bash
```

`prompts/system.md`:

```
You are a test agent for the M7a P2 smoke run. When you receive an issue.opened event:

1. Use `bash` to clone the working_branch:
     git clone --branch <working_branch> http://localhost:8080/<owner>/<repo>.git work
2. cd into work, create a file `hello.txt` with the content "hello from <role-key>".
3. git add hello.txt && git commit -m "smoke: hello from <role-key>"
4. git push origin <working_branch>
5. emit `done`.
```

Push it to the platform — the receive-pack hook recognises root `agent.yml` and classifies the repo as `kind=agent` (M7a P1).

## 2. 准备 host 仓库

任意 host 仓库，根加 `.hangrix/agents.yml`:

```yaml
version: 1
container:
  image: ghcr.io/hangrix/agent-base:latest
  env:
    GIT_TERMINAL_PROMPT: "0"
llm:
  model: claude-sonnet-4-6
roles:
  smoketest:
    agent: hangrix/test-agent@v0.0.1
    triggers: [issue.opened]
    can:
      - issue_read
      - issue_comment
      - read
      - write
      - bash
```

可选 `.hangrix/agents.lock`（强烈建议，避免 tag 漂移）：

```yaml
version: 1
agents:
  - ref: hangrix/test-agent@v0.0.1
    resolved_sha: <40 lowercase hex chars from `git rev-parse v0.0.1` on the agent repo>
    resolved_at: 2026-05-16T00:00:00Z
```

Push host 仓库；不需要 push 任何代码改动 —— `.hangrix/agents.yml` 在 default branch HEAD 即可。

## 3. 开 issue

```bash
curl -X POST -H "Cookie: hangrix_session=..." \
     -H "Content-Type: application/json" \
     -d '{"title":"smoke test"}' \
     http://localhost:8080/api/repos/<owner>/<host>/issues
```

期望立即发生（issue handler 的 `fireIssueOpened` 走同步路径）：

1. `agent_sessions` 多一行：`role_key='smoketest'`, `cause_kind='issue_opened'`, `status='pending'`, `agent_sha=<host yaml 解出的 sha>`, `repo_sha=<host repo default branch 当时的 sha>`。
2. `agent_session_inputs` 多两行：`{"kind":"history",...}` 和 `{"kind":"event","event":"issue.opened",...}`。

确认 SQL：

```sql
SELECT id, role_key, cause_kind, agent_sha, repo_sha, status
  FROM agent_sessions WHERE issue_number = <n>;
```

## 4. Runner 拉容器

某个 active 平台 runner 下一个 `pollTasks` 周期（默认 30s 内）会：

1. `ClaimNextSession` 拿到 row → status `pending → claimed`。
2. 拉 `GET /api/runner/agent-bundles/hangrix/test-agent/<sha>.tar.gz`，校验 X-Hangrix-SHA256，inflate 到 content-addressed 缓存。
3. `docker run --rm -i` 起 `ghcr.io/hangrix/agent-base:latest`，bind-mount bundle 到 `/opt/hangrix/bundle`，注入 env：
   - `HANGRIX_SESSION_TOKEN`（解封后的 plaintext）
   - `HANGRIX_ROLE_KEY=smoketest`
   - `HANGRIX_AGENT_SHA=<sha>` / `HANGRIX_REPO_SHA=<sha>` / `HANGRIX_CAUSE_KIND=issue_opened`
   - `GIT_AUTHOR_NAME=smoketest` / `GIT_AUTHOR_EMAIL=smoketest@agents.<host-domain>` / `GIT_COMMITTER_*` 同上
4. status `claimed → running`，agent 收到 history + issue.opened 两帧后开始干活。

## 5. Agent push 回来

Agent 容器内执行 prompt 描述的 5 步。`git push` 走 receive-pack：

- pre-receive 校验通过（issue 分支 push 是允许的，agent token 的 visibility 校验 / repo:write scope 都过）。
- Commit author 显示为 `smoketest <smoketest@agents.<host-domain>>`。

确认 SQL：

```sql
SELECT author_name, author_email, message
  FROM git_log_via_some_tool_or_API
 WHERE ref = 'refs/heads/issue/<n>'
 ORDER BY committed_at DESC LIMIT 1;
```

Issue timeline 里也会 append 一个 `commit_pushed` event（M4 已有，不依赖 M7a）。

## 6. Agent 结束

Agent 发 `done` → runner `terminate` → status `running → succeeded`，`session_token_sealed` 置 NULL。

## 7. 查 audit 链

```bash
curl -H "Cookie: hangrix_session=..." \
     http://localhost:8080/api/admin/agent-sessions/by-issue/<repo_id>/<issue_number>
```

期望返回的 JSON `items[0]`：

```json
{
  "session_id": ...,
  "role_key": "smoketest",
  "status": "succeeded",
  "agent_repo": "hangrix/test-agent@<sha>",
  "agent_sha": "<40-hex sha>",
  "repo_sha": "<40-hex sha>",
  "cause_kind": "issue_opened",
  "cause_id": "",
  "role_config": { "can": [...], "model": "claude-sonnet-4-6", "container": {"image": "..."} }
}
```

把 `(agent_sha, repo_sha)` 一对 checkout 出来就能精确复现 agent 当时看到的 prompt + 工具集 + 代码状态 —— 这是 M7a P2 audit chain 的核心承诺。

## 8. 关 issue → 归档

```bash
curl -X PATCH -H "Cookie: hangrix_session=..." \
     -H "Content-Type: application/json" \
     -d '{"state":"closed"}' \
     http://localhost:8080/api/repos/<owner>/<host>/issues/<n>
```

期望立即发生：

1. `agent_sessions.status` 上所有该 issue 的非 archived 行 → `archived`，`ended_at` 写当前时间，`session_token_sealed = NULL`。
2. 任何还在跑的容器：runner 当前不主动 kill 它（M7a 范围内本来 happy path 是 agent 已经 `done` 了；late-arrival 的 terminate call 因为 SQL `WHERE status NOT IN ('succeeded','failed','cancelled','archived')` 不会回写 status，archived 保持权威）。

## 9. 失败排查

| 现象 | 排查点 |
|---|---|
| 开 issue 后没新 session row | 检查 host yaml 解析：`apps/hangrix/internal/modules/agent_session/service/spawner.go::loadHostConfig` 拒 unknown fields，看 server log 有没有 ErrHostConfigInvalid。 |
| Session row 起来但 agent_sha 空 | Agent 仓库的 ref 解不出 —— 检查 `.hangrix/agents.lock` 或 agent 仓库的 tag/branch 是否真存在。 |
| Runner 不取这行 | 看 runner 是否 `kind=platform` + `status=active`，看 runner agent token 没过期。`agent_sessions.runner_id IS NULL` 是预期（unpinned）。 |
| Container 起不来 / 拉 image 失败 | Runner 这台机器跑 `docker pull <image>` 看错。M7a P2 只支持 `container.image`，host yaml 写了 `container.build:` 会被 spawner reject。 |
| Push 时 author 不是 role key | 检查 session env 里 `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` 是否正确注入。生产路径在 `apps/hangrix/internal/modules/agent_session/service/spawner.go::spawnRole`。 |
| Audit 接口 403 | `/api/admin/*` 需要 admin cookie；用 `Role=admin` 的用户登录。 |
