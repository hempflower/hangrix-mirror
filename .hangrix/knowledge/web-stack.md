# Web stack (apps/web)

The frontend is a Nuxt 4 SPA. This file is the stack reference for any work under `apps/web/**`. The build → embed pipeline and the dev proxy live in [.hangrix/knowledge/frontend-embed.md](.hangrix/knowledge/frontend-embed.md); `AGENTS.md` carries the repo-wide summary and the shadcn init/add rule.

## Frameworks & libraries

- Nuxt 4 + Vue 3 + Tailwind v4.
- shadcn-vue (`new-york` style, neutral). Generated components land in `app/components/ui/<name>/`.
- Lucide icons, reka-ui, `class-variance-authority`, `clsx`, `tailwind-merge`. The `cn(...)` class-merge helper is at `app/lib/utils.ts`.
- pnpm workspace. Run package scripts as `pnpm --filter web <task>`.

## File layout & conventions

- Pages → `app/pages/`, layouts → `app/layouts/`, components → `app/components/`, composables → `app/composables/`. Read a neighbour first for conventions.
- Add shadcn-vue components with `pnpm --filter web dlx shadcn-vue@latest add <name> --yes`. Never re-run `pnpm dlx shadcn-vue@latest init` — it clobbers `components.json`, `app/lib/utils.ts`, and the Tailwind CSS (also in AGENTS.md "Do not").
- Translations live in `apps/web/i18n/`; `@nuxtjs/i18n` is wired in `nuxt.config.ts`. Match existing key patterns, and `grep`-check template references before deleting a key.

## Backend calls

The dev server proxies `/api/**` and `/healthz` to `:8080` (`routeRules.proxy` in `nuxt.config.ts` — see [.hangrix/knowledge/frontend-embed.md](.hangrix/knowledge/frontend-embed.md)). Call API paths relatively (`/api/...`); an absolute `http://localhost:8080/...` works in dev but breaks in the embedded prod build. Bearer tokens (`hgx_*`) are opaque — never parse them in the frontend.

## Verification commands

- `pnpm --filter web typecheck` — always, before submitting.
- `pnpm --filter web build` — for routing/composable changes.
- UI changes (pages, components, layouts): start the dev server in the background (`pnpm --filter web dev`) and drive it with the Playwright `browser_*` tools to confirm the rendered output. A passing typecheck without visual verification is **not** enough for UI work.
- Dev-server smoke check: `cd apps/web && timeout 15 pnpm dev 2>&1 | head -50` and watch for the Nuxt ready message; kill after.
