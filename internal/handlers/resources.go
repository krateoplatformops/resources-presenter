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
// resource_kind can be a short name (e.g. "panels") or the full apiVersion:Kind
// format (e.g. "widgets.templates.krateo.io/v1beta1:Panel"). Resolution is done
// via the embedded resource registry; unknown kinds return 404.
//
// Design decision: an empty result set returns 200 with an empty items array,
// not 404. This is consistent with Kubernetes LIST semantics — the resource kind
// is valid, there are just no instances matching the filters.
func ResourcesHandler(db *pgxpool.Pool, log *slog.Logger, reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		totalStart := time.Now()

		// Per-request state for the deferred log.
		var (
			resourceKind   string
			statusCode     int
			rowsReturned   int
			parseDuration  time.Duration
			queryDuration  time.Duration
			serDuration    time.Duration
			queryErr       error
		)

		// Every request gets logged — successes and errors alike.
		defer func() {
			totalDuration := time.Since(totalStart)
			lvl := slog.LevelInfo
			if statusCode >= 500 {
				lvl = slog.LevelError
			} else if statusCode >= 400 {
				lvl = slog.LevelWarn
			}

			attrs := []slog.Attr{
				slog.String("handler", "resources"),
				slog.String("resource_kind", resourceKind),
				slog.Int("status_code", statusCode),
				slog.Int("rows_returned", rowsReturned),
				slog.Group("duration_ms",
					slog.Float64("1_parse", float64(parseDuration.Microseconds())/1000.0),
					slog.Float64("2_query", float64(queryDuration.Microseconds())/1000.0),
					slog.Float64("3_serialize", float64(serDuration.Microseconds())/1000.0),
					slog.Float64("4_total", float64(totalDuration.Microseconds())/1000.0),
				),
			}
			if queryErr != nil {
				attrs = append(attrs, slog.Any("err", queryErr))
			}

			log.LogAttrs(r.Context(), lvl, "request completed", attrs...)
		}()

		// --- Phase 1: Request parsing / validation ---
		parseStart := time.Now()

		params, policy, parseErr := parseRequest(r, reg)
		parseDuration = time.Since(parseStart)
		resourceKind = strings.TrimPrefix(r.URL.Path, "/resources/")

		if parseErr != nil {
			statusCode = parseErr.status
			if parseErr.extra != nil {
				writeJSON(w, statusCode, parseErr.extra)
			} else {
				writeJSONError(w, statusCode, parseErr.msg)
			}
			return
		}

		// --- Phase 2: DB query execution ---
		queryStart := time.Now()

		result, err := sql.ListResources(r.Context(), db, params, policy)
		queryDuration = time.Since(queryStart)
		if err != nil {
			queryErr = err
			statusCode = http.StatusInternalServerError
			writeJSONError(w, statusCode, "internal server error")
			return
		}

		// --- Phase 3: Response serialization ---
		serializeStart := time.Now()

		statusCode = http.StatusOK
		rowsReturned = len(result.Items)
		w.Header().Set("Content-Type", "application/json")
		// Serialization errors are rare (broken connection). Already committed
		// the 200 status at this point, so just let the defer log it.
		if err := json.NewEncoder(w).Encode(result); err != nil {
			queryErr = fmt.Errorf("serialize: %w", err)
		}

		serDuration = time.Since(serializeStart)
	}
}

// handlerError carries an HTTP status and message for parse-phase errors.
type handlerError struct {
	status int
	msg    string
	extra  map[string]any // if non-nil, used instead of {"error": msg}
}

// parseRequest validates the URL, resolves the resource kind, and extracts query params.
// Returns (params, policy, nil) on success, or (zero, nil, *handlerError) on failure.
func parseRequest(r *http.Request, reg *registry.Registry) (sql.ListParams, *access.Policy, *handlerError) {
	resourceKind := strings.TrimPrefix(r.URL.Path, "/resources/")
	if resourceKind == "" {
		return sql.ListParams{}, nil, &handlerError{
			status: http.StatusBadRequest,
			msg:    "resource_kind is required in the URL path",
		}
	}

	def, ok := reg.Resolve(resourceKind)
	if !ok {
		return sql.ListParams{}, nil, &handlerError{
			status: http.StatusNotFound,
			extra: map[string]any{
				"error":     fmt.Sprintf("unknown resource kind %q", resourceKind),
				"available": reg.ShortNames(),
			},
		}
	}

	params, err := parseListParams(r, def.DBKind)
	if err != nil {
		return sql.ListParams{}, nil, &handlerError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid parameters: %v", err),
		}
	}

	// TODO(auth): populate from verified JWT/session claims.
	policy := access.PolicyFromRequest(r)

	return params, policy, nil
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

// writeJSON writes an arbitrary value as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// writeJSONError writes a JSON error response: {"error": "message"}.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
