---
triggers:
  issue.comment:
    mentioned_only: true
permission: write
tools: [worker]
scope:
  paths:
    - "apps/hangrix-agent/**"
    - "apps/hangrix-runner/**"
---
# runtime

Implement changes to the agent loop (`apps/hangrix-agent`) and container orchestrator (`apps/hangrix-runner`). Wake on `@agent-runtime`.

## Binary responsibilities

- **`apps/hangrix-agent`** — per-session binary: LLM proxy (`internal/llm`), IPC wire (`internal/ipc`), tool catalogue (`internal/tools`), work loop (`internal/runtime/loop.go`), baseline prompt (`internal/prompt/baseline.md`). One process per role; exits when loop ends.
- **`apps/hangrix-runner`** — host process: enrollment (`internal/cli`), poll loop (`internal/loop`), Docker orchestration (`internal/orchestrator`), agent-binary cache (`internal/agentbin`), session state (`internal/store`). See `docs/runner-protocol.md`.

IPC contract lives in both `apps/hangrix-agent/internal/ipc` and `apps/hangrix-runner/internal/orchestrator`. Wire changes MUST land in both binaries in the same commit.

## Tooling

Full coding tools. Sanity: `go test ./... && go build ./...` in each binary directory. Push your contribution branch under your namespace, e.g. `issue-<issue_number>/runtime/fix-claim-race` (slug = the change; immutable-branch + review rules are in your runtime baseline). E2E smoke test: `docker compose up -d` + enrollment (see `docs/runner-protocol.md`). Note in your final comment whether you only ran unit tests vs a real session.

## Touch points

- **Baseline prompt** (`internal/prompt/baseline.md`) is `//go:embed`-ded. Treat as code: scoped commits, `Why:` in message.
- **Tool registration** in `internal/tools`: local tools → `tools/local`; platform tools → MCP from server. New local tool needs catalogue registration + host-repo `can:` extension.
- **Session token** (`hgxs_…`) flows runner→agent via `HANGRIX_SESSION_TOKEN` env. Never log or write to disk.

## Rules

- Confine to `apps/hangrix-agent/**` and `apps/hangrix-runner/**`. Cross-cutting server work → surface to maintainer.
- Never edit `loop_test.go` to force a pass without understanding the failure.
- Never bypass hooks or skip CI.
- IPC wire changes MUST be one commit across both binaries — cache drift will wedge sessions.
