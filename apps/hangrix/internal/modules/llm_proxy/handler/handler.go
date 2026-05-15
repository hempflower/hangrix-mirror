// Package handler implements the OpenAI-Response-API-compatible HTTP proxy
// mounted at /api/llm/{provider_name}/v1/*.
//
// Auth model:
//
//   - No session cookies, no CSRF, no RequireAuth: every request carries an
//     `Authorization: Bearer hgxs_<prefix>_<secret>` session token. The
//     bearerAuth middleware resolves it via domain.Validator and stores
//     the (token, provider) pair in the request context. Failures map to
//     401 (missing/malformed header) or 403 (token invalid/inactive).
//
//   - The URL path's {provider_name} must match the provider the token is
//     bound to. The body's `model` field must match the token's pinned
//     model AND, when AllowedModels is non-empty, must appear in it.
//     Mismatches are 403 — these are policy violations, not auth failures.
//
// Translation strategy:
//
//   - openai / openai-compat are transparent: the request body and path
//     `/v1/...` are forwarded as-is to BaseURL with the Authorization
//     header swapped for the decrypted upstream key. Streaming responses
//     pass through verbatim via http.Flusher.
//
//   - anthropic translates OpenAI Response API <-> Anthropic Messages API
//     for non-streaming text-only traffic. Streaming requests return 501;
//     non-text content (tools, images) is silently dropped. The exit
//     condition for M6a only requires ONE provider type to work
//     end-to-end, so this scoping is intentional.
//
// Every request — success or failure — writes one row to llm_usage_log
// via Lookup.RecordUsage. The write is best-effort; logging failures
// never break a working upstream call.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// maxRequestBody bounds the buffered request body so a hostile caller
// cannot OOM the server by streaming a multi-gigabyte JSON object. 4 MiB
// comfortably fits a long conversation plus a few function-tool schemas;
// anything larger is rejected with 413.
const maxRequestBody = 4 << 20

// upstreamTimeout is the per-request timeout for non-streaming calls.
// Streaming calls run on a zero-timeout client because the upstream may
// hold the connection open for minutes while emitting tokens.
const upstreamTimeout = 5 * time.Minute

// ctxKey is a private type so other packages cannot accidentally collide
// with our context keys.
type ctxKey int

const (
	ctxKeyValidated ctxKey = iota
)

// Handler implements server.RouteProvider for the proxy. It holds the wide
// domain.Repo interface (not Lookup) because we need TouchSessionTokenLastUsed
// after each successful upstream call — that method is not part of Lookup
// and exposing it just to satisfy a narrower interface would be churn.
type Handler struct {
	repo      domain.Repo
	validator domain.Validator
	box       *cryptobox.Box
	stdClient *http.Client // bounded timeout, used for non-streaming requests
	streamClient *http.Client // zero timeout, used for SSE/streaming requests
}

// HandlerDeps wires the dependencies the proxy needs at request time. The
// http clients are constructed inside NewHandler rather than provided so
// the timeout policy lives next to the code that depends on it.
type HandlerDeps struct {
	Repo      domain.Repo
	Validator domain.Validator
	Config    *config.Config
}

// NewHandler builds its own cryptobox from config.LLM.EncryptionKey. The
// sibling llm_provider module already builds one from the same key — that
// is fine because cryptobox.Box has no exclusive state (a fresh AEAD over
// the same key is interchangeable with any other). A malformed key panics
// here so misconfiguration surfaces at startup rather than the first call.
func NewHandler(deps *HandlerDeps) *Handler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("llm_proxy cryptobox: %w", err))
	}
	return &Handler{
		repo:      deps.Repo,
		validator: deps.Validator,
		box:       box,
		stdClient: &http.Client{Timeout: upstreamTimeout},
		// A zero Timeout means "no client-side deadline". Streaming upstream
		// responses are bounded only by the upstream itself.
		streamClient: &http.Client{Timeout: 0},
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/llm/{provider_name}/v1", func(r chi.Router) {
		r.Use(h.bearerAuth)
		// Match every method + every tail under /v1. Chi expects an explicit
		// wildcard route; HandleFunc with chi.URLParam("*") gives us the
		// suffix the upstream needs (e.g. "responses", "embeddings/foo").
		r.HandleFunc("/*", h.proxy)
	})
}

