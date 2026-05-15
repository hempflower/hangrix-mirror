// Package handler implements the OpenAI-Response-API-compatible HTTP
// proxy mounted at /api/llm/v1/responses.
//
// Architecture:
//
//   - This file owns the HTTP + wire-format boundary: bearer-token auth,
//     body bounding, model→provider routing, key decryption, parsing the
//     inbound Responses-API JSON into a typed upstream.Request,
//     dispatching through upstream.Provider.Respond, marshalling the
//     typed Response back as Responses-API JSON, and writing the usage
//     log row.
//
//   - Per-vendor logic (URL shaping, request/response translation,
//     reasoning effort mapping, usage extraction) lives behind
//     upstream.Provider in the sibling upstream package. The handler
//     dispatches via the Registry; adding a new vendor is one new
//     Provider implementation, no edits here.
//
// Auth model:
//
//   - No session cookies, no CSRF, no RequireAuth: every request
//     carries an `Authorization: Bearer hgxs_<prefix>_<secret>` session
//     token. The bearerAuth middleware resolves it via
//     runner/domain.SessionTokenValidator and stores the agent_session
//     on the request context. Failures map to 401 (missing/malformed
//     header) or 403 (token invalid / inactive).
//
//   - The session token is the in-container agent's identity. It is NOT
//     bound to a specific provider — the proxy resolves the upstream by
//     scanning every registered provider's allowed_models for the
//     request body's `model` field.
//
// Scope:
//
//   - Only POST /v1/responses is supported. Other paths (/v1/embeddings,
//     /v1/audio/*, /v1/files/*) need their own typed adapters; they
//     return 404 until then.
//
//   - Streaming responses are not supported (`stream:true` → 501). A
//     typed Response can't represent a partial token stream; SSE will
//     be re-introduced when there's a real consumer.
//
// Every request — success or failure — writes one row to llm_usage_log
// via llm_provider/domain.Lookup.RecordUsage. Logging failures never
// break a working upstream call (best-effort, swallowed).
package handler

import (
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
	llmdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/upstream"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// maxRequestBody bounds the buffered request body so a hostile caller
// cannot OOM the server by streaming a multi-gigabyte JSON object.
// 4 MiB comfortably fits a long conversation plus a few function-tool
// schemas; anything larger is rejected with 413.
const maxRequestBody = 4 << 20

// upstreamTimeout is the per-request timeout for upstream calls. With
// streaming removed, one timeout covers everything.
const upstreamTimeout = 5 * time.Minute

type ctxKey int

const (
	ctxKeySession ctxKey = iota
)

// Handler implements server.RouteProvider for the proxy.
type Handler struct {
	lookup    llmdomain.Lookup
	validator runnerdomain.SessionTokenValidator
	registry  *upstream.Registry
	box       *cryptobox.Box
	client    *http.Client
}

type HandlerDeps struct {
	Lookup    llmdomain.Lookup
	Validator runnerdomain.SessionTokenValidator
	Registry  *upstream.Registry
	Config    *config.Config
}

func NewHandler(deps *HandlerDeps) *Handler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("llm_proxy cryptobox: %w", err))
	}
	return &Handler{
		lookup:    deps.Lookup,
		validator: deps.Validator,
		registry:  deps.Registry,
		box:       box,
		client:    &http.Client{Timeout: upstreamTimeout},
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/llm/v1", func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Post("/responses", h.respond)
	})
}

