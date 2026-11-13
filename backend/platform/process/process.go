package process

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-buddy/backend/platform/config"
)

type Factory func(context.Context, config.Config) (http.Handler, func(), error)

func Static(handler http.Handler) Factory {
	return func(context.Context, config.Config) (http.Handler, func(), error) { return handler, func() {}, nil }
}

func Main(service string, factory Factory) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("configuration rejected", "error", err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	handler, closeApp, err := factory(setupCtx, cfg)
	cancel()
	if err != nil {
		slog.Error("application initialization failed")
		os.Exit(1)
	}
	defer closeApp()
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		slog.Error("HTTP listener failed")
		os.Exit(1)
	}
	slog.Info("bootstrap process starting", "service", service, "demo", cfg.DemoMode, "llm_provider", cfg.LLMProvider)
	if err := Serve(ctx, listener, handler, 5*time.Second); err != nil {
		slog.Error("HTTP server failed")
		os.Exit(1)
	}
}

// Serve owns the listener and waits for in-flight requests on cancellation.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, shutdownTimeout time.Duration) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	done := make(chan struct{})
	defer close(done)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	err := server.Serve(listener)
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}
