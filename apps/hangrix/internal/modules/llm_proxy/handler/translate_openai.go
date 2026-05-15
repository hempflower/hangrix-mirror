package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// defaultOpenAIBaseURL is the canonical OpenAI host. Used when a provider
// row of type `openai` has an empty BaseURL — operators rarely override
// it, but the column is kept settable for staging fixtures and tunnels.
const defaultOpenAIBaseURL = "https://api.openai.com"

// errBaseURLRequired is surfaced by openai-compat when a provider row is
// missing its BaseURL. The handler maps this to 500 because the provider
// was admin-registered without a required field — not a caller problem.
var errBaseURLRequired = errors.New("provider base_url is required for openai-compat")

// safeRequestHeaders names headers we want to pass through to the upstream
// from the caller. We deliberately drop Authorization (replaced with the
// decrypted upstream key), Cookie (callers should never send one to /api/llm,
// but defense in depth), and the chi-internal X-Forwarded-* headers — those
// would confuse upstream rate limiters that key off remote IP.
var safeRequestHeaders = map[string]struct{}{
	"Accept":          {},
	"Accept-Encoding": {},
	"Content-Type":    {},
	"User-Agent":      {},
	"X-Request-Id":    {},
}

// forwardOpenAI proxies an OpenAI Response API call to the configured
// upstream (defaulting to api.openai.com when BaseURL is empty). The
// caller has already buffered the body and verified the model is allowed;
// we only handle the transport. Returns the live response for the handler
// to drain — caller closes Body.
func forwardOpenAI(
	ctx context.Context,
	client *http.Client,
	provider *domain.Provider,
	apiKey string,
	r *http.Request,
	body []byte,
	providerName string,
) (*http.Response, error) {
	base := strings.TrimRight(provider.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	return doOpenAIStyleRequest(ctx, client, base, apiKey, "Bearer", r, body, providerName, nil)
}

// doOpenAIStyleRequest is the shared transport for both openai and
// openai-compat. extraHeaders is used by the anthropic translator (which
// also routes through here for the request half once it has rewritten the
// body) but is otherwise nil. authScheme is "Bearer" for OpenAI and
// blank-string for x-api-key — kept generic so a future provider type
// can reuse this.
func doOpenAIStyleRequest(
	ctx context.Context,
	client *http.Client,
	base, apiKey, authScheme string,
	r *http.Request,
	body []byte,
	providerName string,
	extraHeaders map[string]string,
) (*http.Response, error) {
	tail := upstreamSuffix(r.URL.Path, providerName)
	if tail == "" {
		tail = "/"
	}
	url := base + tail
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, url, newBodyReader(body))
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	for name := range safeRequestHeaders {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if authScheme != "" {
		req.Header.Set("Authorization", authScheme+" "+apiKey)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	// http.Client follows the documented behaviour: set ContentLength so
	// the upstream sees a Content-Length header and doesn't switch to
	// chunked encoding for what is in fact a fully-buffered body.
	req.ContentLength = int64(len(body))
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))

	return client.Do(req)
}
