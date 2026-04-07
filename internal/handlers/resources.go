package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	xcontext "github.com/krateoplatformops/plumbing/context"
	"github.com/krateoplatformops/plumbing/http/response"
	"github.com/krateoplatformops/resources-presenter/internal/sql"
	"github.com/krateoplatformops/resources-presenter/internal/telemetry"
)

const (
	defaultLimit = 100
	maxLimit     = 5000
	queryTimeout = 10 * time.Second
)

// --- Handler types ---

// resourceHandler groups shared dependencies for the resources HTTP handlers (list and detail).
type resourceHandler struct {
	db      *pgxpool.Pool
	log     *slog.Logger
	auth    Authorizer
	metrics *telemetry.Metrics
}

// requestCtx groups per-request state passed through handler phases.
type requestCtx struct {
	w         http.ResponseWriter
	hl        *handlerLogger
	traceID   string
	gvr       string // group/version/resource string for logging (list handler only)
	globalUID string // for detail handler logging (detail handler only)
}

// --- Handlers ---

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
// Note: an empty result set returns 200 with an empty items array,
// not 404. This is consistent with Kubernetes LIST semantics: the resource kind
// is valid, there are just no instances matching the filters.
func ResourcesHandler(db *pgxpool.Pool, log *slog.Logger, auth Authorizer, metrics *telemetry.Metrics) http.HandlerFunc {
	h := &resourceHandler{db: db, log: log, auth: auth, metrics: metrics}
	return h.handleResources
}

// handleResources implements the GET/POST /resources handler flow.
//
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
//	Access.latency  =  UserConfig time  +  handler 6_total time
//
// The gap between Access.latency and 6_total is mostly the UserConfig middleware.
func (h *resourceHandler) handleResources(w http.ResponseWriter, r *http.Request) {
	traceID := xcontext.TraceId(r.Context(), false)

	hl := newHandlerLogger(h.log, r, "resources", traceID, h.metrics)
	defer hl.emit()
	defer func() {
		status := hl.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		h.metrics.RecordHTTPRequest(r.Context(), hl.handler, r.Method, status, time.Since(hl.start))
	}()

	// Only GET and POST are allowed.
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		hl.StatusCode = http.StatusMethodNotAllowed
		h.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "method_not_allowed", http.StatusMethodNotAllowed)
		w.Header().Set("Allow", "GET, POST")
		response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
		return
	}

	rc := &requestCtx{w: w, hl: hl, traceID: traceID}

	// --- Phase 1: Request parsing / validation ---
	params, ok := resourcesParse(rc, r)
	if !ok {
		return
	}

	queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
	defer queryCancel()

	// --- Phase 2: Discovery ---
	discovered, ok := h.resourcesDiscover(queryCtx, rc, params)
	if !ok {
		return
	}

	// --- Phase 3: RBAC authorization (batch) ---
	allowed, ok := h.resourcesRBAC(r.Context(), rc, discovered)
	if !ok {
		return
	}
	params.AllowedTargets = allowed

	// --- Phase 4: DB query execution ---
	result, ok := h.resourcesQuery(queryCtx, rc, params)
	if !ok {
		return
	}

	// --- Phase 5: Response serialization ---
	h.serializeResponse(rc, "5_serialize", result)
}

// resourcesParse handles Phase 1: request parsing and validation.
// It populates rc.gvr for downstream logging.
func resourcesParse(rc *requestCtx, r *http.Request) (sql.ListParams, bool) {
	parseStart := time.Now()

	params, parseErr := parseRequest(rc.w, r)
	rc.hl.addPhase("1_parse", time.Since(parseStart))

	rc.gvr = buildGVR(params.ResourceGroup, params.ResourceVersion, params.ResourcePlural)
	rc.hl.Extra = []slog.Attr{
		slog.String("gvr", rc.gvr),
	}

	if parseErr != nil {
		rc.hl.StatusCode = parseErr.status
		rc.hl.Err = fmt.Errorf("%s", parseErr.msg)
		rc.hl.metrics.IncHTTPError(r.Context(), rc.hl.handler, r.Method, "invalid_params", parseErr.status)
		response.Encode(rc.w, response.New(parseErr.status, fmt.Errorf("%s", parseErr.msg)))
		return sql.ListParams{}, false
	}
	return params, true
}

