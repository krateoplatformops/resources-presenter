package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	xcontext "github.com/krateoplatformops/plumbing/context"
	"github.com/krateoplatformops/plumbing/http/response"
	"github.com/krateoplatformops/resources-presenter/internal/sql"
)

const (
	defaultLimit = 100
	maxLimit     = 5000
	queryTimeout = 10 * time.Second
)

// uuidRegex validates UUID format (RFC 4122).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Authorizer checks whether the current user is allowed to GET a specific
// resource type in a given namespace.
type Authorizer interface {
	CanGet(ctx context.Context, group, resource, namespace string) bool
}

// ResourcesHandler returns an HTTP handler for GET/POST /resources.
// The resource type is identified by query parameters: group, version, kind, resource.
//   - group, version, kind → combined into DB format: group/version.Kind
//   - resource → K8s plural resource name (e.g. "panels"), used for RBAC checks
//
// GET uses query parameters; POST uses a JSON body with the same fields.
//
// Design decision: an empty result set returns 200 with an empty items array,
// not 404. This is consistent with Kubernetes LIST semantics — the resource kind
// is valid, there are just no instances matching the filters.
func ResourcesHandler(db *pgxpool.Pool, log *slog.Logger, auth Authorizer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		totalStart := time.Now()

		ctx := xcontext.BuildContext(r.Context())
		traceId := xcontext.TraceId(ctx, false)

		// Per-request state for the deferred log.
		var (
			resourceKind  string
			statusCode    int
			rowsReturned  int
			parseDuration time.Duration
			rbacDuration  time.Duration
			queryDuration time.Duration
			serDuration   time.Duration
			queryErr      error
		)

		// Every request gets logged in detail, then there is also the higher-level access log due to the middleware.
		defer func() {
			totalDuration := time.Since(totalStart)
			lvl := slog.LevelDebug
			if statusCode >= 500 {
				lvl = slog.LevelError
			} else if statusCode >= 400 {
				lvl = slog.LevelWarn
			}

			attrs := []slog.Attr{
				slog.String("handler", "resources"),
				slog.String("method", r.Method),
				slog.String("resource_kind", resourceKind),
				slog.String("trace_id", traceId),
				slog.Int("status_code", statusCode),
				slog.Int("rows_returned", rowsReturned),
				slog.Group("duration_ms",
					slog.Float64("1_parse", float64(parseDuration.Microseconds())/1000.0),
					slog.Float64("2_rbac", float64(rbacDuration.Microseconds())/1000.0),
					slog.Float64("3_query", float64(queryDuration.Microseconds())/1000.0),
					slog.Float64("4_serialize", float64(serDuration.Microseconds())/1000.0),
					slog.Float64("5_total", float64(totalDuration.Microseconds())/1000.0),
				),
			}
			if queryErr != nil {
				attrs = append(attrs, slog.Any("err", queryErr))
			}

			log.LogAttrs(r.Context(), lvl, "request completed", attrs...)
		}()

		// Only GET and POST are allowed.
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			statusCode = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "GET, POST")
			response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
			return
		}

		// --- Phase 1: Request parsing / validation ---
		parseStart := time.Now()

		params, parseErr := parseRequest(r)
		parseDuration = time.Since(parseStart)
		if params.ResourceKind != "" {
			resourceKind = params.ResourceKind
		}

		if parseErr != nil {
			statusCode = parseErr.status
			response.Encode(w, response.New(parseErr.status, fmt.Errorf("%s", parseErr.msg)))
			return
		}

		// --- Phase 2: RBAC authorization ---
		rbacStart := time.Now()

		if !auth.CanGet(r.Context(), params.Group, params.Resource, params.Namespace) {
			rbacDuration = time.Since(rbacStart)
			statusCode = http.StatusForbidden
			response.Encode(w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
			return
		}
		rbacDuration = time.Since(rbacStart)

		// --- Phase 3: DB query execution ---
		queryStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		result, err := sql.ListResources(queryCtx, db, params.ListParams)
		queryDuration = time.Since(queryStart)
		if err != nil {
			queryErr = err
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// --- Phase 4: Response serialization ---
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
		if _, err := w.Write(data); err != nil {
			log.Debug("client write error", slog.Any("err", err))
		}

		serDuration = time.Since(serializeStart)
	}
}

// handlerError carries an HTTP status and message for parse-phase errors.
type handlerError struct {
	status int
	msg    string
}

// handlerParams wraps sql.ListParams with the parsed group/resource fields
// needed for the RBAC check.
type handlerParams struct {
	sql.ListParams
	Group    string // API group (e.g. "apps", "widgets.templates.krateo.io")
	Resource string // K8s plural resource name (e.g. "panels", "deployments"), used for RBAC
}

// buildResourceKind constructs the DB resource_kind from group, version, and kind.
// Format: "group/version.Kind" (e.g. "widgets.templates.krateo.io/v1beta1.Panel").
func buildResourceKind(group, version, kind string) string {
	return group + "/" + version + "." + kind
}

// parseRequest validates the request and extracts params from query string (GET) or JSON body (POST).
// The resource type is identified by required query/body params: group, version, kind, resource.
// Namespace is required for RBAC authorization.
func parseRequest(r *http.Request) (handlerParams, *handlerError) {
	var (
		params   sql.ListParams
		group    string
		resource string
		err      error
	)

	switch r.Method {
	case http.MethodPost:
		params, group, resource, err = parseListParamsJSON(r)
	default:
		params, group, resource, err = parseListParams(r)
	}

	if err != nil {
		return handlerParams{}, &handlerError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid parameters: %v", err),
		}
	}

	// Namespace is required for RBAC authorization. At least for now
	if params.Namespace == "" {
		return handlerParams{ListParams: params, Group: group, Resource: resource}, &handlerError{
			status: http.StatusBadRequest,
			msg:    "query parameter 'namespace' is required",
		}
	}

	return handlerParams{ListParams: params, Group: group, Resource: resource}, nil
}

