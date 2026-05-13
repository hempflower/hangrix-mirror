# Hangrix

Monorepo containing the Go service and the Nuxt frontend for Hangrix.
The Go binary embeds the prebuilt frontend so a single `hangrix.exe` serves
the whole app.

## Layout

```
.
├── apps/
│   ├── hangrix/    Go HTTP service (embeds the Nuxt static bundle)
│   │   ├── cmd/hangrix/   entry point — only container init + Get[*App]
│   │   ├── conf/          YAML config (server.addr, ...)
│   │   ├── internal/
│   │   │   ├── app/         App: flag parsing, config loading, server start
│   │   │   ├── config/      viper-based config loader
│   │   │   ├── modules/     feature modules (modular monolith)
│   │   │   │   ├── hello/       handler / domain / repo / infra (as needed)
│   │   │   │   └── healthz/
│   │   │   ├── server/      chi router + RouteProvider interface
│   │   │   └── web/         //go:embed of apps/web's static output
│   │   └── scripts/copy-web-dist.mjs   stages apps/web/.output/public into internal/web/dist
│   └── web/        Nuxt 4 frontend (shadcn-vue + Tailwind v4)
├── pkg/
│   ├── common/     Shared Go package
│   └── ioc/        IoC container used by apps/hangrix
├── go.work         Go multi-module workspace
├── turbo.json      Turborepo task graph (JS + Go)
└── pnpm-workspace.yaml
```

## Prerequisites

- Go >= 1.26
- Node.js >= 20
- pnpm >= 9

## Getting started

```sh
# install JS dependencies (all workspaces)
pnpm install

# sync go workspace
go work sync

# all tasks via Turborepo (JS + Go)
pnpm dev               # turbo run dev
pnpm build             # turbo run build       (web#generate → embed → go build)
pnpm test              # turbo run test
pnpm typecheck         # turbo run typecheck

# run only the Nuxt dev server (proxies /api/** and /healthz to :8080 — see nuxt.config.ts routeRules)
pnpm --filter web dev

# run only the Go service with hot reload (air, configured at apps/hangrix/.air.toml)
pnpm --filter hangrix dev

# or one-shot without hot reload
cd apps/hangrix && go run ./cmd/hangrix

# produce the embedded single-binary build
pnpm --filter hangrix build
# → apps/hangrix/bin/hangrix.exe   (serves SPA + /api/* + /healthz)
```

## How embedding works

1. `apps/web` is built statically with `nuxi generate`, producing `apps/web/.output/public/`.
2. `apps/hangrix/scripts/copy-web-dist.mjs` stages that into `apps/hangrix/internal/web/dist/`.
3. `internal/web/web.go` has `//go:embed all:dist`, so `go build` bakes the files into the binary.
4. At runtime `internal/web` is registered as a `server.RouteProvider` mounted at `/*`. chi's radix-tree matching gives specific routes (`/api/hello`, `/healthz`) precedence; everything else falls through to the SPA handler, which serves `index.html` for paths that don't map to a file.

Turborepo wires the dependency: `hangrix#build` declares `dependsOn: ["web#generate"]`, so the static bundle is always fresh before the Go build runs.

`internal/web/dist/` contents (apart from `.gitkeep`) are gitignored — the directory is regenerated on every build.

## Architecture: ioc + RouteProvider

The Go service uses `pkg/ioc` for dependency injection. Each domain exposes a
`Module() *ioc.Module` that registers its constructors and bindings. `main.go`
only creates a container, self-registers the container, loads all modules,
and fetches `*app.App`:

```go
c := ioc.NewContainer()
c.Provide(func() *ioc.Container { return c }).ToSelf()
c.Load(app.Module(), server.Module(), healthz.Module(), hello.Module(), web.Module())
ioc.Get[*app.App](c).Run(os.Args[1:])
```

`App` depends on `*ioc.Container`. Inside `App.Run` it parses flags, loads
config, registers `*config.Config` into the container via `Provide(...).ToSelf()`,
then resolves `*server.Server` from the container — `Server` takes
`*config.Config` and `[]RouteProvider` via standard DI.

Constructor rules enforced by ioc:

