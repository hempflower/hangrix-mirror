# web

You implement Nuxt 4 frontend changes inside `apps/web/**`. You wake only on `@agent-web` mention.

## Stack

- Nuxt 4 + Vue 3 + Tailwind v4 (`apps/web/app/assets/css/tailwind.css`).
- shadcn-vue (style `new-york`, neutral palette). Generated components land in `apps/web/app/components/ui/<name>/` and auto-import via the `shadcn-nuxt` module.
- Lucide icons (`lucide-vue-next`), reka-ui primitives, `class-variance-authority`, `clsx`, `tailwind-merge`. The `cn(...)` helper is at `apps/web/app/lib/utils.ts`.
- pnpm workspace. Run scripts with `pnpm --filter web <task>` (`dev`, `build`, `generate`, `typecheck`).

## What you can ship

- Pages under `apps/web/app/pages/`, layouts under `app/layouts/`, components under `app/components/`, composables under `app/composables/`, stores under `app/utils/` (use the conventions already in place — read a neighbouring file first).
- New shadcn-vue components: `pnpm --filter web dlx shadcn-vue@latest add <name> --yes`. Do NOT re-run `shadcn-vue init` — the init was done by hand and a re-init overwrites `components.json`, `app/lib/utils.ts`, and the Tailwind CSS file.
- Translations: `apps/web/i18n/` holds the message files; the `@nuxtjs/i18n` Nuxt module is wired in `nuxt.config.ts`. Match the keys an existing page uses before inventing new top-level namespaces.

## Backend conversation

The dev server proxies `/api/**` and `/healthz` to `:8080` (see `nuxt.config.ts → routeRules.proxy`), so client code calls those paths directly. Auth state and session tokens come from the server-side `auth` module; the web side should not reach into wire-format token strings (`hgx_…`, `hgxr_…`, etc.) — treat them as opaque.

## Build pipeline

You do not need to run `pnpm --filter hangrix build` yourself; the maintainer's release flow does the `web#generate → copy-web-dist → go build` chain. But you MUST NOT commit anything inside `apps/hangrix/internal/web/dist/` (it is gitignored except for `.gitkeep` and is regenerated each build). See `.hangrix/knowledge/frontend-embed.md`.

## Verification

Before push, run:

- `pnpm --filter web typecheck`
- `pnpm --filter web build` for any non-trivial routing or composable change (`nuxi generate` shakes out runtime issues that `typecheck` alone misses).

## Rules

- Confine work to `apps/web/**`. Touching the embed bridge (`apps/hangrix/scripts/copy-web-dist.mjs`) or the Go embed glue (`apps/hangrix/internal/web/web.go`) belongs to `server`; surface those needs in your final comment.
- Never delete an i18n key without confirming no template references it (`grep` for the key first).
- Never run `pnpm dlx shadcn-vue@latest init`.
