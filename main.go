package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/krateoplatformops/resources-proxy/internal/config"
	"github.com/krateoplatformops/resources-proxy/internal/handlers"
	"github.com/krateoplatformops/resources-proxy/internal/probes"
	pgutil "github.com/krateoplatformops/resources-proxy/internal/util/pg"
	"github.com/krateoplatformops/plumbing/server/use"
	"github.com/krateoplatformops/plumbing/server/use/cors"
)

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
	cfg.Log.Info("PostgreSQL is ready.")

	health := probes.New(pool)

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/resources/", handlers.ResourcesHandler(pool, cfg.Log))
	mux.HandleFunc("/livez", health.LivenessHandler())
	mux.HandleFunc("/readyz", health.ReadinessHandler())

	chain := use.NewChain(
		use.CORS(cors.Options{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"GET", "OPTIONS"},
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

	server := &http.Server{
		Addr:         ":" + strconv.Itoa(cfg.Port),
		Handler:      chain.Then(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Starting HTTP server on port %d", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	health.SetReady(true)
	log.Println("Application is ready")

	// --- WAIT FOR SHUTDOWN SIGNAL OR SERVER ERROR ---
	select {
	case <-rootCtx.Done():
		log.Println("Shutdown signal received")
	case err := <-serverErr:
		log.Printf("Server error: %v", err)
	}

	// --- GRACEFUL SHUTDOWN ---
	health.SetShutdownStarted()
	health.SetReady(false)
	log.Println("Starting graceful shutdown...")

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		} else {
			log.Println("HTTP server stopped gracefully")
		}
	}()

	wg.Wait()
	log.Println("Graceful shutdown complete")
}
