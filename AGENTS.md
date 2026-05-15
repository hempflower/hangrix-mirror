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

**Prefer ioc binding over manual construction.** Anywhere you'd be tempted to `infra.NewPostgresRepo(...)` from a handler or a sibling module, define the dep on a `*Deps` field instead and let the container hand it over. Manual construction inside a function body is almost always a sign that the wiring belongs in `Module()`.

**Bind one concrete to many interfaces** when the same instance satisfies several roles (e.g. `*service.Service` implements both `domain.Store` and `domain.Validator`). Chain `.ToInterface(...)` calls on the same `ProviderBinder`:

```go
svc := m.Provide(service.New)
svc.ToInterface(new(domain.Store))
svc.ToInterface(new(domain.Validator))
```

This guarantees every consumer gets the *same* singleton — critical when the singleton holds state (caches, secrets, repo handles).

Per feature, the package convention is a modular-monolith layout under `internal/modules/<name>/`:

```
internal/modules/<name>/
├── handler/handler.go    package handler — Handler struct + RegisterRoutes(chi.Router)
├── domain/               package domain  — interfaces + value types + pure validation
├── service/              package service — business logic: regex, bcrypt, minting, orchestration
├── infra/                package infra   — persistence ONLY (sqlc queries + row→domain mapping + transaction primitives)
│   ├── queries.sql       sqlc input
│   ├── <name>db/         sqlc-generated package (Queries, models). DO NOT hand-edit.
│   └── migrations/       goose migrations
└── module.go             package <name>  — Module() wires every layer this feature uses
```

Only `module.go` is allowed to import the sub-layers. Cross-module wiring goes through the ioc container by depending on the other module's `domain` interfaces — never import another module's `handler`, `service`, or `infra` directly.

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
2. `apps/hangrix/internal/modules/<name>/module.go` — `package <name>`, `func Module() *ioc.Module` that does `m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))`. Add further layers (`domain`, `service`, `infra`) here when the feature grows beyond HTTP.
3. Append `<name>.Module()` to the `c.Load(...)` call in `cmd/hangrix/main.go`.

`Server` discovers the handler automatically through the `[]RouteProvider` slice dependency.

## Layering rules (do not cross)

Every module's code lives in one of four layers. Each layer has a job; mixing them is the most common review correction.

| Layer | Does | Does NOT |
| --- | --- | --- |
| **domain** | Type / interface / error declarations. Pure-data validation methods (`Input.Validate()`). Constants for wire formats. | I/O, crypto, regex matching on persisted state, SQL, framework imports. |
| **service** | Regex, bcrypt, token minting, retry policy, multi-step orchestration, callback-based transactional flows. Implements the `domain.*` interfaces handlers consume. | Direct pgx calls, raw SQL, knowledge of pgtype. |
| **infra** | sqlc-generated queries + thin row→domain mapping + transaction wrappers + cryptobox seal/open as opaque field storage + pgx error mapping (ErrNoRows, unique-violation). | bcrypt, regex on wire formats, secret minting, input validation, business invariants, "ensure name length ≤ N" style checks. |
| **handler** | HTTP plumbing. DTO ↔ domain. Authn middleware wiring. Maps domain errors to status codes. | Business logic. Persistence concerns. Token minting (call service). |

When you find crypto or regex in `infra/`, that's a bug — extract to `service/`. When you find I/O in `domain/`, that's a bug — extract to `service/`. Existing examples to copy:

- `modules/runner/service/` — `Enroller` (callback-pattern bcrypt under Repo's transaction), `AgentTokenValidator` / `SessionTokenValidator` (stateless read-path), `MintAgentToken` / `MintSessionToken` / `MintEnrollToken` (wire-format minting).
- `modules/token/service/service.go` — `Service` implements both `domain.Store` and `domain.Validator` on one type; persistence (`PostgresRepo`) only knows `Insert(InsertParams)` etc.
- `modules/git/domain/refname.go` — pure-data validation (`IsValidRefName`) callable from infra without coupling infra to policy.

**Callback pattern for transactional writes that need crypto:** When bcrypt has to run between `SELECT FOR UPDATE` and the matching `UPDATE` (e.g. enrollment redemption), the service supplies a `verify func(stored *Row) error` closure to the repo. The repo owns the transaction; the closure runs inside it. Don't move the transaction up into service — pgx transaction handles don't survive interface boundaries cleanly.

## Database access (sqlc + goose)

Every module that touches Postgres uses **sqlc-generated** queries — no hand-written `pool.QueryRow(...)` calls in new code. The generated `<name>db/` package is the only thing infra is allowed to call into directly.

- **Queries:** write `internal/modules/<name>/infra/queries.sql`. Use `sqlc.arg('name')` / `sqlc.narg('name')` to name parameters explicitly (default `$1` positional generates ugly `Column8`-style fields). Cast with `::TYPE` when the inferred type is wrong (e.g. `sqlc.arg('model')::TEXT = ANY(allowed_models)`).
- **Migrations:** `internal/modules/<name>/infra/migrations/<NNNNN>_<slug>.sql` with `-- +goose Up` / `-- +goose Down`. Forward-applies must be idempotent; never edit a shipped migration — add a new one.
- **Register the module** in `apps/hangrix/sqlc.yaml` with the migration set it depends on (union user/org/repo migrations as needed so sqlc can resolve FKs at parse time; runtime still applies each module's own migrations via its own `goose_<name>` version table).
- **Regenerate:** `cd apps/hangrix && sqlc generate`. The binary lives at `/go/bin/sqlc`; install with `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest` if missing.
- **Do not** commit a `_test.go` next to generated `<name>db/queries.sql.go`.
- **Cross-module FKs** are allowed within Postgres but referenced schemas must be unioned into the sqlc config for that module. Cross-module hard FKs in domain (e.g. `llm_usage_log.session_id` → `agent_sessions.id`) are usually a smell — store the ID as plain `BIGINT` and let the consumer module own the lookup.

## Token wire formats

Every bearer token in the system uses one of four prefixes. A single auth router can dispatch by inspecting the prefix alone — don't try every validator in turn.

| Prefix | Issued to | Validator | Lives in |
| --- | --- | --- | --- |
| `hgx_` | users (PATs) | `token/domain.Validator` | `token/service` |
| `hgxe_` | runners (enrollment, one-shot) | `runner/domain.EnrollValidator` | `runner/service` |
| `hgxr_` | runners (long-lived agent token) | `runner/domain.AgentValidator` | `runner/service` |
| `hgxs_` | agent sessions (identity for LLM / MCP / git) | `runner/domain.SessionTokenValidator` | `runner/service` |

All four follow the same `<prefix>_<8 chars>_<32 chars>` shape with alphabet `[A-Za-z0-9]`. The 8-char public prefix is the lookup key; the 32-char secret is bcrypt-hashed. **Plaintext exists exactly once at creation** and is never reconstructable. Reuse the service `Mint*Token()` helpers — don't roll a new wire format.

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
- Hand-write SQL in `infra/` when sqlc would do. If the query is too dynamic for sqlc (rare — `sqlc.narg` + `IS NULL` predicates cover most filter cases), document why in a comment above the query.
- Put bcrypt, regex, or token-format strings in `infra/`. Even a one-liner regex on a wire format is a service concern (see [Layering rules](#layering-rules-do-not-cross)).
- Construct another module's `infra.New*Repo()` directly. Take the `domain.*` interface on your `*Deps` instead and let the container resolve it.
- Edit shipped goose migrations in place. Add a new one — the version table is unforgiving.
- Edit anything inside `*db/` (sqlc-generated). Update `queries.sql` and re-run `sqlc generate`.
