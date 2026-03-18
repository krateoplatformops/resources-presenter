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
	queryTimeout = 10 * time.Second
)

// uuidRegex validates UUID format (RFC 4122).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Authorizer checks whether the current user is allowed to GET resources.
// It receives a list of discovered (group, resource, namespace) targets and
// returns only the targets the user is permitted to access.
type Authorizer interface {
	FilterAllowed(ctx context.Context, targets []sql.ResourceTarget) []sql.ResourceTarget
}

// ResourcesHandler returns an HTTP handler for GET/POST /resources.
// The resource type is identified by query parameters:
//   - group     → API group (required, e.g. "apps", "widgets.templates.krateo.io")
//   - version   → API version (optional, e.g. "v1", "v1beta1")
//   - resource  → K8s plural resource name (optional, e.g. "panels", "deployments")
//   - namespace → K8s namespace (optional, default "default", "*" for all namespaces)
//
// Additional filters to narrow down the query and RBAC targets:
//   - cluster        → exact match on cluster_name
//   - composition_id → filter by composition_id (UUID string)
//   - name           → exact match on resource_name (mutually exclusive with 'name_contains')
//   - name_contains  → case-insensitive partial match on resource_name (mutually exclusive with 'name')
//   - labels         → JSON object for label filtering (e.g. {"env": "prod", "tier": "frontend"})
//   - since          → RFC3339 timestamp to filter resources created after that time
//
// GET uses query parameters; POST uses a JSON body with the same fields.
//
// The handler flow:
//  1. Parse and validate request parameters
//  2. Discovery: find distinct (group, resource, namespace) tuples in the DB
//  3. RBAC: batch-check permissions, keep only allowed targets
//  4. Query: list resources filtered to allowed targets
//  5. Serialize and return response
//
// Design decision: an empty result set returns 200 with an empty items array,
// not 404. This is consistent with Kubernetes LIST semantics: the resource kind
// is valid, there are just no instances matching the filters.
func ResourcesHandler(db *pgxpool.Pool, log *slog.Logger, auth Authorizer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// NOTE on latency measurement:
		//
		// totalStart marks the beginning of the handler. It does NOT include
		// time spent in upstream middleware (TraceId → Access → CORS → GZip → UserConfig).
		// In particular, the UserConfig middleware (JWT validation + fetching the
		// user's clientconfig Secret from the Kubernetes API) runs BEFORE this
		// handler and can add significant latency.
		//
		// The Access middleware (use.Access) wraps the entire chain and logs its
		// own "latency" field, which DOES include middleware time. Therefore:
		//
		//   Access.latency  =  UserConfig time  +  handler 6_total time
		//
		// The gap between Access.latency and 6_total is mostly the UserConfig middleware.
		totalStart := time.Now()

		ctx := xcontext.BuildContext(r.Context())
		traceId := xcontext.TraceId(ctx, false)

		// Per-request state for the deferred log.
		var (
			gvr              string
			statusCode       int
			rowsReturned     int
			parseDuration    time.Duration
			discoverDuration time.Duration
			rbacDuration     time.Duration
			queryDuration    time.Duration
			serDuration      time.Duration
			queryErr         error
		)

		// Every request gets logged in detail, then there is also the higher-level access log due to the Access middleware.
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
				slog.String("gvr", gvr),
				slog.String("trace_id", traceId),
				slog.Int("status_code", statusCode),
				slog.Int("rows_returned", rowsReturned),
				slog.Group("handler_duration_ms",
					slog.Float64("1_parse", float64(parseDuration.Microseconds())/1000.0),
					slog.Float64("2_discovery", float64(discoverDuration.Microseconds())/1000.0),
					slog.Float64("3_rbac_authz", float64(rbacDuration.Microseconds())/1000.0),
					slog.Float64("4_query", float64(queryDuration.Microseconds())/1000.0),
					slog.Float64("5_serialize", float64(serDuration.Microseconds())/1000.0),
					slog.Float64("6_total", float64(totalDuration.Microseconds())/1000.0),
				),
			}
			if queryErr != nil {
				attrs = append(attrs, slog.Any("err", queryErr))
			}

			log.LogAttrs(r.Context(), lvl, "request completed by handler", attrs...)
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

		// Build GVR string for logging (use available fields).
		if params.ResourceGroup != "" {
			gvr = params.ResourceGroup
			if params.ResourceVersion != "" {
				gvr += "/" + params.ResourceVersion
			}
			if params.ResourcePlural != "" {
				gvr += "." + params.ResourcePlural
			}
		}

		if parseErr != nil {
			statusCode = parseErr.status
			response.Encode(w, response.New(parseErr.status, fmt.Errorf("%s", parseErr.msg)))
			return
		}

		// --- Phase 2: Discovery ---
		// Find distinct (group, resource, namespace) tuples matching the
		// request filters. These are the targets we need to RBAC-check.
		discoverStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		discovered, err := sql.DiscoverTargets(queryCtx, db, params.ListParams)
		discoverDuration = time.Since(discoverStart)
		if err != nil {
			log.Debug("discovery error", slog.Any("err", err), slog.String("trace_id", traceId))
			queryErr = err
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// No matching resources exist: return empty result (not 404).
		if len(discovered) == 0 {
			log.Debug("discovery found no targets", slog.String("gvr", gvr), slog.String("trace_id", traceId))
			statusCode = http.StatusOK
			emptyResult := &sql.ListResult{Count: 0, Items: []sql.ResourceItem{}}
			data, _ := json.Marshal(emptyResult)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			return
		} else {
			log.Debug("discovery found targets", slog.Int("count", len(discovered)), slog.String("gvr", gvr), slog.String("trace_id", traceId))
		}

		// --- Phase 3: RBAC authorization (batch) ---
		rbacStart := time.Now()

		allowed := auth.FilterAllowed(r.Context(), discovered)
		rbacDuration = time.Since(rbacStart)

		if len(allowed) == 0 {
			log.Debug("RBAC filter excluded all targets", slog.String("gvr", gvr), slog.String("trace_id", traceId))
			statusCode = http.StatusForbidden
			response.Encode(w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
			return
		} else {
			log.Debug("RBAC filter allowed some targets", slog.Int("allowed_count", len(allowed)), slog.Int("discovered_count", len(discovered)), slog.String("gvr", gvr), slog.String("trace_id", traceId))
		}

		// Inject allowed targets into params for the list query.
		params.AllowedTargets = allowed

		// --- Phase 4: DB query execution ---
		queryStart := time.Now()

		result, err := sql.ListResources(queryCtx, db, params.ListParams)
		queryDuration = time.Since(queryStart)
		if err != nil {
			queryErr = err
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// --- Phase 5: Response serialization ---
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
			log.Debug("client write error", slog.Any("err", err), slog.String("trace_id", traceId))
		}

		serDuration = time.Since(serializeStart)
	}
}