func parseListParams(r *http.Request) (sql.ListParams, string, string, error) {
	q := r.URL.Query()

	// group, version, kind, resource are required to identify the resource type.
	group := q.Get("group")
	version := q.Get("version")
	kind := q.Get("kind")
	resource := q.Get("resource")
	if group == "" || version == "" || kind == "" || resource == "" {
		return sql.ListParams{}, "", "", fmt.Errorf("query parameters 'group', 'version', 'kind', and 'resource' are all required")
	}

	resourceKind := buildResourceKind(group, version, kind)

	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		return sql.ListParams{}, "", "", err
	}

	compositionID := q.Get("composition_id")
	if compositionID != "" && !uuidRegex.MatchString(compositionID) {
		return sql.ListParams{}, "", "", fmt.Errorf("invalid composition_id: not a valid UUID")
	}

	// Validate labels JSON if provided.
	labels := q.Get("labels")
	if labels != "" {
		var tmp map[string]any
		if err := json.Unmarshal([]byte(labels), &tmp); err != nil {
			return sql.ListParams{}, "", "", fmt.Errorf("invalid labels JSON: %w", err)
		}
	}

	// Parse since timestamp if provided.
	var since *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return sql.ListParams{}, "", "", fmt.Errorf("invalid since: must be RFC3339 timestamp")
		}
		since = &t
	}

	cursor := sql.EncodedCursor(q.Get("cursor"))
	if err := sql.ValidateCursor(cursor); err != nil {
		return sql.ListParams{}, "", "", fmt.Errorf("invalid cursor: %w", err)
	}

	p := sql.ListParams{
		ResourceKind:  resourceKind,
		Cluster:       q.Get("cluster"),
		Namespace:     q.Get("namespace"),
		CompositionID: compositionID,
		Name:          q.Get("name"),
		Labels:        labels,
		Since:         since,
		Raw:           q.Get("raw") == "true",
		Limit:         limit,
		Cursor:        cursor,
	}

	return p, group, resource, nil
}

// resourcesJSONPayload is the JSON body for POST /resources.
type resourcesJSONPayload struct {
	Group         string         `json:"group"`
	Version       string         `json:"version"`
	Kind          string         `json:"kind"`
	Resource      string         `json:"resource"`
	Cluster       string         `json:"cluster"`
	Namespace     string         `json:"namespace"`
	CompositionID string         `json:"composition_id"`
	Name          string         `json:"name"`
	Labels        map[string]any `json:"labels"`
	Since         *time.Time     `json:"since"`
	Raw           *bool          `json:"raw"`
	Limit         *int           `json:"limit"`
	Cursor        string         `json:"cursor"`
}

func parseListParamsJSON(r *http.Request) (sql.ListParams, string, string, error) {
	defer r.Body.Close()

	var payload resourcesJSONPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&payload); err != nil {
		if err == io.EOF {
			return sql.ListParams{}, "", "", fmt.Errorf("empty JSON body")
		}
		return sql.ListParams{}, "", "", fmt.Errorf("invalid JSON body: %w", err)
	}

	// Reject multiple JSON values.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return sql.ListParams{}, "", "", fmt.Errorf("invalid JSON body: multiple JSON values")
	}

	// group, version, kind, resource are required.
	if payload.Group == "" || payload.Version == "" || payload.Kind == "" || payload.Resource == "" {
		return sql.ListParams{}, "", "", fmt.Errorf("fields 'group', 'version', 'kind', and 'resource' are all required")
	}

	resourceKind := buildResourceKind(payload.Group, payload.Version, payload.Kind)

	limit := defaultLimit
	if payload.Limit != nil {
		limit = *payload.Limit
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	if payload.CompositionID != "" && !uuidRegex.MatchString(payload.CompositionID) {
		return sql.ListParams{}, "", "", fmt.Errorf("invalid composition_id: not a valid UUID")
	}

	var labels string
	if payload.Labels != nil {
		rawLabels, err := json.Marshal(payload.Labels)
		if err != nil {
			return sql.ListParams{}, "", "", fmt.Errorf("invalid labels JSON")
		}
		labels = string(rawLabels)
	}

	raw := false
	if payload.Raw != nil {
		raw = *payload.Raw
	}

	cursor := sql.EncodedCursor(payload.Cursor)
	if err := sql.ValidateCursor(cursor); err != nil {
		return sql.ListParams{}, "", "", fmt.Errorf("invalid cursor: %w", err)
	}

	p := sql.ListParams{
		ResourceKind:  resourceKind,
		Cluster:       payload.Cluster,
		Namespace:     payload.Namespace,
		CompositionID: payload.CompositionID,
		Name:          payload.Name,
		Labels:        labels,
		Since:         payload.Since,
		Raw:           raw,
		Limit:         limit,
		Cursor:        cursor,
	}

	return p, payload.Group, payload.Resource, nil
}

func parseLimit(v string) (int, error) {
	limit := defaultLimit
	if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("invalid limit: %w", err)
		}
		limit = n
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit, nil
}
