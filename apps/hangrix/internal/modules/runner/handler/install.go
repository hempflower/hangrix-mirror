// Public runner-install surface. Two routes, both unauthenticated:
//
//	GET /install/runner.sh        - templated bash one-liner installer
//	GET /install/hangrix-runner   - the runner binary (embedded payload)
//
// The script is what the admin runners page surfaces in the enroll-token
// dialog — the operator on a fresh machine runs:
//
//	curl -fsSL https://<server>/install/runner.sh | sh -s -- hgxe_<token>
//
// and gets an enrolled runner. Possessing the binary alone is harmless:
// without an enroll token the runner cannot reach any authenticated
// /api/runner/* surface.
package handler

import (
	"fmt"
	"net/http"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
)

// installScriptTemplate is the bash one-liner the server hands operators.
// The %[1]s placeholder is replaced with the server's public base URL at
// request time so a copy-pasted curl|sh "just works" against the
// instance that minted the enroll token. Kept POSIX-shell (#!/bin/sh) so
// it runs on busybox-only images.
//
// Behaviour at a glance:
//
//  1. download the runner binary to /usr/local/bin (root) or
//     ~/.local/bin (fallback).
//  2. run `hangrix-runner enroll` to redeem the supplied token.
//  3. if systemd is detected AND the script is running as root AND
//     --no-service was not passed, write a systemd unit + enable+start
//     hangrix-runner.service. State dir is /var/lib/hangrix in this
//     mode so the service-managed runner doesn't collide with a
//     developer's user-mode state under $HOME/.hangrix.
//  4. otherwise leave the operator with a hint about how to start it.
const installScriptTemplate = `#!/bin/sh
# Hangrix runner installer (one-shot).
#
# Usage:
#   curl -fsSL %[1]s/install/runner.sh | sh -s -- <enroll-token> [--no-service]
#
# Env overrides:
#   HANGRIX_RUNNER_BIN        install path (default /usr/local/bin/hangrix-runner)
#   HANGRIX_RUNNER_SERVER     server base URL (default %[1]s)
#   HANGRIX_RUNNER_STATE_DIR  override service state dir (default /var/lib/hangrix)
set -eu

SERVER="${HANGRIX_RUNNER_SERVER:-%[1]s}"
BIN_DEFAULT="/usr/local/bin/hangrix-runner"
BIN="${HANGRIX_RUNNER_BIN:-$BIN_DEFAULT}"
SERVICE_STATE_DIR="${HANGRIX_RUNNER_STATE_DIR:-/var/lib/hangrix}"

TOKEN=""
SKIP_SERVICE=0

# Trivial flag parsing — first positional is the token, --no-service
# disables the systemd hook even when running as root.
while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-service) SKIP_SERVICE=1 ;;
    --help|-h)
      echo "usage: curl -fsSL $SERVER/install/runner.sh | sh -s -- <enroll-token> [--no-service]"
      exit 0
      ;;
    --*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)   [ -z "$TOKEN" ] && TOKEN="$1" || { echo "extra arg: $1" >&2; exit 2; } ;;
  esac
  shift
done
[ -z "$TOKEN" ] && TOKEN="${HANGRIX_ENROLL_TOKEN:-}"

if [ -z "$TOKEN" ]; then
  echo "error: no enroll token supplied" >&2
  echo "usage: curl -fsSL $SERVER/install/runner.sh | sh -s -- <enroll-token> [--no-service]" >&2
  exit 2
fi

IS_ROOT=0
[ "$(id -u 2>/dev/null || echo 1000)" = "0" ] && IS_ROOT=1

echo "==> downloading hangrix-runner -> $BIN"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
curl -fsSL "$SERVER/install/hangrix-runner" -o "$TMP"
chmod +x "$TMP"

# Move into place; fall back to ~/.local/bin if /usr/local/bin is not writable.
if ! mv "$TMP" "$BIN" 2>/dev/null; then
  ALT="$HOME/.local/bin/hangrix-runner"
  mkdir -p "$HOME/.local/bin"
  mv "$TMP" "$ALT"
  BIN="$ALT"
  echo "==> /usr/local/bin not writable; installed to $BIN"
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) ;;
    *) echo "note: add $HOME/.local/bin to PATH" ;;
  esac
fi

# Decide service-manager strategy BEFORE running enroll so the enroll
# writes state.json into the right dir for the chosen path.
SERVICE_MANAGER=""
if [ "$SKIP_SERVICE" = "0" ] && [ "$IS_ROOT" = "1" ]; then
  if command -v systemctl >/dev/null 2>&1 && [ -d /etc/systemd/system ] && systemctl --version >/dev/null 2>&1; then
    SERVICE_MANAGER="systemd"
  fi
fi

if [ "$SERVICE_MANAGER" = "systemd" ]; then
  echo "==> systemd detected; enrolling with state dir $SERVICE_STATE_DIR"
  mkdir -p "$SERVICE_STATE_DIR"
  HANGRIX_RUNNER_STATE_DIR="$SERVICE_STATE_DIR" "$BIN" enroll --server "$SERVER" --token "$TOKEN"

  UNIT="/etc/systemd/system/hangrix-runner.service"
  echo "==> writing systemd unit -> $UNIT"
  cat > "$UNIT" <<EOSVC
[Unit]
Description=Hangrix runner
Documentation=https://github.com/anthropics/hangrix
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN serve
Environment=HANGRIX_RUNNER_STATE_DIR=$SERVICE_STATE_DIR
Restart=on-failure
RestartSec=5
# Hangrix runner needs the docker socket to spawn agent containers; we
# don't sandbox it further here. Tighten via override.conf as appropriate.

[Install]
WantedBy=multi-user.target
EOSVC

  systemctl daemon-reload
  systemctl enable --now hangrix-runner.service
  echo "==> hangrix-runner.service enabled and started"
  echo
  echo "Logs:   journalctl -u hangrix-runner -f"
  echo "Status: systemctl status hangrix-runner"
else
  echo "==> enrolling against $SERVER"
  "$BIN" enroll --server "$SERVER" --token "$TOKEN"

  cat <<EOF

Runner installed and enrolled.

Start it manually with:
  $BIN serve

EOF

  if [ "$SKIP_SERVICE" = "0" ] && [ "$IS_ROOT" = "0" ] && command -v systemctl >/dev/null 2>&1; then
    echo "(tip) systemd is available on this host — re-run this installer with sudo to install hangrix-runner.service automatically."
  fi
fi
`

// serveInstallScript renders the install script with the server's public
// base URL templated in. Anonymous: this is the one-shot bootstrap a new
// runner host runs to get the binary onto disk.
func (h *AgentHandler) serveInstallScript(w http.ResponseWriter, r *http.Request) {
	base := h.publicBase(r)
	body := fmt.Sprintf(installScriptTemplate, base)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// serveInstallBinary streams the embedded runner binary. Anonymous on
// purpose — the install script needs to fetch it before the runner has
// any token. The binary itself contains no secrets.
func (h *AgentHandler) serveInstallBinary(w http.ResponseWriter, _ *http.Request) {
	info, err := binaries.Get(binaries.NameRunner)
	if err != nil {
		http.Error(w, "hangrix-runner binary not embedded in this build", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="hangrix-runner"`)
	w.Header().Set("X-Hangrix-SHA256", info.SHA256)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size))
	_, _ = w.Write(info.Bytes)
}
