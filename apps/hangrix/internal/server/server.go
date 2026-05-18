package server

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
)

// RouteProvider is implemented by any module that contributes routes to the
// HTTP server. Implementations are collected via the ioc container and their
// routes are registered on the shared chi router at server construction time.
type RouteProvider interface {
	RegisterRoutes(r chi.Router)
}

// BackgroundJob is implemented by any module that needs a long-lived
// goroutine alongside the HTTP server — periodic sweepers, reapers,
// queue workers, etc. Implementations are collected by ioc and each
// gets Start called once during ListenAndServe with a context that
// stays alive for the lifetime of the server. Implementations must
// return promptly on ctx.Done() so future graceful shutdown wiring
// composes cleanly.
type BackgroundJob interface {
	Start(ctx context.Context)
}

type Server struct {
	addr   string
	router http.Handler
	jobs   []BackgroundJob
}

type ServerDeps struct {
	Config    *config.Config
	Providers []RouteProvider
	Jobs      []BackgroundJob
}

func NewServer(deps *ServerDeps) *Server {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	for _, p := range deps.Providers {
		p.RegisterRoutes(r)
	}

	return &Server{
		addr:   deps.Config.Server.Addr,
		router: r,
		jobs:   deps.Jobs,
	}
}

func (s *Server) ListenAndServe() error {
	// Background jobs run for the lifetime of the process. We hand each
	// a Background context — graceful shutdown is not yet wired into the
	// HTTP server, so plumbing a real cancel here would be misleading.
	// When that lands, swap context.Background() for the shared shutdown
	// ctx and the jobs will already honour it.
	ctx := context.Background()
	for _, j := range s.jobs {
		go j.Start(ctx)
	}
	log.Printf("hangrix listening on %s", s.addr)
	return http.ListenAndServe(s.addr, s.router)
}
