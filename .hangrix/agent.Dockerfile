# syntax=docker/dockerfile:1.7
FROM mcr.microsoft.com/devcontainers/go:1.26-bookworm

ARG NODE_MAJOR=20
ARG PNPM_VERSION=10.33.2
ARG SQLC_VERSION=v1.30.0
ARG GOOSE_VERSION=v3.27.0
ARG PG_MAJOR=17
ARG S6_OVERLAY_VERSION=3.2.0.2
ARG TARGETARCH

# Node + pnpm
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
 && apt-get install -y --no-install-recommends nodejs xz-utils \
 && rm -rf /var/lib/apt/lists/* \
 && corepack enable \
 && corepack prepare pnpm@${PNPM_VERSION} --activate

# PostgreSQL ${PG_MAJOR} (pgdg apt repo) + Redis 7 (Debian 12 default) + procps.
# Versions match docker-compose.yml so tests against the embedded DB
# behave the same as against the compose stack. Cluster is initialised
# at build time with the credentials the repo's code expects
# (postgres://hangrix:hangrix@localhost:5432/hangrix), then stopped —
# s6-overlay starts it at container boot.
RUN install -d /usr/share/postgresql-common/pgdg \
 && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
      -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
 && echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" \
      > /etc/apt/sources.list.d/pgdg.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      postgresql-${PG_MAJOR} postgresql-client-${PG_MAJOR} redis-server procps \
 && rm -rf /var/lib/apt/lists/* \
 && pg_ctlcluster ${PG_MAJOR} main start \
 && su - postgres -c "psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE hangrix WITH LOGIN PASSWORD 'hangrix' SUPERUSER;\"" \
 && su - postgres -c "createdb -O hangrix hangrix" \
 && pg_ctlcluster ${PG_MAJOR} main stop

# s6-overlay: PID 1 = /init, which supervises postgres + redis (and any
# future service drop-in under /etc/s6-overlay/s6-rc.d/). The host's
# `.hangrix/agents.yml` opts into this by setting
# `container.entrypoint: [/init]`; without that, the runner uses its
# built-in `sleep infinity` default and these services stay dormant.
RUN <<'SETUP'
set -eux
case "${TARGETARCH:-$(dpkg --print-architecture)}" in
  amd64) S6_ARCH=x86_64 ;;
  arm64) S6_ARCH=aarch64 ;;
  *) echo "unsupported arch: ${TARGETARCH}"; exit 1 ;;
esac
curl -fsSL "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz" -o /tmp/s6-noarch.tar.xz
curl -fsSL "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6_ARCH}.tar.xz" -o /tmp/s6-arch.tar.xz
tar -C / -Jxpf /tmp/s6-noarch.tar.xz
tar -C / -Jxpf /tmp/s6-arch.tar.xz
rm /tmp/s6-noarch.tar.xz /tmp/s6-arch.tar.xz

# postgres service: longrun supervised process, runs in foreground as
# the `postgres` system user. /var/run/postgresql lives on tmpfs in a
# fresh container, so the run script (executed as root before the
# setuidgid hand-off) recreates it on each boot.
mkdir -p /etc/s6-overlay/s6-rc.d/postgres
echo longrun > /etc/s6-overlay/s6-rc.d/postgres/type
cat > /etc/s6-overlay/s6-rc.d/postgres/run <<'RUN'
#!/bin/sh
mkdir -p /var/run/postgresql
chown postgres:postgres /var/run/postgresql
exec /command/s6-setuidgid postgres \
  /usr/lib/postgresql/17/bin/postgres \
  -D /var/lib/postgresql/17/main \
  -c config_file=/etc/postgresql/17/main/postgresql.conf
RUN
chmod 755 /etc/s6-overlay/s6-rc.d/postgres/run

# redis service: same pattern. The Debian default config has
# `daemonize no` and `supervised no` already, perfect for s6.
mkdir -p /etc/s6-overlay/s6-rc.d/redis
echo longrun > /etc/s6-overlay/s6-rc.d/redis/type
cat > /etc/s6-overlay/s6-rc.d/redis/run <<'RUN'
#!/bin/sh
mkdir -p /var/run/redis
chown redis:redis /var/run/redis
exec /command/s6-setuidgid redis /usr/bin/redis-server /etc/redis/redis.conf
RUN
chmod 755 /etc/s6-overlay/s6-rc.d/redis/run

# Enable both services under the default `user` bundle (empty marker
# files — s6-rc reads the directory listing).
mkdir -p /etc/s6-overlay/s6-rc.d/user/contents.d
touch /etc/s6-overlay/s6-rc.d/user/contents.d/postgres
touch /etc/s6-overlay/s6-rc.d/user/contents.d/redis
SETUP

ENTRYPOINT ["/init"]

# Go tooling
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@${SQLC_VERSION} \
 && go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION} \
 && go install github.com/air-verse/air@v1.65.1

# Playwright MCP + Chrome for Testing — for browser-based verification (web role).
# install-browser chrome-for-testing downloads the branded Chrome for Testing
# binary and headless shell via @playwright/mcp's wrapper around
# `playwright install` (replaces the older `npm install -g playwright` +
# `npx playwright install chrome` combo).
RUN npx @playwright/mcp@latest install-browser chrome-for-testing

ENV PNPM_HOME=/caches/pnpm
ENV PATH=$PNPM_HOME:/go/bin:$PATH
ENV GOMODCACHE=/go/pkg/mod
# s6-overlay reaps zombies and waits for services to come up; tell it
# not to require a stage-2 user script.
ENV S6_KEEP_ENV=1
ENV S6_BEHAVIOUR_IF_STAGE2_FAILS=2