// resourcesDiscover handles Phase 2: finding distinct (group, resource, namespace)
// tuples matching the request filters. These tuples define the RBAC targets for the next phase.
func (h *resourceHandler) resourcesDiscover(ctx context.Context, rc *requestCtx, params sql.ListParams) ([]sql.ResourceTarget, bool) {
	discoverStart := time.Now()

	discovered, err := sql.DiscoverTargets(ctx, h.db, params)
	rc.hl.addPhase("2_discovery", time.Since(discoverStart))
	if err != nil {
		h.log.Debug("discovery error", slog.Any("err", err), slog.String("trace_id", rc.traceID))
		rc.hl.Err = err
		rc.hl.StatusCode = http.StatusInternalServerError
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "discovery_error", http.StatusInternalServerError)
		response.InternalError(rc.w, fmt.Errorf("internal server error"))
		return nil, false
	}

	// No matching resources exist: return empty result (not 404).
	if len(discovered) == 0 {
		h.log.Debug("discovery found no targets", slog.String("gvr", rc.gvr), slog.String("trace_id", rc.traceID))
		rc.hl.StatusCode = http.StatusOK
		writeJSON(rc.w, h.log, rc.traceID, emptyListResult)
		return nil, false
	}
	h.metrics.AddDiscoveredTargets(ctx, rc.hl.handler, int64(len(discovered)))
	h.log.Debug("discovery found targets", slog.Int("count", len(discovered)), slog.String("gvr", rc.gvr), slog.String("trace_id", rc.traceID))
	return discovered, true
}

// resourcesRBAC handles Phase 3: batch RBAC authorization.
func (h *resourceHandler) resourcesRBAC(ctx context.Context, rc *requestCtx, discovered []sql.ResourceTarget) ([]sql.ResourceTarget, bool) {
	rbacStart := time.Now()

	allowed := h.auth.FilterAllowed(ctx, discovered)
	rc.hl.addPhase("3_rbac_authz", time.Since(rbacStart))

	if len(allowed) == 0 {
		h.log.Debug("RBAC filter excluded all targets", slog.String("gvr", rc.gvr), slog.String("trace_id", rc.traceID))
		rc.hl.StatusCode = http.StatusForbidden
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "rbac_denied", http.StatusForbidden)
		response.Encode(rc.w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
		return nil, false
	}
	h.metrics.AddAllowedTargets(ctx, rc.hl.handler, int64(len(allowed)))
	h.log.Debug("RBAC filter allowed some targets", slog.Int("allowed_count", len(allowed)), slog.Int("discovered_count", len(discovered)), slog.String("gvr", rc.gvr), slog.String("trace_id", rc.traceID))
	return allowed, true
}

// resourcesQuery handles Phase 4: DB query execution.
func (h *resourceHandler) resourcesQuery(ctx context.Context, rc *requestCtx, params sql.ListParams) (*sql.ListResult, bool) {
	queryStart := time.Now()

	result, err := sql.ListResources(ctx, h.db, params)
	rc.hl.addPhase("4_query", time.Since(queryStart))
	if err != nil {
		h.log.Debug("list query error", slog.Any("err", err), slog.String("trace_id", rc.traceID))
		rc.hl.Err = err
		rc.hl.StatusCode = http.StatusInternalServerError
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "query_error", http.StatusInternalServerError)
		response.InternalError(rc.w, fmt.Errorf("internal server error"))
		return nil, false
	}
	return result, true
}