// ResourceDetailHandler returns an HTTP handler for GET /resources/{global_uid}.
// It fetches a single resource by its global_uid and returns it in the same
// response format as the list endpoint (count + items array).
//
// Query parameters:
//   - raw → include full Kubernetes object (default: true)
//
// The handler flow:
//  1. Parse and validate path parameter (global_uid)
//  2. Query: fetch resource by global_uid
//  3. RBAC: check permissions on the fetched resource's (group, resource, namespace)
//  4. Serialize and return response
//
// Returns 400 for missing global_uid, 404 if not found, 403 if RBAC denied.
func ResourceDetailHandler(db *pgxpool.Pool, log *slog.Logger, auth Authorizer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		totalStart := time.Now()

		ctx := xcontext.BuildContext(r.Context())
		traceId := xcontext.TraceId(ctx, false)

		var (
			globalUID     string
			statusCode    int
			parseDuration time.Duration
			queryDuration time.Duration
			rbacDuration  time.Duration
			serDuration   time.Duration
			queryErr      error
		)

		defer func() {
			totalDuration := time.Since(totalStart)
			lvl := slog.LevelDebug
			if statusCode >= 500 {
				lvl = slog.LevelError
			} else if statusCode >= 400 {
				lvl = slog.LevelWarn
			}

			attrs := []slog.Attr{
				slog.String("handler", "resource_detail"),
				slog.String("method", r.Method),
				slog.String("global_uid", globalUID),
				slog.String("trace_id", traceId),
				slog.Int("status_code", statusCode),
				slog.Group("handler_duration_ms",
					slog.Float64("1_parse", float64(parseDuration.Microseconds())/1000.0),
					slog.Float64("2_query", float64(queryDuration.Microseconds())/1000.0),
					slog.Float64("3_rbac_authz", float64(rbacDuration.Microseconds())/1000.0),
					slog.Float64("4_serialize", float64(serDuration.Microseconds())/1000.0),
					slog.Float64("5_total", float64(totalDuration.Microseconds())/1000.0),
				),
			}
			if queryErr != nil {
				attrs = append(attrs, slog.Any("err", queryErr))
			}

			log.LogAttrs(r.Context(), lvl, "request completed by handler", attrs...)
		}()

		if r.Method != http.MethodGet {
			statusCode = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "GET")
			response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
			return
		}

		// --- Phase 1: Parse path parameter ---
		parseStart := time.Now()

		globalUID = r.PathValue("global_uid")
		if globalUID == "" {
			parseDuration = time.Since(parseStart)
			statusCode = http.StatusBadRequest
			response.Encode(w, response.New(http.StatusBadRequest, fmt.Errorf("global_uid path parameter is required")))
			return
		}

		// raw defaults to true for the detail endpoint.
		includeRaw := true
		if r.URL.Query().Get("raw") == "false" {
			includeRaw = false
		}

		parseDuration = time.Since(parseStart)

		// --- Phase 2: DB query ---
		queryStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		result, err := sql.GetByGlobalUID(queryCtx, db, globalUID, includeRaw)
		queryDuration = time.Since(queryStart)
		if err != nil {
			queryErr = err
			statusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		if result.Count == 0 {
			statusCode = http.StatusNotFound
			response.Encode(w, response.New(http.StatusNotFound, fmt.Errorf("resource not found: %s", globalUID)))
			return
		}

		// --- Phase 3: RBAC authorization ---
		rbacStart := time.Now()

		item := result.Items[0]
		targets := []sql.ResourceTarget{
			{Group: item.Group, Resource: item.Resource, Namespace: item.Namespace},
		}
		allowed := auth.FilterAllowed(r.Context(), targets)
		rbacDuration = time.Since(rbacStart)

		if len(allowed) == 0 {
			log.Debug("RBAC denied access to resource", slog.String("global_uid", globalUID), slog.String("trace_id", traceId))
			statusCode = http.StatusForbidden
			response.Encode(w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
			return
		}

		// --- Phase 4: Serialize ---
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

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(data); err != nil {
			log.Debug("client write error", slog.Any("err", err), slog.String("trace_id", traceId))
		}

		serDuration = time.Since(serializeStart)
	}
}

