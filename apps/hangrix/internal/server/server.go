package server

import (
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

type Server struct {
	addr   string
	router http.Handler
}

type ServerDeps struct {
	Config    *config.Config
	Providers []RouteProvider
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
	}
}

func (s *Server) ListenAndServe() error {
	log.Printf("hangrix listening on %s", s.addr)
	return http.ListenAndServe(s.addr, s.router)
}
