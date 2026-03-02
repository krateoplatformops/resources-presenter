package probes

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func New(pool *pgxpool.Pool) *Probes {
	return &Probes{
		dbpool: pool,
		ready:  false,
	}
}

type Probes struct {
	mu              sync.RWMutex
	ready           bool
	dbpool          *pgxpool.Pool
	shutdownStarted bool
}

func (h *Probes) SetReady(ready bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = ready
}

func (h *Probes) IsReady() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ready && !h.shutdownStarted
}

func (h *Probes) SetShutdownStarted() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdownStarted = true
}

func (h *Probes) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		shuttingDown := h.shutdownStarted
		h.mu.RUnlock()

		if shuttingDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("shutting down"))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

func (h *Probes) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.IsReady() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := h.dbpool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("database unavailable"))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	}
}
