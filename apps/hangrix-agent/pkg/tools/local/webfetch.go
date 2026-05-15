package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// webfetch fetches a URL and, by default, strips HTML to a readable text
// form for the LLM. The HTML→text conversion is intentionally crude: drop
// <script> and <style> blocks, strip remaining tags, collapse whitespace.
// A real markdown converter (e.g. JinaReader) would be better but pulls
// in a heavy dep tree; the v1 LLM-friendly form is good enough for the
// "find a docs page → summarise" loop M6b needs.

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
	return "Fetch a URL. By default returns the body converted to plain text (HTML stripped). Set raw=true to get the original body bytes (useful for non-HTML responses)."
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
		return nil, errors.New("url is required")
	}
	if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
		return nil, fmt.Errorf("only http/https URLs are supported")
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
	} else {
		out["text"] = htmlToText(string(body))
	}
	return out, nil
}

var (
	// Two regexes instead of one back-referenced pattern: Go's RE2 has
	// no backreferences, so we strip <script>…</script> and
	// <style>…</style> in two passes.
	scriptBlock  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleBlock   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	htmlTag      = regexp.MustCompile(`<[^>]+>`)
	multiSpace   = regexp.MustCompile(`[ \t]+`)
	multiNewline = regexp.MustCompile(`\n{3,}`)
)

func htmlToText(s string) string {
	s = scriptBlock.ReplaceAllString(s, "")
	s = styleBlock.ReplaceAllString(s, "")
	s = htmlTag.ReplaceAllString(s, "")
	// Decode the four entities that show up in nearly every page; full
	// entity decoding would need html.UnescapeString, which is fine but
	// brings in net/html — and the four below cover the 99% case.
	s = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(s)
	s = multiSpace.ReplaceAllString(s, " ")
	s = multiNewline.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
