package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

//go:embed all:dist
var distFS embed.FS

const placeholder = `<!doctype html><meta charset="utf-8"><title>hangrix</title>` +
	`<style>body{font-family:system-ui;padding:2rem;max-width:40rem;margin:auto}</style>` +
	`<h1>hangrix</h1>` +
	`<p>Frontend not bundled into this binary. Build it with:</p>` +
	`<pre><code>pnpm --filter web generate &amp;&amp; pnpm --filter hangrix build</code></pre>`

type SPAHandler struct {
	fsys       fs.FS
	fileServer http.Handler
	hasIndex   bool
}

func NewSPAHandler() *SPAHandler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	_, statErr := fs.Stat(sub, "index.html")
	return &SPAHandler{
		fsys:       sub,
		fileServer: http.FileServer(http.FS(sub)),
		hasIndex:   statErr == nil,
	}
}

func (h *SPAHandler) RegisterRoutes(r chi.Router) {
	r.Handle("/*", h)
}

func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.hasIndex {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholder))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path != "" {
		if f, err := h.fsys.Open(path); err == nil {
			f.Close()
			h.fileServer.ServeHTTP(w, r)
			return
		}
	}
	r.URL.Path = "/"
	h.fileServer.ServeHTTP(w, r)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewSPAHandler).ToInterface(new(server.RouteProvider))
	return m
}
