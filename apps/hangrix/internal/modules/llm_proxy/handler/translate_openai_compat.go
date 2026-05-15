package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// forwardOpenAICompat is identical on the wire to forwardOpenAI but
// rejects an empty BaseURL. openai-compat means "OpenAI wire format
// pointed at OpenRouter / vLLM / Together / Groq" — defaulting to
// api.openai.com would silently send platform traffic to OpenAI when an
// operator forgot to fill the column in. Surfaced as errBaseURLRequired
// so the handler maps it to 500 (server misconfiguration, not a caller
// problem).
func forwardOpenAICompat(
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
		return nil, errBaseURLRequired
	}
	return doOpenAIStyleRequest(ctx, client, base, apiKey, "Bearer", r, body, providerName, nil)
}
