package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// webfetch fetches a URL and, by default, converts the HTML body to
// markdown so the LLM keeps document structure (headings, lists, links,
// code blocks) instead of a flat tag-stripped blob. We delegate the
// conversion to github.com/JohannesKaufmann/html-to-markdown/v2 — it
// handles nested markup, tables, and the inline/block distinction far
// better than a hand-rolled regex pass and is the canonical Go choice
// for this job. Set raw=true to skip the conversion and get the original
// body bytes (useful for JSON, plain text, etc.).

type webfetchArgs struct {
	URL string `json:"url"`
	Raw bool   `json:"raw"`
}

type webfetchTool struct {
	http *http.Client
}

func newWebFetchTool() Tool {
	return &webfetchTool{
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Cap redirects so a hostile server can't bounce us forever.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

func (webfetchTool) Name() string { return "webfetch" }
func (webfetchTool) Description() string {
	return "Fetch a URL. By default returns the body converted to markdown (headings, lists, links, and code blocks preserved; scripts/styles stripped) under the 'markdown' field. Set raw=true to skip the conversion and get the original body bytes under 'body' instead — useful for non-HTML responses like JSON or plain text."
}
func (webfetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "Absolute http(s) URL."},
			"raw": map[string]any{"type": "boolean"},
		},
		"required": []string{"url"},
	}
}

func (w *webfetchTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var a webfetchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.URL == "" {
		return nil, errors.New("webfetch: missing required 'url' argument. Provide an absolute http(s) URL to fetch.")
	}
	if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
		return nil, fmt.Errorf("webfetch: %q is not a supported URL. Only absolute http:// and https:// URLs are allowed — relative paths and other schemes (file://, ftp://, etc.) are not fetched. To read a local file use the 'read' tool instead.", a.URL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hangrix-agent/0.1 (+webfetch)")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Cap body to 4 MiB. A page larger than that almost always carries
	// repeated boilerplate the LLM doesn't benefit from; the cap keeps
	// us from blowing up the context window in one tool call.
	const cap = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, cap+1))
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(body) > cap {
		body = body[:cap]
		truncated = true
	}

	out := map[string]any{
		"url":          a.URL,
		"status":       resp.StatusCode,
		"content_type": resp.Header.Get("Content-Type"),
		"truncated":    truncated,
	}
	if a.Raw {
		out["body"] = string(body)
		return out, nil
	}
	md, convErr := htmltomarkdown.ConvertString(string(body))
	if convErr != nil {
		// Conversion failure is rare (the library tolerates very broken
		// HTML), but if it happens we surface the original bytes so the
		// LLM still has something to work with rather than an empty result.
		out["body"] = string(body)
		out["conversion_error"] = convErr.Error()
		return out, nil
	}
	out["markdown"] = md
	return out, nil
}
