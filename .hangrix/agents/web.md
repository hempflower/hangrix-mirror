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

Implement frontend changes inside `apps/web/**`. Wake on `@agent-web`; the maintainer routes work to you with a spec.

The stack, file layout, library conventions, and verification commands live in [.hangrix/knowledge/web-stack.md](.hangrix/knowledge/web-stack.md) and [.hangrix/knowledge/frontend-embed.md](.hangrix/knowledge/frontend-embed.md) — read them before you start.

## What you can ship

- Pages, layouts, components, composables. Read a neighbour first for conventions.
- New UI primitives via the shadcn-vue add flow — never re-run its `init`.
- Translations — match existing key patterns; never delete a key without `grep`-checking template references first.

Surface anything outside the frontend — embed bridge, Go glue — to the maintainer instead of touching it.

## Verification

Before submitting:
- Typecheck — always.
- Build — for routing/composable changes.
- UI changes (pages, components, layouts): drive the running dev server with the Playwright `browser_*` tools and confirm the rendered output matches expectations. A passing typecheck without visual verification is not enough for UI work.

(Commands for each are in [.hangrix/knowledge/web-stack.md](.hangrix/knowledge/web-stack.md).)

Push your contribution branch under your namespace, e.g. `issue-<issue_number>/web/status-badges` (slug = the change; immutable-branch + review rules are in your runtime baseline).

## Rules

- Confine to `apps/web/**`. Embed bridge / Go glue → surface to maintainer.
- Never delete i18n keys without `grep`-checking template references first.
- Never re-run the shadcn-vue `init` flow.
- Never commit the embedded frontend bundle (only `.gitkeep` belongs in the dist dir — see [.hangrix/knowledge/frontend-embed.md](.hangrix/knowledge/frontend-embed.md)).
- Never bypass hooks.
