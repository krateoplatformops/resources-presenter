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

	xcontext "github.com/krateoplatformops/plumbing/context"
	"github.com/krateoplatformops/plumbing/kubeutil/rbac"
	"github.com/krateoplatformops/plumbing/pgutil"
	"github.com/krateoplatformops/plumbing/server/probes"
	"github.com/krateoplatformops/plumbing/server/use"
	"github.com/krateoplatformops/plumbing/server/use/cors"
	"github.com/krateoplatformops/resources-presenter/internal/config"
	"github.com/krateoplatformops/resources-presenter/internal/handlers"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// rbacAuthorizer implements handlers.Authorizer using plumbing's rbac.UserCan.
type rbacAuthorizer struct{}

func (rbacAuthorizer) CanGet(ctx context.Context, group, resource, namespace string) bool {
	// Extract the user endpoint from context (set by use.UserConfig middleware).
	ep, err := xcontext.UserConfig(ctx)
	if err != nil {
		return false
	}

	return rbac.UserCan(ctx, rbac.UserCanOptions{
		UserConfig:    ep,
		Verb:          "get",
		GroupResource: schema.GroupResource{Group: group, Resource: resource},
		Namespace:     namespace,
	})
}

func main() {
	cfg := config.Setup()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pgCtx, cancel := context.WithTimeout(rootCtx, cfg.DbReadyTimeout)
	defer cancel()

	pool, err := pgutil.WaitForPostgres(pgCtx, cfg.Log, cfg.DbURL)
	if err != nil {
		cfg.Log.Error("cannot connect to PostgreSQL", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()
	cfg.Log.Info("PostgreSQL is ready")

	// HTTP server
	mux := http.NewServeMux()
	probes.Register(mux, cfg.Log, pool, time.Second)

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
	)

	// Authenticated routes: append use.UserConfig so the handler can extract
	// the user's Endpoint from context for RBAC checks.
	authChain := chain.Append(use.UserConfig(cfg.SigningKey, cfg.AuthnNS))

	auth := rbacAuthorizer{}
	mux.Handle("/resources", authChain.Then(handlers.ResourcesHandler(pool, cfg.Log, auth)))

	server := &http.Server{
		Addr:         ":" + strconv.Itoa(cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		cfg.Log.Info("starting HTTP server", slog.Int("port", cfg.Port))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	cfg.Log.Info("application is ready")

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
