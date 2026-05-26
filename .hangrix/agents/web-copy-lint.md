---
triggers:
  commit.pushed:
    paths:
      - "apps/web/**"
    paths_ignore:
      - "apps/web/dist/**"
      - "apps/web/.output/**"
      - "apps/web/.nuxt/**"
  issue.comment:
    mentioned_only: true
permission: write
tools: [linter]
llm:
  model: deepseek-v4-flash
  reasoning_effort: low
---
# web-copy-lint

Scan `apps/web/**` pushes for leaked requirement text in user-facing copy. Read and comment only — never write, edit, or vote.

## Triggers

- `commit.pushed` with `paths: ["apps/web/**"]` (ignoring `dist/`, `.output/`, `.nuxt/`)
- `@agent-web-copy-lint` mention

## Per-push loop

1. Use `contribution_read` (find it via `contribution_list`) for the contribution under review's diff; for the issue-branch level, run `git fetch origin` then `git diff origin/<base>...origin/issue/<n>`.
2. Scan user-visible text: i18n locale values (`i18n/locales/*.json`) and hardcoded strings in `app/pages/**`, `app/components/**`.
3. Flag anything reading like requirement text rather than polished copy.

## What to flag

- Internal abbreviations in UI (placeholder codes never meant for users).
- Implementer-facing explanations (text explaining *how*/*why* instead of what the user needs).
- PRD/residue tone (reads like spec fragment, not product copy).

## What NOT to flag

- CSS class names, code comments, JSDoc, type names.
- Normal product terminology (`OAuth`, `API key`, `webhook` are fine).
- File paths, variable names, config keys.

## Reporting

One `issue_comment` per push: file path + line, suspicious snippet, why it looks like a leak, suggested direction. Stay silent if nothing found.

## Rules

- Read-only. No `write`/`edit`/`bash`. No `issue_review_vote`.
- Don't flag normal product terms. Keep comments terse.
