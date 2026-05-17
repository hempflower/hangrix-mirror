#!/usr/bin/env bash
#
# End-to-end smoke runner for the multi-role agent loop, docker-compose
# edition. Currently validates the M7b retirement criteria (dispatcher
# → backend → reviewer → maintainer); the harness itself is generic
# enough that later milestones can drop in more roles / fixtures.
#
# The compose stack in scripts/smoke/compose.yml is self-contained:
# postgres + redis + server + runner all run as services on the
# hangrix-smoke bridge network. Agent containers the runner spawns join
# the same network so they reach `server:8080` by docker-DNS — none of
# the devcontainer-specific path / network workarounds in the previous
# revision are needed.
#
# Subcommands:
#
#   up        build images + bring stack up (server first, then runner
#             after enrollment seeds its state.json)
#   setup     `up` + admin / org / LLM provider / 4 agent repos + host
#             repo via the API
#   smoke     open the demo issue and tail the audit roster + timeline
#   down      docker compose down -v (wipes postgres + repos volumes)
#   logs SVC  follow logs of one service (server / runner / postgres / …)
#   shell SVC `docker compose exec` into a service for poking around
#
# Required env (or `.env` next to compose.yml):
#
#   HANGRIX_SMOKE_STATE_DIR  absolute host path holding runner state +
#                            agent binaries cache + bundle cache.
#                            Defaults to <repo-root>/data/smoke.
#   LLM_API_KEY              your DeepSeek (or OpenAI-compat) key.
#   LLM_MODEL                model name; must be in the provider's
#                            allowed_models list. Default deepseek-v4-pro.
#   LLM_BASE_URL             provider base URL (no `/v1` suffix).
#                            Default https://api.deepseek.com.
#   ADMIN_USER / ADMIN_PASSWORD / ADMIN_EMAIL
#                            first-user credentials run.sh registers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source .env if present so LLM_API_KEY / overrides land in env before
# defaults below. Compose CLI also reads this file for its own
# interpolation; sourcing here keeps bash + compose in sync.
if [ -f "$SCRIPT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$SCRIPT_DIR/.env"
  set +a
fi

# ----------------------------------------------------------------------
# defaults — overridable via env / .env file
# ----------------------------------------------------------------------
: "${HANGRIX_SMOKE_STATE_DIR:=$REPO_ROOT/data/smoke}"

# Pick the SERVER URL the API curl calls go to:
#   - bare host (no /.dockerenv): the compose stack publishes 8080 on
#     the host loopback, so http://localhost:8080 is the right URL.
#   - inside a sibling container (devcontainer, CI runner image, …):
#     localhost is OUR loopback, not the daemon's. Reach the compose
#     server by its service DNS name. We also attach the current
#     container to the hangrix-smoke network so DNS resolves; failures
#     are silently ignored (idempotent: re-attach is a no-op).
if [ -r /.dockerenv ]; then
  : "${SERVER:=http://server:8080}"
else
  : "${SERVER:=http://localhost:8080}"
fi
: "${ADMIN_USER:=smoke-admin}"
: "${ADMIN_EMAIL:=smoke-admin@example.com}"
: "${ADMIN_PASSWORD:=smoke-pass-1234}"
: "${ORG_NAME:=smoke}"
: "${LLM_PROVIDER:=deepseek}"
: "${LLM_TYPE:=openai-compat}"
: "${LLM_BASE_URL:=https://api.deepseek.com}"
: "${LLM_MODEL:=deepseek-v4-pro}"
: "${LLM_API_KEY:=}"
: "${HOST_REPO_NAME:=host}"
HOST_REPO="$ORG_NAME/$HOST_REPO_NAME"
ROLES=(dispatcher backend reviewer maintainer)
FIXTURES="$SCRIPT_DIR/fixtures"
export HANGRIX_SMOKE_STATE_DIR

# Path inside the runner container = host path (compose bind-mounts the
# same absolute path on both sides). cookie + agent-binaries / bundles
# all live here.
mkdir -p "$HANGRIX_SMOKE_STATE_DIR"
COOKIE_JAR="$HANGRIX_SMOKE_STATE_DIR/cookies.txt"

COMPOSE=(docker compose -f "$SCRIPT_DIR/compose.yml")

# ----------------------------------------------------------------------
# helpers
# ----------------------------------------------------------------------
log()  { printf '\033[1;36m[smoke]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 required ($2)"; }

# curl_json METHOD PATH [body-file]
curl_json() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" \
      -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -H "Content-Type: application/json" \
      --data @"$body" \
      "$SERVER$path"
  else
    curl -fsS -X "$method" \
      -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      "$SERVER$path"
  fi
}

# Strip http:// off SERVER for the git Basic URL: `http://user:pass@host:port/...`.
server_host_no_scheme() { echo "${SERVER#http://}"; }
git_basic_url() {
  local full="$1"
  echo "http://$ADMIN_USER:$ADMIN_PASSWORD@$(server_host_no_scheme)/git/$full.git"
}

wait_for_server() {
  log "waiting for server to answer /api/auth/me (max 60s)"
  for _ in $(seq 1 60); do
    # Drop -f: any HTTP response (200 / 401 / …) counts as "server is
    # serving"; only transport errors (-w writes 000 then) keep waiting.
    code=$(curl -sS -o /dev/null -w '%{http_code}' "$SERVER/api/auth/me" 2>/dev/null || echo 000)
    case "$code" in
      2*|3*|4*) log "  server responsive (http $code)"; return ;;
    esac
    sleep 1
  done
  die "server didn't become responsive at $SERVER; tail server logs: $0 logs server"
}

