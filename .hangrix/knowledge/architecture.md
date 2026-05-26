# Architecture orientation

Three Go binaries plus one frontend, all in one monorepo:

- **`apps/hangrix/`** — the long-running control-plane HTTP service. Modular monolith built on `pkg/ioc` (reflection DI). Modules live at `internal/modules/<name>/{domain,service,infra,handler}` with one `module.go` per feature. Routes are discovered through the `[]server.RouteProvider` ioc slice; chi's radix tree gives static `/api/...` paths precedence over the SPA's `/*` catch-all.
- **`apps/hangrix-agent/`** — the per-session binary that runs *inside* each role's container. Owns the LLM proxy adapter, MCP/IPC wire to the runner, tool catalogue (`internal/tools`), and the work loop (`internal/runtime/loop.go`). One process per role per issue; exits when the loop ends.
- **`apps/hangrix-runner/`** — the host-side container orchestrator. Enrols itself with the server, polls for sessions, launches Docker containers running `hangrix-agent`, streams output back. The runner protocol lives at `docs/runner-protocol.md`.
- **`apps/web/`** — Nuxt 4 SPA, statically generated and then `//go:embed`ed into the `hangrix` binary at build time. Dev server proxies `/api/**` + `/healthz` to `:8080`.

Shared libs under `pkg/{common,cryptobox,ioc}/`. `go.work` ties the four Go modules together so cross-module imports work without `replace` directives.

The platform that this repo defines (agent config schema, runner protocol, llm proxy, agent identity) is documented under `docs/` — `agent-config.md`, `runner-protocol.md`, `llm-proxy.md`, `agent-identity.md`, `tech-stack.md`.

## Runtime internals (apps/hangrix-agent, apps/hangrix-runner)

Package map for work on the agent loop and orchestrator:

- **`apps/hangrix-agent`** (per-session binary, one process per role, exits when the loop ends): LLM proxy `internal/llm`, IPC wire `internal/ipc`, tool catalogue `internal/tools`, work loop `internal/runtime/loop.go`, embedded baseline prompt `internal/prompt/baseline.md`.
- **`apps/hangrix-runner`** (host process): enrollment `internal/cli`, poll loop `internal/loop`, Docker orchestration `internal/orchestrator`, agent-binary cache `internal/agentbin`, session state `internal/store`.

- **IPC contract** lives in both `apps/hangrix-agent/internal/ipc` and `apps/hangrix-runner/internal/orchestrator`. Because the runner caches the agent binary, a wire change shipped to only one side wedges sessions — wire changes must land in both binaries in the same commit.
- **Baseline prompt** (`internal/prompt/baseline.md`) is `//go:embed`-ded into the agent binary; it is the OS layer every host repo's agents inherit, on top of which each role's `.hangrix/agents/<role>.md` body is appended.
- **Tool registration** is in `internal/tools`: local tools under `tools/local`; platform tools arrive over MCP from the server.
- **Session token** (`hgxs_…`, see AGENTS.md "Token wire formats") flows runner → agent via the `HANGRIX_SESSION_TOKEN` env var and authenticates the agent's LLM / MCP / git calls.

Build/test commands for these binaries are in [.hangrix/knowledge/local-stack.md](.hangrix/knowledge/local-stack.md); the full enrollment + container E2E is in [docs/runner-protocol.md](docs/runner-protocol.md).

## Container image lifecycle

A host repo declares either `container.image:` (pull-only) or `container.build:` (Dockerfile in the repo) — the spawner computes a deterministic docker tag (auto-derived from repo id + dockerfile path + build args when build is used) and ships it to the runner. The runner's `ensureImage` in [apps/hangrix-runner/internal/orchestrator/docker.go](apps/hangrix-runner/internal/orchestrator/docker.go) probes `docker image inspect <tag>` first and only invokes `docker build` on miss, so reuses are free. BuildKit is forced on via `DOCKER_BUILDKIT=1`.
