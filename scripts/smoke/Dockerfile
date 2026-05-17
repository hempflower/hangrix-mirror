# Multi-stage build for the smoke stack.
#
# Stage 1 (`builder`) produces statically-linked binaries:
#   - hangrix-agent  → copied into apps/hangrix/.../payload/ so the
#                      server build embeds it via //go:embed.
#   - hangrix        → the platform server.
#   - hangrix-runner → the runner that spawns agent containers.
#
# Stage 2 (`server`) is the runtime image for the server. It needs
# git on PATH because the platform shells out to `git archive` /
# `git cat-file` for bundle distribution and host-yaml reads.
#
# Stage 3 (`runner`) is the runtime image for the runner. It needs
# the docker CLI (the runner shells out to `docker run …` to spawn
# agent containers — the docker daemon socket is bind-mounted in
# from the host by compose).
#
# Both runtime images use `ubuntu:24.04` so the smoke environment
# stays consistent with the agent base image (also ubuntu:24.04).

ARG GO_VERSION=1.26
ARG UBUNTU_VERSION=24.04

# ----------------------------------------------------------------------
# Stage 1 — builder
# ----------------------------------------------------------------------
FROM golang:${GO_VERSION} AS builder
WORKDIR /src

# Copy the whole monorepo. .dockerignore (sibling file) trims out
# node_modules, web/dist, data/, etc. so the build context stays small.
COPY . .

ENV CGO_ENABLED=0
ENV GOFLAGS="-trimpath"

# Order matters: the server's //go:embed payload includes hangrix-agent,
# so the agent must be built and placed in payload/ BEFORE the server
# build runs. We also wipe the embedded web SPA placeholder so go:embed
# is happy when the JS dist isn't pre-built (smoke doesn't need the UI).
RUN cd apps/hangrix-agent \
 && go build -o /out/hangrix-agent ./cmd/hangrix-agent \
 && cp /out/hangrix-agent /src/apps/hangrix/internal/modules/runner/binaries/payload/hangrix-agent

RUN cd apps/hangrix-runner \
 && go build -o /out/hangrix-runner ./cmd/hangrix-runner \
 && cp /out/hangrix-runner /src/apps/hangrix/internal/modules/runner/binaries/payload/hangrix-runner

# Web dist is empty in source. The server's web.go has a placeholder
# branch that activates when index.html is absent, so we leave it alone.
RUN cd apps/hangrix \
 && go build -o /out/hangrix ./cmd/hangrix

# ----------------------------------------------------------------------
# Stage 2 — server runtime
# ----------------------------------------------------------------------
FROM ubuntu:${UBUNTU_VERSION} AS server
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/hangrix /usr/local/bin/hangrix
COPY scripts/smoke/server-config.yaml /etc/hangrix/config.yaml

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hangrix"]
CMD ["--config", "/etc/hangrix/config.yaml"]

# ----------------------------------------------------------------------
# Stage 3 — runner runtime
# ----------------------------------------------------------------------
FROM ubuntu:${UBUNTU_VERSION} AS runner
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates curl docker.io \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/hangrix-runner /usr/local/bin/hangrix-runner

# The runner state directory is set by compose via env. The container
# image itself ships with no state — every runner instance mints fresh
# enrollment + cache directories under HANGRIX_RUNNER_STATE_DIR.
ENTRYPOINT ["/usr/local/bin/hangrix-runner"]