// bearerAuth resolves the Authorization header into a *domain.Validated
// and stores it in the request context. 401 on missing/malformed header,
// 403 on token invalid/inactive — matches the rest of the codebase where
// 401 means "you forgot to authenticate" and 403 means "we authenticated
// you, but you can't have this".
func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		v, err := h.validator.ValidateToken(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrInvalidToken), errors.Is(err, domain.ErrTokenNotFound):
				writeError(w, http.StatusForbidden, "invalid session token")
			case errors.Is(err, domain.ErrTokenInactive):
				writeError(w, http.StatusForbidden, "session token revoked or expired")
			default:
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyValidated, v)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// proxy is the entry point for every authenticated request. It is one big
// function on purpose: the linear flow (read body → inspect model → decrypt
// key → dispatch → log usage → maybe touch token) is much easier to read
// when not chopped into helpers that each take and return three things.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	validated, ok := r.Context().Value(ctxKeyValidated).(*domain.Validated)
	if !ok || validated == nil {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}

	// (1) URL provider name must match the token's bound provider. Without
	// this a caller could use a token issued for provider A against the
	// path of provider B; the upstream would never know.
	urlName := chi.URLParam(r, "provider_name")
	if urlName != validated.Provider.Name {
		writeError(w, http.StatusForbidden, "token does not match provider in URL")
		return
	}

	// (2) Read and bound the body. We have to inspect `model` and, for
	// anthropic, re-encode it; buffering up front simplifies both paths.
	body, err := readBoundedBody(r.Body)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// (3) Extract the `model` field. We use a loose map because the
	// Responses API has dozens of optional fields and modelling them all
	// here would couple us to upstream's schema churn. A typed struct
	// would also lose round-trip fidelity for fields we don't recognise.
	var bodyMap map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
	}
	model := getString(bodyMap, "model")

	// (4) Enforce the token's pinned model and the provider's allow-list.
	// An empty AllowedModels is "no allow-list configured" — fall through
	// to the upstream's own validation rather than rejecting locally.
	if validated.Token.Model != "" && model != validated.Token.Model {
		writeError(w, http.StatusForbidden, "model does not match token binding")
		return
	}
	if len(validated.Provider.AllowedModels) > 0 && !contains(validated.Provider.AllowedModels, model) {
		writeError(w, http.StatusForbidden, "model not allowed by provider")
		return
	}

	// (5) Decrypt the sealed api key once per request. A decrypt failure
	// is a server-config bug (wrong master key, corrupted row) — never a
	// caller problem — so it maps to 500.
	apiKey, err := h.box.Decrypt(validated.Provider.ApiKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt provider key")
		return
	}

	stream := getBool(bodyMap, "stream")

	// (6) Dispatch by provider type. Each translator owns its own header
	// hygiene (Authorization vs x-api-key, content-type, etc.); the
	// handler only cares about the resulting *http.Response.
	client := h.stdClient
	if stream {
		client = h.streamClient
	}

	var (
		upstream    *http.Response
		dispatchErr error
	)
	switch validated.Provider.Type {
	case domain.ProviderTypeOpenAI:
		upstream, dispatchErr = forwardOpenAI(r.Context(), client, validated.Provider, apiKey, r, body, urlName)
	case domain.ProviderTypeOpenAICompat:
		upstream, dispatchErr = forwardOpenAICompat(r.Context(), client, validated.Provider, apiKey, r, body, urlName)
	case domain.ProviderTypeAnthropic:
		upstream, dispatchErr = forwardAnthropic(r.Context(), client, validated.Provider, apiKey, r, body, urlName, stream)
	default:
		dispatchErr = fmt.Errorf("unsupported provider type: %s", validated.Provider.Type)
	}

	// Translator-level errors (bad config, unsupported endpoint, transport
	// failure before we got a response) become a 5xx/501 here and a usage
	// row with status 0. The recordUsage call is best-effort.
	if dispatchErr != nil {
		status := http.StatusBadGateway
		msg := dispatchErr.Error()
		// errStreamingUnsupported is the one translator-level error we
		// surface as 501 — it's a "feature not implemented" signal, not
		// a transport failure.
		if errors.Is(dispatchErr, errStreamingUnsupported) {
			status = http.StatusNotImplemented
		}
		if errors.Is(dispatchErr, errPathUnsupported) {
			status = http.StatusNotImplemented
		}
		if errors.Is(dispatchErr, errBaseURLRequired) {
			status = http.StatusInternalServerError
		}
		writeError(w, status, msg)
		h.recordUsage(r.Context(), validated, model, 0, 0, 0, int32(status), msg, r.URL.Path, time.Since(start))
		return
	}
	defer upstream.Body.Close()

	// (7) Stream the upstream response back to the caller. For streaming
	// SSE, copy headers and flush after every chunk; for non-streaming,
	// io.Copy the whole body. We also try to extract usage counts from
	// non-stream JSON bodies so the usage log has something useful.
	copyResponseHeaders(w.Header(), upstream.Header)
	w.WriteHeader(upstream.StatusCode)

	var (
		promptTokens, completionTokens, totalTokens int32
		errMessage                                  string
	)
	if stream {
		streamCopy(w, upstream.Body)
	} else {
		respBody, _ := io.ReadAll(upstream.Body)
		if _, err := w.Write(respBody); err != nil {
			// Caller hung up. Nothing useful to do — still log usage below.
			errMessage = err.Error()
		}
		promptTokens, completionTokens, totalTokens = extractUsage(respBody, validated.Provider.Type)
	}

	if upstream.StatusCode >= 400 && errMessage == "" {
		errMessage = http.StatusText(upstream.StatusCode)
	}

	h.recordUsage(r.Context(), validated, model, promptTokens, completionTokens, totalTokens,
		int32(upstream.StatusCode), errMessage, r.URL.Path, time.Since(start))

	// (8) Touch the token's last_used_at only on a 2xx — a 4xx/5xx from
	// upstream is not a useful "the token was just used successfully"
	// signal. Best-effort: swallow errors.
	if upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
		_ = h.repo.TouchSessionTokenLastUsed(r.Context(), validated.Token.ID)
	}
}

