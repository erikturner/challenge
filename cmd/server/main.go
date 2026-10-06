package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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

	apiAddr := ":8443"
	healthAddr := ":8081"
	namespace := "default"
	certFile := envOr("TLS_CERT_FILE", "/tls/server.crt")
	keyFile := envOr("TLS_KEY_FILE", "/tls/server.key")
	caFile := envOr("TLS_CA_FILE", "/tls/ca.crt")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := kube.New(ctx, namespace)
	if err != nil {
		logger.Error("create kubernetes client", "error", err)
		os.Exit(1)
	}

	healthServer := &http.Server{
		Addr:              healthAddr,
		Handler:           api.NewHealthHandler(client, logger),
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

	syncCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := client.WaitForSync(syncCtx); err != nil {
		logger.Error("wait for informer cache sync", "error", err)
		stop()
	}

	tlsConfig, err := loadTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		logger.Error("load TLS configuration", "error", err)
		os.Exit(1)
	}

	apiServer := &http.Server{
		Addr:              apiAddr,
		Handler:           api.New(client, logger),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("mTLS API server listening", "address", apiAddr, "namespace", namespace)

		if err := apiServer.ListenAndServeTLS("", ""); err != nil &&
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

func loadTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse client CA")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