// bearerAuth resolves the Authorization header into the calling
// agent_session and stores it on the request context. 401 on
// missing/malformed header, 403 on token invalid/inactive.
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
		sess, err := h.validator.ValidateSessionToken(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, runnerdomain.ErrInvalidSessionToken):
				writeError(w, http.StatusForbidden, "invalid session token")
			case errors.Is(err, runnerdomain.ErrSessionTokenInactive):
				writeError(w, http.StatusForbidden, "session token revoked or session terminated")
			default:
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeySession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// respond is the entry point for every authenticated request. Linear
// flow: validate, parse, resolve provider by model, dispatch, marshal,
// log usage.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sess, ok := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	if !ok || sess == nil {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}

	// (1) Buffer and parse the body into a typed Request.
	body, err := readBoundedBody(r.Body)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	upReq, stream, err := upstream.ParseResponsesAPIRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if stream {
		writeError(w, http.StatusNotImplemented, "streaming not supported by this proxy")
		h.recordUsage(r.Context(), sess, 0, upReq.Model, upstream.Usage{}, http.StatusNotImplemented, "stream not supported", r.URL.Path, time.Since(start))
		return
	}
	if upReq.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	// (2) Resolve model → provider. No provider lists the model → 404.
	prov, err := h.lookup.FindProviderByModel(r.Context(), upReq.Model)
	if err != nil {
		if errors.Is(err, llmdomain.ErrNoModelMatch) {
			msg := fmt.Sprintf("no provider serves model %q", upReq.Model)
			writeError(w, http.StatusNotFound, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// (3) Look up the upstream adapter. Unregistered type → 501.
	adapter, ok := h.registry.Lookup(prov.Type)
	if !ok {
		msg := fmt.Sprintf("unsupported provider type: %s", prov.Type)
		writeError(w, http.StatusNotImplemented, msg)
		h.recordUsage(r.Context(), sess, prov.ID, upReq.Model, upstream.Usage{}, http.StatusNotImplemented, msg, r.URL.Path, time.Since(start))
		return
	}

	// (4) Decrypt the sealed api key once per request.
	apiKey, err := h.box.Decrypt(prov.ApiKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt provider key")
		return
	}

	// (5) Fill in connection params and dispatch.
	upReq.APIKey = apiKey
	upReq.BaseURL = prov.BaseURL
	upReq.Client = h.client

	upResp, dispatchErr := adapter.Respond(r.Context(), upReq)
	if dispatchErr != nil {
		status, msg := dispatchStatusFor(dispatchErr)
		writeError(w, status, msg)
		h.recordUsage(r.Context(), sess, prov.ID, upReq.Model, upstream.Usage{}, int32(status), msg, r.URL.Path, time.Since(start))
		return
	}

	// (6) Marshal the typed Response back into Responses-API JSON.
	outBody, err := upstream.MarshalResponsesAPIResponse(upResp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode response")
		h.recordUsage(r.Context(), sess, prov.ID, upReq.Model, upResp.Usage, http.StatusInternalServerError, err.Error(), r.URL.Path, time.Since(start))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(outBody); err != nil {
		h.recordUsage(r.Context(), sess, prov.ID, upReq.Model, upResp.Usage, http.StatusOK, err.Error(), r.URL.Path, time.Since(start))
		return
	}

	h.recordUsage(r.Context(), sess, prov.ID, upReq.Model, upResp.Usage,
		http.StatusOK, "", r.URL.Path, time.Since(start))
}

// dispatchStatusFor maps adapter-level errors onto HTTP statuses + a
// user-facing message. UpstreamError surfaces the upstream's own
// status; sentinel errors map to 501/500; everything else is 502.
func dispatchStatusFor(err error) (int, string) {
	var ue *upstream.UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode, ue.Message
	}
	switch {
	case errors.Is(err, upstream.ErrStreamingUnsupported):
		return http.StatusNotImplemented, err.Error()
	case errors.Is(err, upstream.ErrBaseURLRequired):
		return http.StatusInternalServerError, err.Error()
	default:
		return http.StatusBadGateway, err.Error()
	}
}

// recordUsage writes one usage row. We log + swallow on failure so a
// transient DB hiccup never breaks a working API call. Synchronous
// (not in a goroutine) so the row is visible by the time the response
// returns; the call is tiny and not on the hot path.
func (h *Handler) recordUsage(
	ctx context.Context,
	sess *runnerdomain.AgentSession,
	providerID int64,
	model string,
	u upstream.Usage,
	status int32,
	errMessage string,
	path string,
	latency time.Duration,
) {
	rec := &llmdomain.UsageRecord{
		ProviderID:       providerID,
		Model:            model,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		LatencyMS:        int32(latency.Milliseconds()),
		StatusCode:       status,
		ErrorMessage:     errMessage,
		RequestPath:      path,
	}
	if sess != nil {
		rec.SessionID = &sess.ID
	}
	_ = h.lookup.RecordUsage(ctx, rec)
}

// ---- body helpers ----

var errBodyTooLarge = errors.New("request body exceeds limit")

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

// writeError emits a compact JSON error. Matches the shape used by the
// admin handler so frontend code doesn't need a second renderer.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