# ----------------------------------------------------------------------
# subcommand: build / up / down
# ----------------------------------------------------------------------
cmd_build() { "${COMPOSE[@]}" build; }

cmd_up() {
  need docker "install docker"
  need jq    "apt install jq"
  need curl  "apt install curl"
  need git   "apt install git"
  log "building agent base image (hangrix-smoke-agent:latest)"
  docker build -q -t hangrix-smoke-agent:latest "$SCRIPT_DIR/agent-image" >/dev/null
  log "starting postgres + redis + server (runner waits on enrollment)"
  "${COMPOSE[@]}" up --build -d postgres redis server
  # When run.sh runs inside a sibling container (devcontainer), join
  # the smoke network so docker-DNS resolves `server:8080`. The host
  # case skips this (localhost:8080 routes via the published port).
  if [ -r /.dockerenv ]; then
    local self; self=$(hostname)
    if docker network connect hangrix-smoke "$self" >/dev/null 2>&1; then
      log "  attached $self to hangrix-smoke network"
    fi
  fi
  wait_for_server
  ensure_admin
  ensure_org
  ensure_llm_provider
  enroll_runner
  log "starting runner"
  "${COMPOSE[@]}" up -d runner
}

cmd_down() {
  log "tearing down compose stack + volumes"
  "${COMPOSE[@]}" down -v
  rm -f "$COOKIE_JAR"
}

cmd_logs() {
  local svc="${1:-}"
  if [ -z "$svc" ]; then
    "${COMPOSE[@]}" logs -f
  else
    "${COMPOSE[@]}" logs -f "$svc"
  fi
}

cmd_shell() {
  local svc="${1:-runner}"
  "${COMPOSE[@]}" exec "$svc" bash
}

# ----------------------------------------------------------------------
# admin / org / provider / runner enrollment
# ----------------------------------------------------------------------
ensure_admin() {
  log "ensure admin user $ADMIN_USER"
  local body
  body=$(mktemp); printf '{"username":"%s","password":"%s"}\n' "$ADMIN_USER" "$ADMIN_PASSWORD" >"$body"
  if curl -fsS -X POST -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -H "Content-Type: application/json" --data @"$body" \
      "$SERVER/api/auth/login" >/dev/null 2>&1; then
    rm -f "$body"; log "  login OK"; return
  fi
  printf '{"username":"%s","email":"%s","password":"%s"}\n' \
    "$ADMIN_USER" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" >"$body"
  curl -fsS -X POST -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -H "Content-Type: application/json" --data @"$body" \
    "$SERVER/api/auth/register" >/dev/null
  rm -f "$body"
  log "  registered + logged in (first user → admin)"
}

ensure_org() {
  log "ensure org $ORG_NAME"
  if curl_json GET "/api/orgs/$ORG_NAME" >/dev/null 2>&1; then
    log "  already exists"; return
  fi
  local body
  body=$(mktemp)
  jq -n --arg n "$ORG_NAME" --arg d "smoke tenant" \
        '{name:$n, display_name:$n, description:$d}' >"$body"
  curl_json POST "/api/orgs/" "$body" >/dev/null
  rm -f "$body"
  log "  created"
}