// serializeResponse handles the final response serialization phase for both handlers.
func (h *resourceHandler) serializeResponse(rc *requestCtx, phaseLabel string, result *sql.ListResult) {
	serStart := time.Now()

	data, err := writeJSON(rc.w, h.log, rc.traceID, result)
	if err != nil {
		h.log.Debug("response serialization error", slog.Any("err", err), slog.String("trace_id", rc.traceID))
		rc.hl.Err = fmt.Errorf("serialize: %w", err)
		rc.hl.StatusCode = http.StatusInternalServerError
		h.metrics.IncHTTPError(rc.hl.r.Context(), rc.hl.handler, rc.hl.r.Method, "serialize_error", http.StatusInternalServerError)
		response.InternalError(rc.w, fmt.Errorf("response serialization failed"))
		rc.hl.addPhase(phaseLabel, time.Since(serStart))
		return
	}

	rc.hl.Extra = append(rc.hl.Extra, slog.Int("response_size_bytes", len(data)))
	rc.hl.StatusCode = http.StatusOK
	rc.hl.RowsReturned = len(result.Items)
	h.metrics.AddResourcesReturned(rc.hl.r.Context(), rc.hl.handler, rc.hl.r.Method, int64(len(result.Items)))
	rc.hl.addPhase(phaseLabel, time.Since(serStart))
}

// ResourceDetailHandler returns an HTTP handler for GET /resources/{global_uid}.
// It fetches a single resource by its global_uid and returns it in the same
// response format as the list endpoint (count + items array).
//
// Query parameters:
//   - raw        → include full Kubernetes object (default: true)
//   - status_raw → include status_raw JSONB column (default: true)
//
// The handler flow:
//  1. Parse and validate path parameter (global_uid)
//  2. Query: fetch resource by global_uid
//  3. RBAC: check permissions on the fetched resource's (group, resource, namespace)
//  4. Serialize and return response
//
// Returns 400 for missing global_uid, 404 if not found, 403 if RBAC denied.
func ResourceDetailHandler(db *pgxpool.Pool, log *slog.Logger, auth Authorizer, metrics *telemetry.Metrics) http.HandlerFunc {
	h := &resourceHandler{db: db, log: log, auth: auth, metrics: metrics}
	return h.handleResourceDetail
}

// handleResourceDetail implements the GET /resources/{global_uid} handler flow.
func (h *resourceHandler) handleResourceDetail(w http.ResponseWriter, r *http.Request) {
	traceID := xcontext.TraceId(r.Context(), false)

	hl := newHandlerLogger(h.log, r, "resource_detail", traceID, h.metrics)
	defer hl.emit()
	defer func() {
		status := hl.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		h.metrics.RecordHTTPRequest(r.Context(), hl.handler, r.Method, status, time.Since(hl.start))
	}()

	if r.Method != http.MethodGet {
		hl.StatusCode = http.StatusMethodNotAllowed
		h.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "method_not_allowed", http.StatusMethodNotAllowed)
		w.Header().Set("Allow", "GET")
		response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
		return
	}

	// --- Phase 1: Parse path parameter ---
	globalUID, includeRaw, includeStatusRaw, ok := detailParse(w, r, hl)
	if !ok {
		return
	}

	rc := &requestCtx{w: w, hl: hl, traceID: traceID, globalUID: globalUID}

	queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
	defer queryCancel()

	// --- Phase 2: DB query ---
	result, ok := h.detailQuery(queryCtx, rc, includeRaw, includeStatusRaw)
	if !ok {
		return
	}

	// --- Phase 3: RBAC authorization ---
	if ok := h.detailRBAC(r.Context(), rc, result.Items[0]); !ok {
		return
	}

	// --- Phase 4: Serialize ---
	h.serializeResponse(rc, "4_serialize", result)
}

