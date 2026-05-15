package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFetchMarkdown spins up a fake HTTP server, fetches a small HTML
// page through the public Call() surface, and asserts the markdown
// response preserves the structural bits the LLM relies on (headings,
// links, fenced code) while dropping noise (script/style content).
// We exercise the tool end-to-end so a future swap of the converter
// library can't silently regress the contract.
func TestWebFetchMarkdown(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html>
<head>
  <style>.a{color:red}</style>
  <script>alert("nope")</script>
</head>
<body>
  <h1>Title</h1>
  <p>Paragraph with a <a href="https://example.com">link</a>.</p>
  <pre><code>println("hi")</code></pre>
</body>
</html>`))
	}))
	defer srv.Close()

	tool := newWebFetchTool()
	raw, err := tool.Call(context.Background(), mustRawJSON(map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	resJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Markdown        string `json:"markdown"`
		Body            string `json:"body"`
		ConversionError string `json:"conversion_error"`
	}
	if err := json.Unmarshal(resJSON, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ConversionError != "" {
		t.Fatalf("conversion errored: %s", got.ConversionError)
	}
	if got.Markdown == "" {
		t.Fatalf("expected markdown content, got empty (body=%q)", got.Body)
	}
	for _, want := range []string{"# Title", "[link](https://example.com)", "println(\"hi\")"} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("markdown missing %q\n---got---\n%s", want, got.Markdown)
		}
	}
	for _, absent := range []string{"alert(", "color:red"} {
		if strings.Contains(got.Markdown, absent) {
			t.Errorf("markdown should not contain %q\n---got---\n%s", absent, got.Markdown)
		}
	}
}

func mustRawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
