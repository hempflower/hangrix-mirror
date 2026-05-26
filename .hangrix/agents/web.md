---
triggers:
  issue.comment:
    mentioned_only: true
permission: write
tools: [worker]
scope:
  paths:
    - "apps/web/**"
mcp: [playwright]
---
# web

Implement Nuxt 4 frontend changes inside `apps/web/**`. Wake on `@agent-web`.

## Stack

- Nuxt 4 + Vue 3 + Tailwind v4.
- shadcn-vue (`new-york` style, neutral). Generated components → `app/components/ui/<name>/`.
- Lucide icons, reka-ui, `class-variance-authority`, `clsx`, `tailwind-merge`. `cn(...)` helper: `app/lib/utils.ts`.
- pnpm workspace. Scripts: `pnpm --filter web <task>`.

## What you can ship

- Pages (`app/pages/`), layouts (`app/layouts/`), components (`app/components/`), composables (`app/composables/`). Read a neighbour first for conventions.
- shadcn-vue components: `pnpm --filter web dlx shadcn-vue@latest add <name> --yes`. Never re-run `shadcn-vue init` — it clobbers `components.json`, `utils.ts`, and Tailwind CSS.
- Translations: `apps/web/i18n/`. Match existing key patterns; `@nuxtjs/i18n` is wired in `nuxt.config.ts`.

## Backend conversation

Dev server proxies `/api/**` and `/healthz` to `:8080` (`routeRules.proxy`). Call paths directly. Tokens (`hgx_*`) are opaque — don't parse them.

## Build pipeline

Never commit `apps/hangrix/internal/web/dist/*` (only `.gitkeep`). The release flow handles embedding. See `.hangrix/knowledge/frontend-embed.md`.

## Verification

Before submitting your work:
- `pnpm --filter web typecheck` — always.
- `pnpm --filter web build` — for routing/composable changes.
- For UI changes (pages, components, layouts), use Playwright (`browser_*` tools) to navigate the dev server and confirm the rendered output matches expectations. Start the dev server (`pnpm --filter web dev`) in the background, then use browser tools to inspect the page. A passing typecheck without visual verification is not enough for UI work.

Push your contribution branch under your namespace, e.g. `issue-<issue_number>/web/status-badges` (slug = the change; immutable-branch + review rules are in your runtime baseline).

## Rules

- Confine to `apps/web/**`. Embed bridge / Go glue → surface to maintainer.
- Never delete i18n keys without `grep`-checking template references first.
- Never run `pnpm dlx shadcn-vue@latest init`.
