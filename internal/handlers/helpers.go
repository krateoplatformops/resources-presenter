package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/krateoplatformops/resources-presenter/internal/sql"
)

// uuidRegex validates UUID format (RFC 4122).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Authorizer checks whether the current user is allowed to GET resources.
// It receives a list of discovered (group, resource, namespace) targets and
// returns only the targets the user is permitted to access.
type Authorizer interface {
	FilterAllowed(ctx context.Context, targets []sql.ResourceTarget) []sql.ResourceTarget
}

// --- Shared helpers ---

// durationMs converts a time.Duration to fractional milliseconds.
func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// normalizeNamespace applies Kubernetes API semantics to the namespace parameter:
//   - absent/empty → "default" (like kubectl)
//   - "*"          → "" (all namespaces, no filter in SQL layer)
//   - any other    → exact match (unchanged)
func normalizeNamespace(ns string) string {
	switch ns {
	case "":
		return "default"
	case "*":
		return ""
	default:
		return ns
	}
}

// Input length limits to prevent excessive memory allocation and slow queries.
const (
	maxStringParamLen = 256  // name, name_contains, cluster, resource, version, namespace
	maxLabelsLen      = 4096 // labels JSON string
)

// validateListParams runs shared validations on an already-populated ListParams.
// It checks input lengths, composition_id format, name/name_contains mutual exclusion, and cursor validity.
func validateListParams(p *sql.ListParams) error {
	// Length checks: reject oversized inputs before any further processing.
	for _, check := range []struct {
		val    string
		name   string
		maxLen int
	}{
		{p.ResourceGroup, "group", maxStringParamLen},
		{p.ResourceVersion, "version", maxStringParamLen},
		{p.ResourcePlural, "resource", maxStringParamLen},
		{p.Cluster, "cluster", maxStringParamLen},
		{p.Namespace, "namespace", maxStringParamLen},
		{p.Name, "name", maxStringParamLen},
		{p.NameContains, "name_contains", maxStringParamLen},
		{p.Labels, "labels", maxLabelsLen},
	} {
		if len(check.val) > check.maxLen {
			return fmt.Errorf("'%s' exceeds maximum length (%d)", check.name, check.maxLen)
		}
	}

	if p.CompositionID != "" && !uuidRegex.MatchString(p.CompositionID) {
		return fmt.Errorf("invalid composition_id: not a valid UUID")
	}
	if p.Name != "" && p.NameContains != "" {
		return fmt.Errorf("'name' and 'name_contains' are mutually exclusive")
	}
	if err := sql.ValidateSortBy(p.SortBy); err != nil {
		return err
	}
	if err := sql.ValidateSortOrder(p.SortOrder); err != nil {
		return err
	}
	if err := sql.ValidateCursor(p.Cursor); err != nil {
		return fmt.Errorf("invalid cursor: %w", err)
	}
	return nil
}

// buildGVR formats a group/version/resource string for logging.
func buildGVR(group, version, plural string) string {
	if group == "" {
		return ""
	}
	gvr := group
	if version != "" {
		gvr += "/" + version
	}
	if plural != "" {
		gvr += "." + plural
	}
	return gvr
}

// writeJSON marshals v to JSON and writes it to w. Returns the serialized
// bytes (for caller inspection) and any marshal error. Write errors are
// logged but not returned since the HTTP status is already committed.
func writeJSON(w http.ResponseWriter, log *slog.Logger, traceID string, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(data); werr != nil {
		log.Debug("client write error", slog.Any("err", werr), slog.String("trace_id", traceID))
	}
	return data, nil
}

// emptyListResult is a pre-built empty response to avoid repeated allocation.
var emptyListResult = &sql.ListResult{Count: 0, Items: []sql.ResourceItem{}}

// --- Deferred handler logging ---

// handlerLogger collects per-phase durations and emits a structured log on close.
type handlerLogger struct {
	log     *slog.Logger
	r       *http.Request
	start   time.Time
	handler string
	traceID string

	StatusCode   int
	RowsReturned int
	Err          error

	// Phases is an ordered list of (label, duration) pairs.
	// Each handler populates its own phases.
	Phases []phaseEntry

	// Extra attrs appended before the duration group (e.g. "gvr", "global_uid").
	Extra []slog.Attr
}

type phaseEntry struct {
	Label    string
	Duration time.Duration
}

func newHandlerLogger(log *slog.Logger, r *http.Request, handler, traceID string) *handlerLogger {
	return &handlerLogger{
		log:     log,
		r:       r,
		start:   time.Now(),
		handler: handler,
		traceID: traceID,
	}
}

// emit writes the structured log line. Called via defer.
func (h *handlerLogger) emit() {
	totalDuration := time.Since(h.start)

	lvl := slog.LevelDebug
	if h.StatusCode >= 500 {
		lvl = slog.LevelError
	} else if h.StatusCode >= 400 {
		lvl = slog.LevelWarn
	}

	attrs := []slog.Attr{
		slog.String("handler", h.handler),
		slog.String("method", h.r.Method),
	}
	attrs = append(attrs, h.Extra...)
	attrs = append(attrs,
		slog.String("trace_id", h.traceID),
		slog.Int("status_code", h.StatusCode),
	)
	if h.RowsReturned > 0 {
		attrs = append(attrs, slog.Int("rows_returned", h.RowsReturned))
	}

	// Build duration group from phases + total.
	phaseAttrs := make([]any, 0, len(h.Phases)+1)
	for _, p := range h.Phases {
		phaseAttrs = append(phaseAttrs, slog.Float64(p.Label, durationMs(p.Duration)))
	}
	totalLabel := fmt.Sprintf("%d_total", len(h.Phases)+1)
	phaseAttrs = append(phaseAttrs, slog.Float64(totalLabel, durationMs(totalDuration)))
	attrs = append(attrs, slog.Group("handler_duration_ms", phaseAttrs...))

	if h.Err != nil {
		attrs = append(attrs, slog.Any("err", h.Err))
	}

	h.log.LogAttrs(h.r.Context(), lvl, "request completed by handler", attrs...)
}

// addPhase records a named phase duration.
func (h *handlerLogger) addPhase(label string, d time.Duration) {
	h.Phases = append(h.Phases, phaseEntry{Label: label, Duration: d})
}