ensure_llm_provider() {
  log "ensure LLM provider $LLM_PROVIDER ($LLM_MODEL @ $LLM_BASE_URL)"
  if curl_json GET "/api/admin/llm/providers/$LLM_PROVIDER" >/dev/null 2>&1; then
    log "  already registered"; return
  fi
  [ -n "$LLM_API_KEY" ] || die "LLM_API_KEY not set (paste your DeepSeek key, or set in .env)"
  local body
  body=$(mktemp)
  jq -n --arg n "$LLM_PROVIDER" --arg t "$LLM_TYPE" --arg u "$LLM_BASE_URL" \
        --arg k "$LLM_API_KEY" --arg m "$LLM_MODEL" \
        '{name:$n, type:$t, base_url:$u, api_key:$k, allowed_models:[$m]}' \
        >"$body"
  curl_json POST "/api/admin/llm/providers" "$body" >/dev/null
  rm -f "$body"
  log "  registered"
}

enroll_runner() {
  if [ -s "$HANGRIX_SMOKE_STATE_DIR/state.json" ]; then
    log "runner already enrolled (state.json present)"
    return
  fi
  log "enrolling runner"
  local body resp enroll
  body=$(mktemp); printf '{"name":"smoke-runner","visibility":"platform"}\n' >"$body"
  resp=$(curl_json POST "/api/admin/runners" "$body")
  rm -f "$body"
  enroll=$(jq -r '.enroll_token' <<<"$resp")
  [ -n "$enroll" ] && [ "$enroll" != "null" ] || die "no enroll_token in response: $resp"
  # Run hangrix-runner enroll inside a one-shot runner container. The
  # state dir bind-mount captures state.json into the host directory
  # so the long-running runner service finds it on startup.
  "${COMPOSE[@]}" run --rm -e "HANGRIX_RUNNER_STATE_DIR=$HANGRIX_SMOKE_STATE_DIR" \
      --entrypoint /usr/local/bin/hangrix-runner runner \
      enroll --server "http://server:8080" --token "$enroll"
  log "  enrolled, state.json written to $HANGRIX_SMOKE_STATE_DIR"
}

# ----------------------------------------------------------------------
# agent / host repo fixtures
# ----------------------------------------------------------------------
ensure_agent_repos() {
  for role in "${ROLES[@]}"; do
    create_repo_if_missing "$role" "agent repo for $role"
    push_agent_repo "$role" "$ORG_NAME/$role"
    tag_agent_repo  "$ORG_NAME/$role" "v0.1.0"
  done
}

create_repo_if_missing() {
  local name="$1" desc="$2"
  local full="$ORG_NAME/$name"
  if curl_json GET "/api/repos/$full" >/dev/null 2>&1; then return; fi
  log "  creating repo $full"
  local body; body=$(mktemp)
  jq -n --arg n "$name" --arg d "$desc" --arg o "$ORG_NAME" \
        '{name:$n, description:$d, visibility:"public", init_readme:true, owner:$o}' \
        >"$body"
  curl_json POST "/api/repos/" "$body" >/dev/null
  rm -f "$body"
}

# Default branch is protected by the M4 IssueGuard — every code change
# must land via an issue + API merge. The smoke setup opens one
# bootstrap issue per repo, pushes the fixture onto its `issue/<n>`
# branch, then POST /merge to fast-forward into main.
push_agent_repo() {
  local role="$1" full="$2"
  if has_fixture_content "$full"; then
    log "  fixture already merged into $full"; return
  fi
  local issue_num; issue_num=$(open_bootstrap_issue "$full" "smoke: seed $role fixture")
  log "  pushing fixture for $full via issue #$issue_num"
  local tmp; tmp=$(mktemp -d)
  local url; url=$(git_basic_url "$full")
  (
    cd "$tmp"
    git clone -q "$url" .
    git config user.name "$ADMIN_USER"
    git config user.email "$ADMIN_EMAIL"
    git checkout -q -B "issue/$issue_num"
    cp -r "$FIXTURES/$role/." .
    if [ -f README.md ]; then
      printf '# %s (smoke fixture)\n\nGenerated by scripts/smoke/run.sh; do not edit manually.\n' "$role" >README.md
    fi
    git add .
    git commit -q -m "smoke: $role agent v0.1.0"
    git push -q "$url" "issue/$issue_num"
  )
  rm -rf "$tmp"
  curl_json POST "/api/repos/$full/issues/$issue_num/merge" >/dev/null
}

