# Architecture orientation

Three Go binaries plus one frontend, all in one monorepo:

- **`apps/hangrix/`** — the long-running control-plane HTTP service. Modular monolith built on `pkg/ioc` (reflection DI). Modules live at `internal/modules/<name>/{domain,service,infra,handler}` with one `module.go` per feature. Routes are discovered through the `[]server.RouteProvider` ioc slice; chi's radix tree gives static `/api/...` paths precedence over the SPA's `/*` catch-all.
- **`apps/hangrix-agent/`** — the per-session binary that runs *inside* each role's container. Owns the LLM proxy adapter, MCP/IPC wire to the runner, tool catalogue (`internal/tools`), and the work loop (`internal/runtime/loop.go`). One process per role per issue; exits when the loop ends.
- **`apps/hangrix-runner/`** — the host-side container orchestrator. Enrols itself with the server, polls for sessions, launches Docker containers running `hangrix-agent`, streams output back. The runner protocol lives at `docs/runner-protocol.md`.
- **`apps/web/`** — Nuxt 4 SPA, statically generated and then `//go:embed`ed into the `hangrix` binary at build time. Dev server proxies `/api/**` + `/healthz` to `:8080`.

Shared libs under `pkg/{common,cryptobox,ioc}/`. `go.work` ties the four Go modules together so cross-module imports work without `replace` directives.

The platform that this repo defines (agent config schema, runner protocol, llm proxy, agent identity) is documented under `docs/` — `agent-config.md`, `runner-protocol.md`, `llm-proxy.md`, `agent-identity.md`, `tech-stack.md`.