// detailParse handles Phase 1: parsing path and query parameters.
func detailParse(w http.ResponseWriter, r *http.Request, hl *handlerLogger) (globalUID string, includeRaw, includeStatusRaw, ok bool) {
	parseStart := time.Now()

	globalUID = r.PathValue("global_uid")
	if globalUID == "" {
		hl.addPhase("1_parse", time.Since(parseStart))
		hl.StatusCode = http.StatusBadRequest
		hl.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "invalid_params", http.StatusBadRequest)
		response.Encode(w, response.New(http.StatusBadRequest, fmt.Errorf("global_uid path parameter is required")))
		return "", false, false, false
	}

	// raw and status_raw default to true for the detail endpoint.
	includeRaw = r.URL.Query().Get("raw") != "false"
	includeStatusRaw = r.URL.Query().Get("status_raw") != "false"

	hl.addPhase("1_parse", time.Since(parseStart))
	hl.Extra = []slog.Attr{
		slog.String("global_uid", globalUID),
	}
	return globalUID, includeRaw, includeStatusRaw, true
}

// detailQuery handles Phase 2: fetching the resource by global_uid and validating the result.
func (h *resourceHandler) detailQuery(ctx context.Context, rc *requestCtx, includeRaw, includeStatusRaw bool) (*sql.ListResult, bool) {
	queryStart := time.Now()

	result, err := sql.GetByGlobalUID(ctx, h.db, rc.globalUID, includeRaw, includeStatusRaw)
	rc.hl.addPhase("2_query", time.Since(queryStart))
	if err != nil {
		h.log.Debug("DB query error", slog.Any("err", err), slog.String("global_uid", rc.globalUID), slog.String("trace_id", rc.traceID))
		rc.hl.Err = err
		rc.hl.StatusCode = http.StatusInternalServerError
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "query_error", http.StatusInternalServerError)
		response.InternalError(rc.w, fmt.Errorf("internal server error"))
		return nil, false
	}

	if result.Count == 0 {
		h.log.Debug("resource not found", slog.String("global_uid", rc.globalUID), slog.String("trace_id", rc.traceID))
		rc.hl.StatusCode = http.StatusNotFound
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "not_found", http.StatusNotFound)
		response.Encode(rc.w, response.New(http.StatusNotFound, fmt.Errorf("resource not found: %s", rc.globalUID)))
		return nil, false
	}
	if result.Count > 1 {
		h.log.Error("unexpected multiple results for global_uid", slog.String("global_uid", rc.globalUID), slog.Int("count", result.Count), slog.String("trace_id", rc.traceID))
		rc.hl.Err = fmt.Errorf("unexpected multiple results for global_uid %s", rc.globalUID)
		rc.hl.StatusCode = http.StatusInternalServerError
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "multiple_results", http.StatusInternalServerError)
		response.InternalError(rc.w, fmt.Errorf("internal server error"))
		return nil, false
	}
	return result, true
}

// detailRBAC handles Phase 3: RBAC authorization for a single resource.
func (h *resourceHandler) detailRBAC(ctx context.Context, rc *requestCtx, item sql.ResourceItem) bool {
	rbacStart := time.Now()

	targets := []sql.ResourceTarget{
		{Group: item.Group, Resource: item.Resource, Namespace: item.Namespace},
	}
	allowed := h.auth.FilterAllowed(ctx, targets)
	rc.hl.addPhase("3_rbac_authz", time.Since(rbacStart))

	if len(allowed) == 0 {
		h.log.Debug("RBAC denied access to resource", slog.String("global_uid", rc.globalUID), slog.String("trace_id", rc.traceID))
		rc.hl.StatusCode = http.StatusForbidden
		h.metrics.IncHTTPError(ctx, rc.hl.handler, rc.hl.r.Method, "rbac_denied", http.StatusForbidden)
		response.Encode(rc.w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
		return false
	}
	h.metrics.AddAllowedTargets(ctx, rc.hl.handler, int64(len(allowed)))
	h.log.Debug("RBAC allowed access to resource", slog.String("global_uid", rc.globalUID), slog.String("trace_id", rc.traceID))
	return true
}
