# Embedded frontend — what is and is not in git

The production `hangrix.exe` is one binary that serves the SPA, `/api/**`, and `/healthz`. The Nuxt bundle is `//go:embed`ed into the Go binary via [apps/hangrix/internal/web/web.go](apps/hangrix/internal/web/web.go) (`//go:embed all:dist`).

## Build chain

1. `pnpm --filter web build` → `nuxi generate` → `apps/web/.output/public/`.
2. `node apps/hangrix/scripts/copy-web-dist.mjs` → stages it into `apps/hangrix/internal/web/dist/`.
3. `cd apps/hangrix && go build` → the dist tree is baked into the binary.

Turbo enforces the order: `apps/hangrix/turbo.json` declares `build.dependsOn: ["web#generate"]`, so `pnpm --filter hangrix build` runs the full chain.

## What is in git

- `apps/hangrix/internal/web/dist/.gitkeep` — the only file. Everything else in that directory is gitignored. **Committing the generated bundle would create a noisy diff on every build and a merge-conflict factory.**
- `apps/web/.output/`, `apps/web/.nuxt/`, `apps/web/dist/` — all gitignored.

If you find dist contents in a diff, that is a bug — the worker forgot the gitignore. Reviewer should `request_changes`.

## Dev mode

Two servers run concurrently:

- `pnpm --filter web dev` → Nuxt on `:3000`, with `routeRules.proxy` forwarding `/api/**` and `/healthz` to `:8080`.
- `pnpm --filter hangrix dev` → air-driven Go on `:8080`, watching `cmd/`, `conf/`, `internal/`, and `../../pkg`.

Hit `http://localhost:3000` for everything — the proxy hands `/api/**` to the Go service transparently.

When `index.html` is absent (fresh checkout, before `nuxi generate`), `SPAHandler` serves a built-in placeholder so `go run ./cmd/hangrix` still works. Do NOT add `//go:build` tags to swap embed paths — the placeholder branch handles the "not yet built" case already.