// handlerError carries an HTTP status and message for parse-phase errors.
type handlerError struct {
	status int
	msg    string
}

// handlerParams wraps sql.ListParams.
// Group and plural resource name for RBAC are accessed via
// ListParams.ResourceGroup and ListParams.ResourcePlural.
type handlerParams struct {
	sql.ListParams
}

// parseRequest validates the request and extracts params from query string (GET) or JSON body (POST).
// The resource type is identified by the required group parameter; version, resource, and namespace
// are optional filters used to narrow the discovery query and subsequent RBAC checks.
func parseRequest(r *http.Request) (handlerParams, *handlerError) {
	var (
		params sql.ListParams
		err    error
	)

	switch r.Method {
	case http.MethodPost:
		params, err = parseListParamsJSON(r)
	default:
		params, err = parseListParams(r)
	}

	if err != nil {
		return handlerParams{}, &handlerError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid parameters: %v", err),
		}
	}

	return handlerParams{ListParams: params}, nil
}

func parseListParams(r *http.Request) (sql.ListParams, error) {
	q := r.URL.Query()

	// group is required; version, resource, namespace are optional filters.
	group := q.Get("group")
	if group == "" {
		return sql.ListParams{}, fmt.Errorf("query parameter 'group' is required")
	}

	version := q.Get("version")
	resource := q.Get("resource")

	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		return sql.ListParams{}, err
	}

	compositionID := q.Get("composition_id")
	if compositionID != "" && !uuidRegex.MatchString(compositionID) {
		return sql.ListParams{}, fmt.Errorf("invalid composition_id: not a valid UUID")
	}

	// Validate labels JSON if provided.
	labels := q.Get("labels")
	if labels != "" {
		var tmp map[string]any
		if err := json.Unmarshal([]byte(labels), &tmp); err != nil {
			return sql.ListParams{}, fmt.Errorf("invalid labels JSON: %w", err)
		}
	}

	// Parse since timestamp if provided.
	var since *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return sql.ListParams{}, fmt.Errorf("invalid since: must be RFC3339 timestamp")
		}
		since = &t
	}

	cursor := sql.EncodedCursor(q.Get("cursor"))
	if err := sql.ValidateCursor(cursor); err != nil {
		return sql.ListParams{}, fmt.Errorf("invalid cursor: %w", err)
	}

	// Namespace handling follows Kubernetes API semantics:
	// - absent/empty → "default" (like kubectl)
	// - "*"          → all namespaces (still filtered by RBAC)
	// - any other    → exact match
	namespace := q.Get("namespace")
	switch namespace {
	case "":
		namespace = "default"
	case "*":
		namespace = "" // empty = no namespace filter in SQL layer
	}

	name := q.Get("name")
	nameContains := q.Get("name_contains")
	if name != "" && nameContains != "" {
		return sql.ListParams{}, fmt.Errorf("'name' and 'name_contains' are mutually exclusive")
	}

	p := sql.ListParams{
		ResourceGroup:   group,
		ResourceVersion: version,
		ResourcePlural:  resource,
		Cluster:         q.Get("cluster"),
		Namespace:       namespace,
		CompositionID:   compositionID,
		Name:            name,
		NameContains:    nameContains,
		Labels:          labels,
		Since:           since,
		Raw:             q.Get("raw") == "true",
		Limit:           limit,
		Cursor:          cursor,
	}

	return p, nil
}

