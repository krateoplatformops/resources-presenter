package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/krateoplatformops/resources-presenter/internal/sql"
)

// --- Request parsing ---

// handlerError carries an HTTP status and message for parse-phase errors.
type handlerError struct {
	status int
	msg    string
}

// parseRequest validates the request and extracts params from query string (GET) or JSON body (POST).
// The resource type is identified by the required group parameter; version, resource, and namespace
// are optional filters used to narrow the discovery query and subsequent RBAC checks.
func parseRequest(r *http.Request) (sql.ListParams, *handlerError) {
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
		return sql.ListParams{}, &handlerError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("invalid parameters: %v", err),
		}
	}

	return params, nil
}

func parseListParams(r *http.Request) (sql.ListParams, error) {
	q := r.URL.Query()

	// group is required; version, resource, namespace are optional filters.
	group := q.Get("group")
	if group == "" {
		return sql.ListParams{}, fmt.Errorf("query parameter 'group' is required")
	}

	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		return sql.ListParams{}, err
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

	p := sql.ListParams{
		ResourceGroup:   group,
		ResourceVersion: q.Get("version"),
		ResourcePlural:  q.Get("resource"),
		Cluster:         q.Get("cluster"),
		Namespace:       normalizeNamespace(q.Get("namespace")),
		CompositionID:   q.Get("composition_id"),
		Name:            q.Get("name"),
		NameContains:    q.Get("name_contains"),
		Labels:          labels,
		Since:           since,
		Raw:             q.Get("raw") == "true",
		StatusRaw:       q.Get("status_raw") == "true",
		Limit:           limit,
		Cursor:          sql.EncodedCursor(q.Get("cursor")),
	}

	if err := validateListParams(&p); err != nil {
		return sql.ListParams{}, err
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
	StatusRaw     *bool          `json:"status_raw"`
	Limit         *int           `json:"limit"`
	Cursor        string         `json:"cursor"`
}

// maxBodySize is the maximum allowed POST body size (1 MB).
// This prevents clients from sending arbitrarily large JSON bodies
// that could exhaust server memory.
const maxBodySize = 1 << 20

func parseListParamsJSON(r *http.Request) (sql.ListParams, error) {
	defer r.Body.Close()

	// Enforce body size limit before reading.
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodySize)

	var payload resourcesJSONPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // Reject unknown fields to prevent silent typos.

	if err := dec.Decode(&payload); err != nil {
		if err == io.EOF {
			return sql.ListParams{}, fmt.Errorf("empty JSON body")
		}
		// MaxBytesReader returns *http.MaxBytesError when the limit is exceeded.
		if err.Error() == "http: request body too large" {
			return sql.ListParams{}, fmt.Errorf("request body too large (max %d bytes)", maxBodySize)
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
		if limit <= 0 {
			return sql.ListParams{}, fmt.Errorf("limit must be a positive integer (got %d)", limit)
		}
		if limit > maxLimit {
			return sql.ListParams{}, fmt.Errorf("limit exceeds maximum allowed (%d)", maxLimit)
		}
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

	statusRaw := false
	if payload.StatusRaw != nil {
		statusRaw = *payload.StatusRaw
	}

	p := sql.ListParams{
		ResourceGroup:   payload.Group,
		ResourceVersion: payload.Version,
		ResourcePlural:  payload.Resource,
		Cluster:         payload.Cluster,
		Namespace:       normalizeNamespace(payload.Namespace),
		CompositionID:   payload.CompositionID,
		Name:            payload.Name,
		NameContains:    payload.NameContains,
		Labels:          labels,
		Since:           payload.Since,
		Raw:             raw,
		StatusRaw:       statusRaw,
		Limit:           limit,
		Cursor:          sql.EncodedCursor(payload.Cursor),
	}

	if err := validateListParams(&p); err != nil {
		return sql.ListParams{}, err
	}

	return p, nil
}

func parseLimit(v string) (int, error) {
	if v == "" || v == "null" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid limit: %w", err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer (got %d)", n)
	}
	if n > maxLimit {
		return 0, fmt.Errorf("limit exceeds maximum allowed (%d)", maxLimit)
	}
	return n, nil
}
