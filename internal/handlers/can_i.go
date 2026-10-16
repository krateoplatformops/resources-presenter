package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	xcontext "github.com/krateoplatformops/plumbing/context"
	"github.com/krateoplatformops/plumbing/http/response"
	"github.com/krateoplatformops/plumbing/kubeutil/rbac"
	"github.com/krateoplatformops/resources-presenter/internal/telemetry"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// maxChecks caps the permission checks in one request.
const maxChecks = 5000

// kubeVerbs are the Kubernetes API verbs accepted by POST /can-i.
var kubeVerbs = map[string]bool{
	"get": true, "list": true, "watch": true, "create": true, "update": true,
	"patch": true, "delete": true, "deletecollection": true,
}

// canICheck is one permission question. An empty namespace asks about all namespaces.
type canICheck struct {
	Verb      string `json:"verb"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
}

// canIPayload is the JSON body for POST /can-i.
type canIPayload struct {
	Checks []canICheck `json:"checks"`
}

type canIItem struct {
	canICheck
	Allowed bool `json:"allowed"`
}

type canIResult struct {
	Items []canIItem `json:"items"`
}

type canIHandler struct {
	log     *slog.Logger
	auth    PermissionChecker
	metrics *telemetry.Metrics
}

// CanIHandler returns an HTTP handler for POST /can-i.
// It answers, for each check in the request, whether the current user may
// perform it, like a batch of SelfSubjectAccessReviews:
//
//	{"checks": [{"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "dev"}]}
//	→ {"items": [{"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "dev", "allowed": true}]}
//
// Items are in request order. An RBAC error counts as not allowed.
func CanIHandler(log *slog.Logger, auth PermissionChecker, metrics *telemetry.Metrics) http.HandlerFunc {
	h := &canIHandler{log: log, auth: auth, metrics: metrics}
	return h.handle
}

func (h *canIHandler) handle(w http.ResponseWriter, r *http.Request) {
	traceID := xcontext.TraceId(r.Context(), false)

	hl := newHandlerLogger(h.log, r, "can_i", traceID, h.metrics)
	defer hl.emit()
	defer func() {
		status := hl.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		h.metrics.RecordHTTPRequest(r.Context(), hl.handler, r.Method, status, time.Since(hl.start))
	}()

	if r.Method != http.MethodPost {
		hl.StatusCode = http.StatusMethodNotAllowed
		h.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "method_not_allowed", http.StatusMethodNotAllowed)
		w.Header().Set("Allow", "POST")
		response.Encode(w, response.New(http.StatusMethodNotAllowed, fmt.Errorf("method not allowed")))
		return
	}

	// --- Phase 1: Request parsing / validation ---
	parseStart := time.Now()
	checks, err := parseCanIJSON(w, r)
	hl.addPhase("1_parse", time.Since(parseStart))
	if err != nil {
		hl.StatusCode = http.StatusBadRequest
		hl.Err = err
		h.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "invalid_params", http.StatusBadRequest)
		response.Encode(w, response.New(http.StatusBadRequest, fmt.Errorf("invalid parameters: %v", err)))
		return
	}
	hl.Extra = []slog.Attr{slog.Int("checks", len(checks))}

	// --- Phase 2: RBAC authorization ---
	rbacStart := time.Now()
	targets := make([]rbac.UserCanTarget, len(checks))
	for i, c := range checks {
		targets[i] = rbac.UserCanTarget{
			Verb:          c.Verb,
			GroupResource: schema.GroupResource{Group: c.Group, Resource: c.Resource},
			Namespace:     c.Namespace,
		}
	}
	allowed := h.auth.CanI(r.Context(), targets)
	hl.addPhase("2_rbac_authz", time.Since(rbacStart))

	// --- Phase 3: Response serialization ---
	serStart := time.Now()
	result := canIResult{Items: make([]canIItem, len(checks))}
	for i, c := range checks {
		result.Items[i] = canIItem{canICheck: c, Allowed: i < len(allowed) && allowed[i]}
	}
	if _, err := writeJSON(w, h.log, traceID, result); err != nil {
		hl.StatusCode = http.StatusInternalServerError
		hl.Err = fmt.Errorf("serialize: %w", err)
		h.metrics.IncHTTPError(r.Context(), hl.handler, r.Method, "serialize_error", http.StatusInternalServerError)
		response.InternalError(w, fmt.Errorf("response serialization failed"))
	} else {
		hl.StatusCode = http.StatusOK
	}
	hl.addPhase("3_serialize", time.Since(serStart))
}

func parseCanIJSON(w http.ResponseWriter, r *http.Request) ([]canICheck, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var payload canIPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&payload); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("empty JSON body")
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, fmt.Errorf("request body too large (max %d bytes)", maxBodySize)
		}
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON body: multiple JSON values")
	}

	if len(payload.Checks) == 0 {
		return nil, fmt.Errorf("field 'checks' is required and must not be empty")
	}
	if len(payload.Checks) > maxChecks {
		return nil, fmt.Errorf("field 'checks' exceeds maximum allowed (%d)", maxChecks)
	}
	for i, c := range payload.Checks {
		if !kubeVerbs[c.Verb] {
			return nil, fmt.Errorf("checks[%d]: 'verb' must be a Kubernetes API verb (got %q)", i, c.Verb)
		}
		if c.Resource == "" {
			return nil, fmt.Errorf("checks[%d]: 'resource' is required", i)
		}
		if len(c.Group) > maxStringParamLen || len(c.Resource) > maxStringParamLen || len(c.Namespace) > maxStringParamLen {
			return nil, fmt.Errorf("checks[%d]: 'group', 'resource' and 'namespace' must not exceed %d characters", i, maxStringParamLen)
		}
	}
	return payload.Checks, nil
}