// resourcesJSONPayload is the JSON body for POST /resources.
type resourcesJSONPayload struct {
	Group         string         `json:"group"`
	Version       string         `json:"version"`
	Resource      string         `json:"resource"`
	Cluster       string         `json:"cluster"`
	Namespace     string         `json:"namespace"`
	CompositionID string         `json:"composition_id"`
	Name          string         `json:"name"`
	NameContains  string         `json:"name_contains"`
	Labels        map[string]any `json:"labels"`
	Since         *time.Time     `json:"since"`
	Raw           *bool          `json:"raw"`
	Limit         *int           `json:"limit"`
	Cursor        string         `json:"cursor"`
}

func parseListParamsJSON(r *http.Request) (sql.ListParams, error) {
	defer r.Body.Close()

	var payload resourcesJSONPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // Reject unknown fields to prevent silent typos.

	if err := dec.Decode(&payload); err != nil {
		if err == io.EOF {
			return sql.ListParams{}, fmt.Errorf("empty JSON body")
		}
		return sql.ListParams{}, fmt.Errorf("invalid JSON body: %w", err)
	}

	// Reject multiple JSON values.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return sql.ListParams{}, fmt.Errorf("invalid JSON body: multiple JSON values")
	}

	// group is required; version and resource are optional filters.
	if payload.Group == "" {
		return sql.ListParams{}, fmt.Errorf("field 'group' is required")
	}

	limit := defaultLimit
	if payload.Limit != nil {
		limit = *payload.Limit
	}
	if limit == -1 {
		// unlimited, keep -1
	} else if limit <= 0 {
		limit = defaultLimit
	}

	if payload.Name != "" && payload.NameContains != "" {
		return sql.ListParams{}, fmt.Errorf("'name' and 'name_contains' are mutually exclusive")
	}

	if payload.CompositionID != "" && !uuidRegex.MatchString(payload.CompositionID) {
		return sql.ListParams{}, fmt.Errorf("invalid composition_id: not a valid UUID")
	}

	var labels string
	if payload.Labels != nil {
		rawLabels, err := json.Marshal(payload.Labels)
		if err != nil {
			return sql.ListParams{}, fmt.Errorf("invalid labels JSON")
		}
		labels = string(rawLabels)
	}

	raw := false
	if payload.Raw != nil {
		raw = *payload.Raw
	}

	// Namespace handling follows Kubernetes API semantics:
	// - absent/empty → "default" (like kubectl)
	// - "*"          → all namespaces (still filtered by RBAC)
	// - any other    → exact match
	namespace := payload.Namespace
	switch namespace {
	case "":
		namespace = "default"
	case "*":
		namespace = "" // empty = no namespace filter in SQL layer
	}

	cursor := sql.EncodedCursor(payload.Cursor)
	if err := sql.ValidateCursor(cursor); err != nil {
		return sql.ListParams{}, fmt.Errorf("invalid cursor: %w", err)
	}

	p := sql.ListParams{
		ResourceGroup:   payload.Group,
		ResourceVersion: payload.Version,
		ResourcePlural:  payload.Resource,
		Cluster:         payload.Cluster,
		Namespace:       namespace,
		CompositionID:   payload.CompositionID,
		Name:            payload.Name,
		NameContains:    payload.NameContains,
		Labels:          labels,
		Since:           payload.Since,
		Raw:             raw,
		Limit:           limit,
		Cursor:          cursor,
	}

	return p, nil
}

func parseLimit(v string) (int, error) {
	limit := defaultLimit
	if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("invalid limit: %w", err)
		}
		if n == -1 {
			return -1, nil // unlimited
		}
		limit = n
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	return limit, nil
}
