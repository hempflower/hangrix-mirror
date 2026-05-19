# Local stack & test prerequisites

Three places Postgres + Redis live; mind which one your turn is hitting.

- **Inside the agent container** — Postgres 17 + Redis 7 are **baked into the image** by [.hangrix/agent.Dockerfile](.hangrix/agent.Dockerfile) and supervised by **s6-overlay** as PID 1. They start automatically when the container starts; nothing for the agent to do. The DSN `postgres://hangrix:hangrix@localhost:5432/hangrix` and Redis on `:6379` are live as soon as the agent loop is running. s6 cleanly shuts both down on `docker stop`.

  This works because [.hangrix/agents.yml](.hangrix/agents.yml) sets `container.entrypoint: [/init]` — without that the runner overrides PID 1 with its default `sleep infinity` and s6 never runs (postgres + redis stay dormant). Adding the entrypoint line is the one piece of config that ties the s6 image to the runner.

- **`./docker-compose.yml`** (repo root) — production-shape Postgres 17 + Redis 7 on `:5432` / `:6379`. Used by humans running the stack outside the agent container. Agent sessions **should not** invoke this — the in-image services on the same DSN already work.
- **`./.devcontainer/docker-compose.yml`** — wraps the same Postgres/Redis behind the dev `app` service (`mcr.microsoft.com/devcontainers/go:1.26-bookworm`) and bind-mounts `/var/run/docker.sock` for sibling docker calls. Dev-environment only; not part of the agent runtime.

## DSN / connection strings the code expects

- Postgres: `postgres://hangrix:hangrix@localhost:5432/hangrix?sslmode=disable` (matches the compose env block).
- Redis: `redis://localhost:6379/0`.

`apps/hangrix/conf/config.yaml` is the default file; the env override prefix is `API_` (`server.addr` → `API_SERVER_ADDR`, `database.dsn` → `API_DATABASE_DSN`).

## Common test failure modes

- **`connection refused` from pgx inside an agent container** → s6 did not start postgres. Most likely `container.entrypoint: [/init]` is missing from `.hangrix/agents.yml` (the runner reverted to `sleep infinity`), or the image was built without s6 (check [.hangrix/agent.Dockerfile](.hangrix/agent.Dockerfile)). Confirm with `pgrep -x postgres` or `pg_isready -h 127.0.0.1 -U hangrix`.
- **`connection refused` outside an agent container** → bring the dev stack up: `docker compose up -d postgres`.
- **`relation does not exist` from sqlc-generated code** → goose migrations have not been applied to the test DB. The server's startup applies them; if your test bypasses startup, apply them manually with `goose -dir internal/modules/<x>/infra/migrations postgres "$DSN" up`.
- **Redis tests timing out** → same diagnosis path as postgres: `pgrep -x redis-server` and check the entrypoint wiring.

## Persistent runtime storage

`apps/hangrix/conf/config.yaml`'s `storage.repos_path` defaults to `./data/repos`. The whole `./data/` tree is gitignored — never commit anything under it. If a test writes there, that is a bug or a missing temp-dir fixture.
