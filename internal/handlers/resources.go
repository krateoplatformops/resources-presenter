package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krateoplatformops/plumbing/http/response"
	"github.com/krateoplatformops/resources-proxy/internal/access"
	"github.com/krateoplatformops/resources-proxy/internal/registry"
	"github.com/krateoplatformops/resources-proxy/internal/sql"
)

const (
	defaultLimit = 100
	maxLimit     = 5000
	queryTimeout = 10 * time.Second
)

// uuidRegex validates UUID format (RFC 4122).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
			resourceKind  string
			statusCode    int
			rowsReturned  int
			parseDuration time.Duration
			queryDuration time.Duration
			serDuration   time.Duration
			queryErr      error
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
			response.Encode(w, response.New(parseErr.status, fmt.Errorf("%s", parseErr.msg)))
			return
		}

		// --- Phase 2: DB query execution ---
		queryStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		result, err := sql.ListResources(queryCtx, db, params, policy)
		queryDuration = time.Since(queryStart)
		if err != nil {
			queryErr = err
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// --- Phase 3: Response serialization ---
		// Pre-marshal to a buffer to avoid committing a 200 status with a
		// truncated body if serialization fails partway through.
		serializeStart := time.Now()

		data, err := json.Marshal(result)
		if err != nil {
			queryErr = fmt.Errorf("serialize: %w", err)
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("response serialization failed"))
			serDuration = time.Since(serializeStart)
			return
		}

		statusCode = http.StatusOK
		rowsReturned = len(result.Items)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)

		serDuration = time.Since(serializeStart)
	}
}

// handlerError carries an HTTP status and message for parse-phase errors.
type handlerError struct {
	status int
	msg    string
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
			msg:    fmt.Sprintf("unknown resource kind %q; available: %v", resourceKind, reg.ShortNames()),
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

	compositionID := q.Get("composition_id")
	if compositionID != "" && !uuidRegex.MatchString(compositionID) {
		return sql.ListParams{}, fmt.Errorf("invalid composition_id: not a valid UUID")
	}

	// Design decision: the spec suggested ?after_updated_at=<ts>&after_id=<id> as
	// explicit cursor fields. We use an opaque ?cursor=<base64> token instead.
	// Benefits: cleaner API surface, no leaked internal column names, easier to
	// evolve the cursor format without breaking clients.
	p := sql.ListParams{
		ResourceKind:  resourceKind,
		Cluster:       q.Get("cluster"),
		Namespace:     q.Get("namespace"),
		CompositionID: compositionID,
		Raw:           q.Get("raw") == "true",
		Limit:         limit,
		Cursor:        sql.EncodedCursor(q.Get("cursor")),
	}

	return p, nil
}
