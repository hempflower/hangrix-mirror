# sqlc + goose conventions

Every module that touches Postgres uses **sqlc-generated** queries — no hand-written `pool.QueryRow` calls in new code. Layout per module:

```
apps/hangrix/internal/modules/<name>/infra/
├── queries.sql          # sqlc input — use sqlc.arg('name') / sqlc.narg('name')
├── <name>db/            # generated package; DO NOT hand-edit
└── migrations/
    └── <NNNNN>_<slug>.sql   # -- +goose Up / -- +goose Down
```

Workflow: edit `queries.sql`, then `cd apps/hangrix && sqlc generate`. The binary lives at `/go/bin/sqlc` (the agent container's Dockerfile installs it).

## Cross-module FK trick — the non-obvious part

The build-time `sqlc generate` is per-module, but Postgres tables can FK across modules (e.g. `repos.user_id → users.id`). sqlc cannot parse a FK without seeing the referenced schema. The fix lives in [apps/hangrix/sqlc.yaml](apps/hangrix/sqlc.yaml): for any module whose queries FK another module, list **every referenced schema dir** under that module's `schema:` array. Example from the file: the `repo` module's sqlc entry unions `user + org + repo` migrations.

At **runtime** each module still applies only its own migrations via a dedicated `goose_<name>` version table — the union is purely so sqlc's parser can resolve types at generate time. Module load order in ioc handles dependency sequencing (org module depends on user, repo on org, etc., via `ioc.Module` deps).

## Migrations

- **Never edit a shipped migration.** Forward-applies must be idempotent; add a new migration if you need to change a structure.
- Always supply both `Up` and `Down` blocks.
- New migration file naming: `<5-digit-padded-NNNNN>_<snake_slug>.sql`. The numbers are per-module, not global.

## Tests

Do **not** commit a `_test.go` next to generated `<name>db/queries.sql.go`. The sqlc regeneration command would happily delete and rewrite that directory; your test would vanish silently.

Integration tests under `internal/modules/<x>/infra/` need a live Postgres on `:5432`. Postgres 17 is baked into the agent container by [.hangrix/agent.Dockerfile](.hangrix/agent.Dockerfile) and started by s6-overlay at container boot, so it is already up by the time your test runs — no extra step. The cluster ships with the credentials the code expects (`hangrix:hangrix@localhost:5432/hangrix`).
