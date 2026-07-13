package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/logging"
)

var errDatabaseNotConfigured = errors.New("database_not_configured")

type pendingDatabaseReadiness struct{}

func (pendingDatabaseReadiness) Check(context.Context) error {
	return errDatabaseNotConfigured
}

func main() {
	baseLogger := logging.NewJSON(os.Stdout, slog.LevelInfo)
	logger := baseLogger.With(slog.String("component", "control-plane"))
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		logger.Error("configuration_invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("configuration_loaded", slog.Any("config", cfg))

	handler := httpserver.NewRouter(httpserver.Dependencies{
		Logger:    baseLogger,
		Readiness: pendingDatabaseReadiness{},
	})
	server := httpserver.NewServer(cfg.HTTP, handler)

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverError := make(chan error, 1)
	go func() {
		logger.Info("http_server_starting", slog.String("bind", cfg.HTTP.Bind))
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http_server_failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	case <-signalContext.Done():
		logger.Info("shutdown_started")
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("shutdown_failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		logger.Info("shutdown_complete")
	}
}