- Constructor must return a single value: a pointer to a struct.
- Constructor takes 0 or 1 parameters; the parameter, if present, is a pointer to a struct whose fields are interfaces, pointers-to-struct, or slices of interfaces.
- Bind with `ToSelf()` for concrete-type resolution, or `ToInterface(new(I))` to register an interface implementation. Slice fields (`[]I`) collect every binding for `I`.

### Adding a feature module

Each domain lives under `internal/modules/<name>/` and is composed of layered subpackages (handler / domain / repo / infra). The module root exposes a single `Module() *ioc.Module` that wires every layer the feature uses.

Minimum (handler only) skeleton:

```
internal/modules/<name>/
├── handler/handler.go    # package handler — Handler struct + RegisterRoutes
└── module.go             # package <name>  — Module() wires handler (and later domain/repo/infra)
```

1. `handler/handler.go`: `type Handler struct{}`, `NewHandler() *Handler`, `(h *Handler) RegisterRoutes(r chi.Router)`. Routes the handler claims (e.g. `/api/<name>`) are static, so chi's radix tree gives them precedence over the SPA catch-all.
2. `module.go`: `package <name>`, exporting `Module() *ioc.Module` that does
   `m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))`.
   Add further layers here as the feature grows.
3. Register the module in `cmd/hangrix/main.go`'s `c.Load(...)` list.

`Server` discovers the handler automatically through the `[]RouteProvider` slice dependency.

Suggested layout once a module needs more than HTTP:

```
internal/modules/<name>/
├── domain/    interfaces + plain types (no framework imports)
├── infra/     concrete implementations (DB clients, HTTP clients, ...)
├── repo/      repository interfaces + impls (or fold into domain/infra)
├── handler/   chi-level HTTP entry points
└── module.go  the only place that knows about every layer
```

Cross-module dependencies should go through the ioc container by depending on interfaces from the other module's `domain/`, not by importing handlers/infra directly.

## Config

`internal/config/config.go` uses viper:

- File: YAML at the path given by `-config` (default `conf/config.yaml`).
- Env override: `API_<UPPER_SNAKE>` overrides any key. Example: `API_SERVER_ADDR=:9090` overrides `server.addr`.
- Defaults: `server.addr` falls back to `:8080` when unset everywhere.

Adding a new config field: add it to the `Config` struct (with `mapstructure` tags), put a value in `conf/config.yaml`, and depend on `*config.Config` from any constructor — it's registered into the ioc container by `App.Run` after flag parsing.

## UI library (shadcn-vue)

The web app is fully wired for [shadcn-vue](https://www.shadcn-vue.com/) with Tailwind v4 (no further `init` needed):

- `apps/web/components.json` — style: `new-york`, base color: `neutral`, CSS variables enabled
- `apps/web/app/lib/utils.ts` — the `cn` helper
- `apps/web/app/assets/css/tailwind.css` — `@import "tailwindcss"`, `@import "tw-animate-css"`, full light/dark CSS variables, `@theme inline` mapping, `@layer base`
- Runtime deps in `apps/web/package.json`: `clsx`, `tailwind-merge`, `class-variance-authority`, `lucide-vue-next`, `reka-ui`, dev `tw-animate-css`
- `shadcn-nuxt` Nuxt module is registered in `nuxt.config.ts` so components auto-import

Add components with:

```sh
pnpm --filter web dlx shadcn-vue@latest add button --yes
pnpm --filter web dlx shadcn-vue@latest add card --yes
```

Generated files land in `apps/web/app/components/ui/<name>/` and are auto-imported by Nuxt.

## Turborepo notes

- Root `turbo.json` defines `build`, `dev`, `lint`, `typecheck`, `test`, `generate`.
- `globalPassThroughEnv` exposes Go-related env (`LOCALAPPDATA`, `GOCACHE`, ...) so Go can find its cache on Windows.
- `apps/hangrix/turbo.json` extends the root and adds `build.dependsOn: ["web#generate"]`.
- `pkg/common/turbo.json` and `pkg/ioc/turbo.json` declare `outputs: []` for the library Go packages.
- Approved build scripts (`esbuild`, `@parcel/watcher`) are pinned in the root `package.json` under `pnpm.onlyBuiltDependencies`.