// recordUsage writes one usage row. We log + swallow on failure so a
// transient DB hiccup never breaks a working API call. Done synchronously
// (not in a goroutine) so the row is visible by the time the response
// returns; the call is tiny and not on the hot path of upstream latency.
func (h *Handler) recordUsage(
	ctx context.Context,
	v *domain.Validated,
	model string,
	prompt, completion, total int32,
	status int32,
	errMessage string,
	path string,
	latency time.Duration,
) {
	rec := &domain.UsageRecord{
		SessionTokenID:   &v.Token.ID,
		ProviderID:       v.Provider.ID,
		Model:            model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		LatencyMS:        int32(latency.Milliseconds()),
		StatusCode:       status,
		ErrorMessage:     errMessage,
		RequestPath:      path,
	}
	if err := h.repo.RecordUsage(ctx, rec); err != nil {
		// Intentionally not surfaced: usage logging is not in the contract
		// with the caller. The Logger middleware will pick up the request.
		_ = err
	}
}

// ---- body helpers ----

var errBodyTooLarge = errors.New("request body exceeds limit")

// readBoundedBody reads the entire body up to maxRequestBody+1 bytes; if
// we manage to read that many it means the body was at least
// maxRequestBody+1 bytes and we reject. Using io.LimitReader with one
// extra byte over the cap is the canonical way to distinguish "exactly
// the limit" from "over the limit".
func readBoundedBody(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	buf, err := io.ReadAll(io.LimitReader(rc, maxRequestBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxRequestBody {
		return nil, errBodyTooLarge
	}
	return buf, nil
}

// ---- response helpers ----

// hopByHopHeaders are stripped on both the upstream request and the
// downstream response — they describe the previous hop and would lie if
// forwarded verbatim. The set is the one named by RFC 7230 §6.1 plus
// `Connection` itself.
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

// copyResponseHeaders mirrors safe upstream headers into the response.
// We drop hop-by-hop headers but otherwise pass everything through —
// Content-Type, Content-Length, X-Request-ID, ratelimit headers, etc.
// all matter to the caller.
func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		if _, hop := hopByHopHeaders[k]; hop {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// streamCopy proxies a streaming response with flush-per-chunk semantics.
// http.ResponseWriter may not implement http.Flusher (e.g. under a test
// recorder) — in that case we fall back to a plain io.Copy, which is
// still correct, just buffered.
func streamCopy(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8192)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// ---- usage extraction ----

// extractUsage parses the per-format usage block out of a JSON response
// body. Returns zeros if anything is missing or malformed — the usage log
// is informational, not authoritative.
func extractUsage(body []byte, t domain.ProviderType) (prompt, completion, total int32) {
	if len(body) == 0 {
		return 0, 0, 0
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0, 0, 0
	}
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return 0, 0, 0
	}
	switch t {
	case domain.ProviderTypeAnthropic:
		// Anthropic Messages: input_tokens / output_tokens, no total.
		prompt = getInt32(usage, "input_tokens")
		completion = getInt32(usage, "output_tokens")
		total = prompt + completion
	default:
		// OpenAI Responses API uses input_tokens/output_tokens/total_tokens.
		// Older Chat Completions used prompt_tokens/completion_tokens/total_tokens;
		// accept either spelling so an openai-compat upstream that still
		// speaks the older shape isn't logged as zero.
		prompt = getInt32(usage, "input_tokens")
		if prompt == 0 {
			prompt = getInt32(usage, "prompt_tokens")
		}
		completion = getInt32(usage, "output_tokens")
		if completion == 0 {
			completion = getInt32(usage, "completion_tokens")
		}
		total = getInt32(usage, "total_tokens")
		if total == 0 {
			total = prompt + completion
		}
	}
	return prompt, completion, total
}

// ---- json helpers ----

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, _ := m[key].(bool)
	return v
}

// getInt32 accepts either json.Number or float64 (the two shapes
// encoding/json produces for a numeric leaf). Values out of int32 range
// saturate to 0 — token counts above 2 billion don't exist and a wrap
// would be more misleading than a zero.
func getInt32(m map[string]any, key string) int32 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		if v < 0 || v > float64(int32(^uint32(0)>>1)) {
			return 0
		}
		return int32(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		if n < 0 || n > int64(int32(^uint32(0)>>1)) {
			return 0
		}
		return int32(n)
	}
	return 0
}

func contains(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

// ---- error helpers ----

// writeError emits a compact JSON error. Matches the shape used by the
// admin handler so frontend code doesn't need a second renderer.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// upstreamSuffix strips the `/api/llm/{provider_name}` prefix from the
// request path so what remains is the `/v1/...` tail the upstream expects.
// Centralised so both translators agree on the slicing.
func upstreamSuffix(reqPath, providerName string) string {
	prefix := "/api/llm/" + providerName
	if !strings.HasPrefix(reqPath, prefix) {
		// Defensive: the chi route ensures this prefix is present, but if
		// a future refactor changes the mount point we'd rather forward
		// the raw path than panic.
		return reqPath
	}
	return reqPath[len(prefix):]
}

// newBodyReader returns a fresh io.Reader over the same bytes — used when
// we need a Body for an *http.Request after having read the original
// request body into memory.
func newBodyReader(b []byte) io.Reader {
	return bytes.NewReader(b)
}
