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
)

const (
	defaultLimit = 100
	queryTimeout = 10 * time.Second
)

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
		ctx := xcontext.BuildContext(r.Context())
		traceID := xcontext.TraceId(ctx, false)

		hl := newHandlerLogger(log, r, "resources", traceID)
		defer hl.emit()

		// Only GET and POST are allowed.
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			hl.StatusCode = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "GET, POST")
			response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
			return
		}

		// --- Phase 1: Request parsing / validation ---
		parseStart := time.Now()

		params, parseErr := parseRequest(r)
		hl.addPhase("1_parse", time.Since(parseStart))

		gvr := buildGVR(params.ResourceGroup, params.ResourceVersion, params.ResourcePlural)
		hl.Extra = []slog.Attr{
			slog.String("gvr", gvr),
		}

		if parseErr != nil {
			hl.StatusCode = parseErr.status
			hl.Err = fmt.Errorf("%s", parseErr.msg)
			response.Encode(w, response.New(parseErr.status, fmt.Errorf("%s", parseErr.msg)))
			return
		}

		// --- Phase 2: Discovery ---
		// Find distinct (group, resource, namespace) tuples matching the
		// request filters. These are the targets we need to RBAC-check.
		discoverStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		discovered, err := sql.DiscoverTargets(queryCtx, db, params)
		hl.addPhase("2_discovery", time.Since(discoverStart))
		if err != nil {
			log.Debug("discovery error", slog.Any("err", err), slog.String("trace_id", traceID))
			hl.Err = err
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// No matching resources exist: return empty result (not 404).
		if len(discovered) == 0 {
			log.Debug("discovery found no targets", slog.String("gvr", gvr), slog.String("trace_id", traceID))
			hl.StatusCode = http.StatusOK
			writeJSON(w, log, traceID, emptyListResult)
			return
		}
		log.Debug("discovery found targets", slog.Int("count", len(discovered)), slog.String("gvr", gvr), slog.String("trace_id", traceID))

		// --- Phase 3: RBAC authorization (batch) ---
		rbacStart := time.Now()
		allowed := auth.FilterAllowed(r.Context(), discovered)
		hl.addPhase("3_rbac_authz", time.Since(rbacStart))

		if len(allowed) == 0 {
			log.Debug("RBAC filter excluded all targets", slog.String("gvr", gvr), slog.String("trace_id", traceID))
			hl.StatusCode = http.StatusForbidden
			response.Encode(w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
			return
		}
		log.Debug("RBAC filter allowed some targets", slog.Int("allowed_count", len(allowed)), slog.Int("discovered_count", len(discovered)), slog.String("gvr", gvr), slog.String("trace_id", traceID))

		// Inject allowed targets into params for the list query.
		params.AllowedTargets = allowed

		// --- Phase 4: DB query execution ---
		queryStart := time.Now()
		result, err := sql.ListResources(queryCtx, db, params)
		hl.addPhase("4_query", time.Since(queryStart))
		if err != nil {
			log.Debug("list query error", slog.Any("err", err), slog.String("trace_id", traceID))
			hl.Err = err
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// --- Phase 5: Response serialization ---
		serStart := time.Now()
		if _, err := writeJSON(w, log, traceID, result); err != nil {
			log.Debug("response serialization error", slog.Any("err", err), slog.String("trace_id", traceID))
			hl.Err = fmt.Errorf("serialize: %w", err)
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("response serialization failed"))
			hl.addPhase("5_serialize", time.Since(serStart))
			return
		}

		hl.StatusCode = http.StatusOK
		hl.RowsReturned = len(result.Items)
		hl.addPhase("5_serialize", time.Since(serStart))
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
		ctx := xcontext.BuildContext(r.Context())
		traceID := xcontext.TraceId(ctx, false)

		hl := newHandlerLogger(log, r, "resource_detail", traceID)
		defer hl.emit()

		if r.Method != http.MethodGet {
			hl.StatusCode = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "GET")
			response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
			return
		}

		// --- Phase 1: Parse path parameter ---
		parseStart := time.Now()

		globalUID := r.PathValue("global_uid")
		if globalUID == "" {
			hl.addPhase("1_parse", time.Since(parseStart))
			hl.StatusCode = http.StatusBadRequest
			response.Encode(w, response.New(http.StatusBadRequest, fmt.Errorf("global_uid path parameter is required")))
			return
		}

		// raw defaults to true for the detail endpoint.
		includeRaw := r.URL.Query().Get("raw") != "false"

		hl.addPhase("1_parse", time.Since(parseStart))
		hl.Extra = []slog.Attr{
			slog.String("global_uid", globalUID),
		}

		// --- Phase 2: DB query ---
		queryStart := time.Now()

		queryCtx, queryCancel := context.WithTimeout(r.Context(), queryTimeout)
		defer queryCancel()

		result, err := sql.GetByGlobalUID(queryCtx, db, globalUID, includeRaw)
		hl.addPhase("2_query", time.Since(queryStart))
		if err != nil {
			log.Debug("DB query error", slog.Any("err", err), slog.String("global_uid", globalUID), slog.String("trace_id", traceID))
			hl.Err = err
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		if result.Count == 0 {
			log.Debug("resource not found", slog.String("global_uid", globalUID), slog.String("trace_id", traceID))
			hl.StatusCode = http.StatusNotFound
			response.Encode(w, response.New(http.StatusNotFound, fmt.Errorf("resource not found: %s", globalUID)))
			return
		}
		if result.Count > 1 {
			log.Error("unexpected multiple results for global_uid", slog.String("global_uid", globalUID), slog.Int("count", result.Count), slog.String("trace_id", traceID))
			hl.Err = fmt.Errorf("unexpected multiple results for global_uid %s", globalUID)
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("internal server error"))
			return
		}

		// --- Phase 3: RBAC authorization ---
		rbacStart := time.Now()
		item := result.Items[0]
		targets := []sql.ResourceTarget{
			// Even though this is a single resource, we still RBAC-check at the resource level (group/resource/namespace)
			// and not at the individual object level.
			{Group: item.Group, Resource: item.Resource, Namespace: item.Namespace},
		}
		allowed := auth.FilterAllowed(r.Context(), targets)
		hl.addPhase("3_rbac_authz", time.Since(rbacStart))

		if len(allowed) == 0 {
			log.Debug("RBAC denied access to resource", slog.String("global_uid", globalUID), slog.String("trace_id", traceID))
			hl.StatusCode = http.StatusForbidden
			response.Encode(w, response.New(http.StatusForbidden, fmt.Errorf("forbidden: insufficient permissions")))
			return
		}
		log.Debug("RBAC allowed access to resource", slog.String("global_uid", globalUID), slog.String("trace_id", traceID))

		// --- Phase 4: Serialize ---
		serStart := time.Now()
		if _, err := writeJSON(w, log, traceID, result); err != nil {
			log.Debug("response serialization error", slog.Any("err", err), slog.String("trace_id", traceID))
			hl.Err = fmt.Errorf("serialize: %w", err)
			hl.StatusCode = http.StatusInternalServerError
			response.InternalError(w, fmt.Errorf("response serialization failed"))
			hl.addPhase("4_serialize", time.Since(serStart))
			return
		}

		hl.StatusCode = http.StatusOK
		hl.addPhase("4_serialize", time.Since(serStart))
	}
}
