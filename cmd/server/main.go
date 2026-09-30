package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erikturner/challenge/internal/api"
	"github.com/erikturner/challenge/internal/kube"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	apiAddr := ":8080"
	healthAddr := ":8081"
	namespace := "default"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := kube.New(ctx, namespace)
	if err != nil {
		logger.Error("create kubernetes client", "error", err)
		os.Exit(1)
	}

	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           api.NewHealthHandler(client),
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		logger.Info("health server listening", "address", healthAddr)

		if err := healthServer.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server failed", "error", err)
			stop()
		}
	}()

	if err := client.WaitForSync(ctx); err != nil {
		logger.Error("wait for informer cache sync", "error", err)
		stop()
	}

	apiServer := &http.Server{
		Addr:              apiAddr,
		Handler:           api.New(client, logger),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("API server listening", "address", apiAddr, "namespace", namespace)

		if err := apiServer.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			logger.Error("API server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("health server shutdown", "error", err)
	}

	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("API server shutdown", "error", err)
	}
}
