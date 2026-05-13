# AGENTS.md

Operational reference for AI agents working on this repo. Terse on purpose — read this first, then the code.

## Stack

- Go 1.26.1 service in `apps/hangrix/` that embeds a Nuxt 4 SPA via `//go:embed`.
- Nuxt 4 + Tailwind v4 + shadcn-vue in `apps/web/`.
- pnpm + Turborepo monorepo; Go modules also tied together via `go.work`.

## Workspaces

| Path | Module/Pkg name | Notes |
| --- | --- | --- |
| `apps/hangrix` | `hangrix` / `github.com/hangrix/hangrix/apps/hangrix` | Main Go service. Binary: `bin/hangrix.exe`. |
| `apps/web` | `web` | Nuxt 4 frontend. SPA is generated, then embedded into Go binary. |
| `pkg/common` | `@hangrix/common-go` / `github.com/hangrix/hangrix/pkg/common` | Shared Go lib. |
| `pkg/ioc` | `@hangrix/ioc` / `github.com/hangrix/hangrix/pkg/ioc` | Reflection-based DI container used by `apps/hangrix`. |

Each Go module has a thin `package.json` exposing `build` / `test` / `lint` / `dev` scripts so Turbo can orchestrate it alongside the JS workspaces.

## Common commands

```sh
pnpm install                        # installs JS deps + runs nuxt prepare
pnpm dev                            # turbo run dev (Nuxt dev + go run hangrix in parallel)
pnpm build                          # turbo run build — full pipeline
pnpm test                           # turbo run test
pnpm typecheck                      # turbo run typecheck
pnpm --filter hangrix build         # web#generate → copy-web-dist → go build
pnpm --filter hangrix dev           # air hot-reload (config: apps/hangrix/.air.toml). Watches cmd/, conf/, internal/ + ../../pkg.

# When both web and hangrix dev servers run, hit http://localhost:3000 for everything —
# Nuxt's routeRules.proxy in nuxt.config.ts forwards /api/** and /healthz to :8080.
cd apps/hangrix && go test ./...
```

When you change a Go file outside `cmd/`, `go build ./...` from `apps/hangrix` is the fastest sanity check.

## ioc container conventions

`pkg/ioc` enforces strict constructor shapes — violations panic at registration. Match these rules exactly.

- Constructor: a function returning **exactly one** `*Struct`.
- Param: 0 or 1 args; the arg (if present) must be `*Deps` where every field is an interface, a pointer-to-struct, or a slice of interfaces (`[]I`).
- Bind with one of:
  - `.ToSelf()` — resolvable as the concrete pointer type.
  - `.ToInterface(new(I))` — resolvable as interface `I`. Multiple impls under the same interface are collected into `[]I` automatically.
- Resolve with `ioc.Get[T](c)`; `T` is the pointer type, interface, or `[]InterfaceType`.

Per feature, the package convention is a modular-monolith layout under `internal/modules/<name>/`:

```
internal/modules/<name>/
├── handler/handler.go    package handler — Handler struct + RegisterRoutes(chi.Router)
├── domain/               package domain  — interfaces + value types (added when needed)
├── repo/                 package repo    — repository interfaces and impls (added when needed)
├── infra/                package infra   — concrete dependencies, e.g. DB / HTTP clients (added when needed)
└── module.go             package <name>  — Module() wires every layer this feature uses
```

Only `module.go` is allowed to import the sub-layers. Cross-module wiring goes through the ioc container by depending on the other module's `domain` interfaces — never import another module's `handler`, `repo`, or `infra` directly.

`main.go` is intentionally trivial — it self-registers the container so `App` can later inject runtime services back into it:

```go
c := ioc.NewContainer()
c.Provide(func() *ioc.Container { return c }).ToSelf()
c.Load(app.Module(), server.Module(), /* feature modules */, web.Module())
ioc.Get[*app.App](c).Run(os.Args[1:])
```