has_fixture_content() {
  local full="$1"
  local refs; refs=$(curl_json GET "/api/repos/$full/refs" 2>/dev/null || echo '{}')
  local sha; sha=$(jq -r '.default_branch_sha // ""' <<<"$refs")
  [ -n "$sha" ] || return 1
  local tree; tree=$(curl_json GET "/api/repos/$full/tree?ref=$sha&path=" 2>/dev/null || echo '[]')
  jq -e 'any(.name == "agent.yml")' <<<"$tree" >/dev/null 2>&1
}

open_bootstrap_issue() {
  local full="$1" title="$2"
  local body resp
  body=$(mktemp); jq -n --arg t "$title" '{title:$t, body:"smoke bootstrap; auto-merged."}' >"$body"
  resp=$(curl_json POST "/api/repos/$full/issues" "$body")
  rm -f "$body"
  jq -r '.number' <<<"$resp"
}

tag_agent_repo() {
  local full="$1" tag="$2"
  local refs; refs=$(curl_json GET "/api/repos/$full/refs")
  if jq -e --arg t "$tag" '.tags[]? | select(.name==$t)' <<<"$refs" >/dev/null 2>&1; then return; fi
  log "  tagging $full @ $tag"
  local sha; sha=$(jq -r '.default_branch_sha' <<<"$refs")
  [ -n "$sha" ] && [ "$sha" != "null" ] || die "no default_branch_sha on $full"
  local body; body=$(mktemp)
  jq -n --arg n "$tag" --arg r "$sha" '{name:$n, ref:$r, annotated:false}' >"$body"
  curl_json POST "/api/repos/$full/tags" "$body" >/dev/null
  rm -f "$body"
}

ensure_host_repo() {
  log "ensure host repo $HOST_REPO"
  create_repo_if_missing "$HOST_REPO_NAME" "smoke host repo"
  if host_yaml_present; then
    log "  host yaml already on main"; return
  fi
  local issue_num; issue_num=$(open_bootstrap_issue "$HOST_REPO" "smoke: register agent team")
  log "  pushing host yaml via issue #$issue_num"
  local tmp; tmp=$(mktemp -d)
  local url; url=$(git_basic_url "$HOST_REPO")
  (
    cd "$tmp"
    git clone -q "$url" .
    git config user.name "$ADMIN_USER"
    git config user.email "$ADMIN_EMAIL"
    git checkout -q -B "issue/$issue_num"
    rm -rf .hangrix
    mkdir -p .hangrix
    cp "$FIXTURES/host/.hangrix/agents.yml" .hangrix/agents.yml
    write_lock_file >.hangrix/agents.lock
    git add .hangrix
    git commit -q -m "smoke: register dispatcher/backend/reviewer/maintainer team"
    git push -q "$url" "issue/$issue_num"
  )
  rm -rf "$tmp"
  curl_json POST "/api/repos/$HOST_REPO/issues/$issue_num/merge" >/dev/null
}

host_yaml_present() {
  local refs; refs=$(curl_json GET "/api/repos/$HOST_REPO/refs" 2>/dev/null || echo '{}')
  local sha; sha=$(jq -r '.default_branch_sha // ""' <<<"$refs")
  [ -n "$sha" ] || return 1
  local tree; tree=$(curl_json GET "/api/repos/$HOST_REPO/tree?ref=$sha&path=.hangrix" 2>/dev/null || echo '[]')
  jq -e 'any(.name == "agents.yml")' <<<"$tree" >/dev/null 2>&1
}

write_lock_file() {
  printf 'version: 1\nagents:\n'
  for role in "${ROLES[@]}"; do
    local full="$ORG_NAME/$role"
    local refs; refs=$(curl_json GET "/api/repos/$full/refs")
    local sha; sha=$(jq -r '.tags[]? | select(.name=="v0.1.0") | .sha' <<<"$refs")
    [ -n "$sha" ] && [ "$sha" != "null" ] || die "could not resolve $full @ v0.1.0"
    printf '  - ref: %s@v0.1.0\n    resolved_sha: %s\n    resolved_at: %s\n' \
      "$full" "$sha" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  done
}

