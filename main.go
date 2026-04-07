package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/krateoplatformops/plumbing/pgutil"
	"github.com/krateoplatformops/plumbing/server/probes"
	"github.com/krateoplatformops/plumbing/server/use"
	"github.com/krateoplatformops/plumbing/server/use/cors"
	"github.com/krateoplatformops/resources-presenter/internal/config"
	"github.com/krateoplatformops/resources-presenter/internal/handlers"
	"github.com/krateoplatformops/resources-presenter/internal/rbac"
	"github.com/krateoplatformops/resources-presenter/internal/telemetry"
)

func main() {
	cfg := config.Setup()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics, shutdownMetrics, err := telemetry.Setup(rootCtx, cfg.Log, telemetry.Config{
		Enabled:        cfg.OTelEnabled,
		ServiceName:    "resources-presenter",
		ExportInterval: cfg.OTelInterval,
	})
	if err != nil {
		cfg.Log.Error("OpenTelemetry setup failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownMetrics(ctx); err != nil {
			cfg.Log.Warn("OpenTelemetry shutdown failed", slog.Any("err", err))
		}
	}()

	pgCtx, cancel := context.WithTimeout(rootCtx, cfg.DbReadyTimeout)
	defer cancel()

	dbConnectStarted := time.Now()
	pool, err := pgutil.WaitForPostgres(pgCtx, cfg.Log, cfg.DbURL)
	metrics.RecordDBConnectDuration(rootCtx, time.Since(dbConnectStarted))
	if err != nil {
		metrics.IncStartupFailure(rootCtx)
		cfg.Log.Error("cannot connect to PostgreSQL", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()
	cfg.Log.Info("PostgreSQL is ready")

	// HTTP server
	mux := http.NewServeMux()

	// probePingTimeout is how long the readiness/liveness probe handler waits
	// for pool.Ping to respond. Must be lower than the Kubernetes
	// timeoutSeconds (chart default: 5s) to avoid kubelet killing the request
	// before the DB check completes.
	probePingTimeout := 3 * time.Second
	probes.Register(mux, cfg.Log, pool, probePingTimeout)

	chain := use.NewChain(
		use.TraceId(),
		use.Access(cfg.Log),
		use.CORS(cors.Options{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders: []string{
				"Accept",
				"Authorization",
				"Content-Type",
				"X-Auth-Code",
				"X-Krateo-TraceId",
			},
			ExposedHeaders:   []string{"Link"},
			AllowCredentials: true,
			MaxAge:           300,
		}),
		handlers.Gzip(),
	)

	// Authenticated routes: append use.UserConfig so the handler can extract
	// the user's Endpoint from context for RBAC checks.
	authChain := chain.Append(use.UserConfig(cfg.SigningKey, cfg.AuthnNS))

	auth := rbac.RbacAuthorizer{}
	mux.Handle("/resources", authChain.Then(handlers.ResourcesHandler(pool, cfg.Log, auth, metrics)))
	mux.Handle("GET /resources/{global_uid}", authChain.Then(handlers.ResourceDetailHandler(pool, cfg.Log, auth, metrics)))

	server := &http.Server{
		Addr:         ":" + strconv.Itoa(cfg.Port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		cfg.Log.Info("starting HTTP server", slog.Int("port", cfg.Port))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	cfg.Log.Info("application is ready")
	metrics.IncStartupSuccess(rootCtx)

	// --- WAIT FOR SHUTDOWN SIGNAL OR SERVER ERROR ---
	select {
	case <-rootCtx.Done():
		cfg.Log.Info("shutdown signal received")
	case err := <-serverErr:
		cfg.Log.Error("server error", slog.Any("err", err))
	}

	// --- GRACEFUL SHUTDOWN ---
	cfg.Log.Info("starting graceful shutdown")

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			cfg.Log.Error("HTTP server shutdown error", slog.Any("err", err))
		} else {
			cfg.Log.Info("HTTP server stopped gracefully")
		}
	}()

	wg.Wait()
	cfg.Log.Info("graceful shutdown complete")
}
