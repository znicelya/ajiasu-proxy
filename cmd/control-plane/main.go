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

func main() {
	if handled, exitCode := runComposeCLI(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); handled {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	if handled, exitCode := runLifecycleCLI(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); handled {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	if handled, exitCode := runAdminCLI(os.Args[1:], os.LookupEnv, os.Stdin, os.Stdout, os.Stderr); handled {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	baseLogger := logging.NewJSON(os.Stdout, slog.LevelInfo)
	logger := baseLogger.With(slog.String("component", "control-plane"))
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		logger.Error("configuration_invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("configuration_loaded", slog.Any("config", cfg))

	switcher := &switchingHandler{}
	runtime := newApplicationRuntime(cfg, baseLogger, switcher.Store)
	switcher.Store(httpserver.NewRouter(httpserver.Dependencies{Logger: baseLogger, Readiness: runtime}))
	server := httpserver.NewServer(cfg.HTTP, switcher)

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer runtime.Close()
	go runtime.Warm(signalContext)

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