# ----------------------------------------------------------------------
# subcommand: setup
# ----------------------------------------------------------------------
cmd_setup() {
  cmd_up
  ensure_agent_repos
  ensure_host_repo
  log "setup done. Next: bash scripts/smoke/run.sh smoke"
}

# ----------------------------------------------------------------------
# subcommand: smoke
# ----------------------------------------------------------------------
cmd_smoke() {
  need curl ""
  need jq   ""
  if ! curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$SERVER/api/auth/me" >/dev/null 2>&1; then
    ensure_admin
  fi
  log "opening smoke issue on $HOST_REPO"
  local body resp issue_n repo_id
  body=$(mktemp)
  jq -n '{title:"smoke: add /healthz", body:"Need a healthz endpoint that returns the string \"ok\"."}' >"$body"
  resp=$(curl_json POST "/api/repos/$HOST_REPO/issues" "$body")
  rm -f "$body"
  issue_n=$(jq -r '.number' <<<"$resp")
  repo_id=$(jq -r '.repo_id' <<<"$resp")
  log "  issue #$issue_n (repo_id=$repo_id)"

  log "watching… (Ctrl+C to stop)"
  while true; do
    local rows tl
    rows=$(curl_json GET "/api/admin/agent-sessions/by-issue/$repo_id/$issue_n" 2>/dev/null || echo '{}')
    tl=$(curl_json   GET "/api/repos/$HOST_REPO/issues/$issue_n/timeline" 2>/dev/null || echo '{}')
    clear
    printf 'smoke — issue #%s on %s — %s\n\n' "$issue_n" "$HOST_REPO" "$(date)"
    printf 'Sessions:\n'
    jq -r '.items // [] | if length == 0 then "  (no sessions yet)" else
      (["  role", "status", "agent_sha", "cause_kind"] | @tsv),
      (sort_by(.session_id)[] | ["  "+.role_key, .status, (.agent_sha[0:12]), .cause_kind] | @tsv)
      end' <<<"$rows" | column -t
    printf '\nTimeline (last 12):\n'
    jq -r '
      ((.comments // []) | map({ts:.created_at, kind:"comment", who:(if .agent_role!="" and .agent_role!=null then "agent:"+.agent_role else .author_username end), body:.body}))
      + ((.events // []) | map({ts:.created_at, kind:.kind, who:(if .agent_role!="" and .agent_role!=null then "agent:"+.agent_role else .actor_username end), body:(.payload | tostring)}))
      | sort_by(.ts) | .[-12:] | .[]
      | "  [\(.ts[0:19])] \(.kind|@text) by \(.who): \(.body[0:140])"' <<<"$tl" || true
    if [ "$(jq -r '.state // ""' <<<"$(curl_json GET "/api/repos/$HOST_REPO/issues/$issue_n" 2>/dev/null || echo '{}')")" = "merged" ]; then
      printf '\n\033[1;32m✓ issue merged — M7b retirement chain complete\033[0m\n'
      break
    fi
    sleep 5
  done
}

# ----------------------------------------------------------------------
# dispatch
# ----------------------------------------------------------------------
sub="${1:-}"; shift || true
case "$sub" in
  build)       cmd_build ;;
  up)          cmd_up ;;
  setup)       cmd_setup ;;
  smoke)       cmd_smoke ;;
  down)        cmd_down ;;
  logs)        cmd_logs "${1:-}" ;;
  shell)       cmd_shell "${1:-runner}" ;;
  ""|help|-h|--help)
    cat <<EOF
smoke runner (docker-compose edition).

Subcommands:
  build         docker compose build (forces a fresh image build)
  up            bring up postgres + redis + server, enroll runner, then
                start the runner service
  setup         \`up\` + push 4 agent repos + push host repo with team yaml
  smoke         open the demo issue + tail the audit roster + timeline
  down          stop everything and wipe postgres + repos volumes
  logs SVC      follow logs of one service (server / runner / postgres / …)
  shell SVC     docker compose exec into a service (default: runner)

Required env (or .env file alongside compose.yml):
  HANGRIX_SMOKE_STATE_DIR  absolute host path for runner state (defaults
                           to <repo-root>/data/smoke)
  LLM_API_KEY              DeepSeek / OpenAI-compat key (required by setup)
  LLM_MODEL                model name (default deepseek-v4-pro)
  LLM_BASE_URL             provider base URL (default https://api.deepseek.com)
EOF
    ;;
  *) die "unknown subcommand: $sub" ;;
esac
