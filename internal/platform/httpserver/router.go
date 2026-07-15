package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Readiness interface {
	Check(context.Context) error
}

type ModuleRoutes interface {
	RegisterPublicRoutes(chi.Router)
	RegisterProtectedRoutes(chi.Router)
}

type Dependencies struct {
	Logger       *slog.Logger
	Readiness    Readiness
	Modules      []ModuleRoutes
	Authenticate func(http.Handler) http.Handler
}

func NewRouter(deps Dependencies) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	router := chi.NewRouter()
	router.Use(requestIDMiddleware)
	router.Use(clientIPMiddleware)
	router.Use(recoveryMiddleware(logger))
	router.Use(accessLogMiddleware(logger))
	router.Use(bodyLimitMiddleware)
	router.Get("/livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if deps.Readiness == nil || deps.Readiness.Check(r.Context()) != nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_ready", "service is not ready", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Route("/api/v1", func(api chi.Router) {
		for _, module := range deps.Modules {
			if module != nil {
				module.RegisterPublicRoutes(api)
			}
		}
		api.Group(func(protected chi.Router) {
			if deps.Authenticate != nil {
				protected.Use(deps.Authenticate)
			}
			for _, module := range deps.Modules {
				if module != nil {
					module.RegisterProtectedRoutes(protected)
				}
			}
		})
	})
	return router
}