`App` takes `*ioc.Container` as a dep. In `App.Run` it parses flags, loads config, calls `container.Provide(func() *config.Config { return cfg }).ToSelf()`, then `ioc.Get[*server.Server](container)`. `Server`'s constructor consumes `*config.Config` + `[]RouteProvider` like any other ioc-resolved type.

Do not add helper functions to `cmd/hangrix/main.go`. Lifecycle belongs in `App.Run`. Wiring belongs in the relevant `Module()`.

## Adding a feature module

1. `apps/hangrix/internal/modules/<name>/handler/handler.go` — `package handler`, `type Handler struct{}`, `NewHandler() *Handler`, `(h *Handler) RegisterRoutes(r chi.Router)`. chi gives static paths precedence over the SPA's `/*` so namespace freely.
2. `apps/hangrix/internal/modules/<name>/module.go` — `package <name>`, `func Module() *ioc.Module` that does `m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))`. Add further layers (`domain`, `repo`, `infra`) here when the feature grows beyond HTTP.
3. Append `<name>.Module()` to the `c.Load(...)` call in `cmd/hangrix/main.go`.

`Server` discovers the handler automatically through the `[]RouteProvider` slice dependency.

## Adding config

- Add a typed field on `config.Config` / sub-struct with a `mapstructure:"..."` tag.
- Set a value in `apps/hangrix/conf/config.yaml`.
- Env override naming: viper prefix is `API` with `.` → `_`. So `server.addr` is `API_SERVER_ADDR`, `database.dsn` would be `API_DATABASE_DSN`.
- Depend on `*config.Config` from any constructor — `App.Run` registers it into the container right after loading. Anything resolved via the container after that step receives it automatically.

Lookup order: env var > YAML file > `SetDefault`. The defaults live in `config.go`; keep YAML in sync.

## Frontend embedding

- The Go binary serves `apps/hangrix/internal/web/dist/` via `//go:embed all:dist` from `internal/web/web.go`.
- `dist/` is gitignored except for `.gitkeep` — content is regenerated on every build.
- The build chain: `web#generate` (Nuxt static SSG) → `node scripts/copy-web-dist.mjs` → `go build`. Turbo enforces this via `apps/hangrix/turbo.json`'s `dependsOn: ["web#generate"]`.
- If `index.html` is absent at runtime, `SPAHandler` serves a built-in placeholder page so `go run ./cmd/hangrix` still works before the bundle is built.
- SPA fallback: any path that doesn't map to an embedded file is served `index.html` so Vue Router can handle it.

Do not commit anything inside `internal/web/dist/` other than `.gitkeep`.

## Turbo gotchas

- Tasks run with a sanitized env. Go-relevant variables (`LOCALAPPDATA`, `APPDATA`, `GOCACHE`, etc.) are whitelisted via `globalPassThroughEnv` in the root `turbo.json`. If you add a Go tool that needs more env, extend that list.
- `pkg/common` and `pkg/ioc` override `build.outputs` to `[]` in their local `turbo.json` because they produce no artifact.
- Build scripts (`esbuild`, `@parcel/watcher`) must run their postinstall to fetch native binaries — pinned under `pnpm.onlyBuiltDependencies` in the root `package.json`.

## Do not

- Add `//go:build` tags to swap embed paths conditionally — the placeholder branch in `web.go` already handles the "not yet built" case.
- Use the global `flag` package; everything new should use `flag.NewFlagSet` so command-line state doesn't leak across invocations.
- Load config eagerly in `main.go`. `App.Run` is the single place that parses flags, calls `config.Load`, and provides `*config.Config` to the container. Resolving `*server.Server` before that step would panic with "no provider found for *config.Config".
- Run `pnpm dlx shadcn-vue@latest init` — the init was already done by hand (components.json, app/lib/utils.ts, full v4 CSS in app/assets/css/tailwind.css). Re-running init would overwrite that config. To add new components use `pnpm --filter web dlx shadcn-vue@latest add <name> --yes`.
- Push to `main` or force-push without explicit user instruction.
