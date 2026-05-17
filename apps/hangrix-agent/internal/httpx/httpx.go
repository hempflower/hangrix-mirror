// Package httpx is a tiny seam for agent-side outbound HTTP clients.
// Its only job today: surface an env-driven `InsecureSkipVerify` knob
// in one well-known place, so the LLM proxy client and the platform
// tools client both honour the same toggle without duplicating
// transport-construction logic.
package httpx

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const insecureEnv = "HANGRIX_INSECURE_SKIP_TLS_VERIFY"

var warnOnce sync.Once

// NewClient returns an *http.Client with the given request timeout.
//
// When env var HANGRIX_INSECURE_SKIP_TLS_VERIFY is set to a truthy
// value (`1`, `true`, `yes`, `on`), the returned client's TLS
// transport runs with InsecureSkipVerify=true — the agent will
// accept any server certificate. This exists as an escape hatch
// for two real failure modes:
//
//   - Container image stripped of /etc/ssl/certs (alpine / distroless
//     / scratch base images that the operator forgot to apt-get
//     install ca-certificates into). The right fix is "fix the
//     image", but flipping this lets you keep moving while that
//     PR lands.
//   - Platform server fronted by a private CA the runner host
//     doesn't trust. Same story: the right fix is to mount the
//     CA bundle into the agent container.
//
// Loudly logs a one-time WARN when active so the operator can't miss
// that production safety is off. There is intentionally no config-
// file knob for this — it should only ever be set via the host
// yaml's `container.env` map (or HANGRIX_INSECURE_SKIP_TLS_VERIFY
// on the runner directly), so reverting is a yaml edit + agent
// restart, not a code rebuild.
func NewClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if !insecureSkip() {
		return c
	}
	warnOnce.Do(func() {
		log.Printf("WARN: %s is set — TLS verification disabled for all outbound HTTP. Do not use in production.", insecureEnv)
	})
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	} else {
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	}
	tr.TLSClientConfig.InsecureSkipVerify = true
	c.Transport = tr
	return c
}

func insecureSkip() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(insecureEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
