package httpserver

import (
	"net/http"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
)

func NewServer(cfg config.HTTP, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Bind,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}
