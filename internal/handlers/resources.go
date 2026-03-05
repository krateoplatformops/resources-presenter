package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krateoplatformops/resources-proxy/internal/access"
	"github.com/krateoplatformops/resources-proxy/internal/registry"
	"github.com/krateoplatformops/resources-proxy/internal/sql"
)

const (
	defaultLimit = 100
	maxLimit     = 5000
)

// ResourcesHandler returns an HTTP handler for GET /resources/{resource_kind}.
// The resource_kind is extracted from the URL path after "/resources/".
//
// Design decision: resource_kind must be provided in the full DB format
// (e.g. "widgets.templates.krateo.io/v1beta1:Panel").
// This avoids maintaining a short-name mapping registry and keeps the service
// stateless. Clients must URL-encode the path segment if it contains special characters.
//
// Design decision: an empty result set returns 200 with an empty items array,
// not 404. This is consistent with Kubernetes LIST semantics — the resource kind
// is valid, there are just no instances matching the filters.
func ResourcesHandler(db *pgxpool.Pool, log *slog.Logger, reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		totalStart := time.Now()

		// --- Phase 1: Request parsing / validation ---
		parseStart := time.Now()

		resourceKind := strings.TrimPrefix(r.URL.Path, "/resources/")
		if resourceKind == "" {
			http.Error(w, "resource_kind is required in the URL path", http.StatusBadRequest)
			return
		}

		// Resolve short name (e.g. "panels") or full format (e.g. "widgets.templates.krateo.io/v1beta1:Panel")
		// to the canonical DB format. Only resources listed in resources.yaml are served.
		def, ok := reg.Resolve(resourceKind)
		if !ok {
			http.Error(w,
				fmt.Sprintf("unknown resource kind %q; available: %v", resourceKind, reg.ShortNames()),
				http.StatusNotFound,
			)
			return
		}
		// log both the requested resource kind and the resolved DBKind for observability.
		log.Debug("handling request",
			slog.String("handler", "resources"),
			slog.String("requested_resource_kind", resourceKind),
			slog.String("resolved_db_kind", def.DBKind),
		)

		params, err := parseListParams(r, def.DBKind)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid parameters: %v", err), http.StatusBadRequest)
			return
		}

		// TODO(auth): populate from verified JWT/session claims.
		policy := access.PolicyFromRequest(r)

		parseDuration := time.Since(parseStart)

		// --- Phase 2: DB query execution ---
		queryStart := time.Now()

		result, err := sql.ListResources(r.Context(), db, params, policy)
		if err != nil {
			log.Error("query failed",
				slog.String("handler", "resources"),
				slog.String("resource_kind", resourceKind),
				slog.Any("err", err),
			)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		queryDuration := time.Since(queryStart)

		// --- Phase 3: Response serialization ---
		serializeStart := time.Now()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			log.Error("response serialization failed",
				slog.String("handler", "resources"),
				slog.String("resource_kind", resourceKind),
				slog.Any("err", err),
			)
			return
		}

		serializeDuration := time.Since(serializeStart)
		totalDuration := time.Since(totalStart)

		// --- Structured latency log ---
		log.Info("request completed",
			slog.String("handler", "resources"),
			slog.String("resource_kind", resourceKind),
			slog.Float64("parse_ms", float64(parseDuration.Microseconds())/1000.0),
			slog.Float64("query_duration_ms", float64(queryDuration.Microseconds())/1000.0),
			slog.Float64("serialize_ms", float64(serializeDuration.Microseconds())/1000.0),
			slog.Float64("total_duration_ms", float64(totalDuration.Microseconds())/1000.0),
			slog.Int("rows_returned", len(result.Items)),
			slog.Int("status_code", http.StatusOK),
		)
	}
}

func parseListParams(r *http.Request, resourceKind string) (sql.ListParams, error) {
	q := r.URL.Query()

	limit := defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return sql.ListParams{}, fmt.Errorf("invalid limit: %w", err)
		}
		limit = n
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	p := sql.ListParams{
		ResourceKind:  resourceKind,
		Cluster:       q.Get("cluster"),
		Namespace:     q.Get("namespace"),
		CompositionID: q.Get("composition_id"),
		Raw:           q.Get("raw") == "true",
		Limit:         limit,
		Cursor:        sql.EncodedCursor(q.Get("cursor")),
	}

	return p, nil
}
