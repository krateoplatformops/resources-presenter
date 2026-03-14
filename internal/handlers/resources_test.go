package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sqlpkg "github.com/krateoplatformops/resources-presenter/internal/sql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// --- Test authorizers ---

// allowAllAuthorizer always allows access (used by most tests).
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) FilterAllowed(ctx context.Context, targets []sqlpkg.ResourceTarget) []sqlpkg.ResourceTarget {
	return targets
}

// denyAllAuthorizer always denies access (used by RBAC denial tests).
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) FilterAllowed(ctx context.Context, targets []sqlpkg.ResourceTarget) []sqlpkg.ResourceTarget {
	return nil
}

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// resourceQuery builds a query string for /resources with the required group/version/resource
// plus any additional filters.
func resourceQuery(group, version, resource string, extra map[string]string) string {
	u := "/resources?group=" + url.QueryEscape(group) +
		"&version=" + url.QueryEscape(version) +
		"&resource=" + url.QueryEscape(resource)
	for k, v := range extra {
		u += "&" + k + "=" + v
	}
	return u
}

// Shorthand for the two resource types used in tests.
func deploymentQuery(extra map[string]string) string {
	return resourceQuery("apps", "v1", "deployments", extra)
}

func panelQuery(extra map[string]string) string {
	return resourceQuery("widgets.templates.krateo.io", "v1beta1", "panels", extra)
}

// --- Integration test: multi-page pagination ---

func TestResourcesPagination_MultiPage(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "default",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           500,
		StartTime:       now,
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	const pageSize = 100

	var (
		cursor string
		total  int
		page   int
	)

	for {
		extra := map[string]string{
			"limit":     fmt.Sprintf("%d", pageSize),
			"namespace": "default",
		}
		if cursor != "" {
			extra["cursor"] = cursor
		}
		resp := callResourcesGET(t, handler, "apps", "v1", "deployments", extra)

		items := extractItems(t, resp)

		cursorVal, _ := resp["cursor"].(string)

		t.Logf("page=%d items=%d cursor=%q", page, len(items), cursorVal)

		total += len(items)
		page++

		// Exit: empty page means we're done.
		if len(items) == 0 {
			if cursorVal != "" {
				t.Fatal("cursor must be empty on final empty page")
			}
			break
		}

		// Full page with cursor: more pages to come.
		if len(items) == pageSize && cursorVal != "" {
			cursor = cursorVal
			continue
		}

		// Full page without cursor: last page happened to be exactly full.
		// Partial page: also the last page.
		if cursorVal != "" {
			t.Fatalf("cursor must be empty on last page (items=%d)", len(items))
		}
		break
	}

	if total != 500 {
		t.Fatalf("expected 500 resources, got %d", total)
	}
}

// --- Integration test: namespace and cluster filters ---

func TestResourcesFilter_NamespaceAndCluster(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Seed resources across two clusters and two namespaces.
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           10,
		StartTime:       now,
		Delta:           time.Second,
	})
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-b",
		Namespace:       "staging",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           5,
		StartTime:       now.Add(-100 * time.Second),
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Filter by cluster + namespace (namespace is now required).
	resp := callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"cluster":   "cluster-a",
		"namespace": "prod",
	})
	items := extractItems(t, resp)
	if len(items) != 10 {
		t.Fatalf("cluster filter: expected 10 items, got %d", len(items))
	}

	// Filter by namespace only.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"namespace": "staging",
	})
	items = extractItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("namespace filter: expected 5 items, got %d", len(items))
	}

	// Filter by both.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"cluster":   "cluster-a",
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 10 {
		t.Fatalf("cluster+namespace filter: expected 10 items, got %d", len(items))
	}

	// Non-matching filter returns empty list, not error.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"cluster":   "cluster-nonexistent",
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 0 {
		t.Fatalf("nonexistent cluster: expected 0 items, got %d", len(items))
	}
}

// --- Integration test: raw=true returns JSONB payload ---

func TestResourcesRawFlag(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "default",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           3,
		StartTime:       now,
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Without raw: items should have no raw field.
	resp := callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"namespace": "default",
	})
	items := extractItems(t, resp)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if _, ok := first["raw"]; ok {
		t.Fatal("expected no raw field when raw=false")
	}

	// With raw=true: items should include raw JSONB.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"raw":       "true",
		"namespace": "default",
	})
	items = extractItems(t, resp)
	if len(items) != 3 {
		t.Fatalf("expected 3 items with raw, got %d", len(items))
	}
	first = items[0].(map[string]any)
	rawField, ok := first["raw"]
	if !ok {
		t.Fatal("expected raw field when raw=true")
	}
	rawMap, ok := rawField.(map[string]any)
	if !ok {
		t.Fatalf("raw field is not an object: %T", rawField)
	}
	if rawMap["kind"] != "Deployment" {
		t.Fatalf("expected raw.kind=Deployment, got %v", rawMap["kind"])
	}
}

// --- Integration test: missing group returns 400 ---

func TestResourcesMissingGroup(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// No params at all — group is required.
	req := httptest.NewRequest("GET", "/resources", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Integration test: RBAC denial returns 403 ---

func TestResourcesRBACDenied(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Seed resources so discovery finds targets before RBAC check.
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "default",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           3,
		StartTime:       now,
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), denyAllAuthorizer{})

	req := httptest.NewRequest("GET", deploymentQuery(map[string]string{"namespace": "default"}), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "forbidden") {
		t.Fatalf("expected error message to mention forbidden, got: %s", rec.Body.String())
	}
}

// --- Integration test: name search (case-insensitive partial match) ---

func TestResourcesFilter_Name(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Seed resources with varied names.
	seedResourcesWithLabels(t, db, seedLabelsOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		StartTime:       now,
		Delta:           time.Second,
		Resources: []seedResource{
			{Name: "my-api-service", Labels: map[string]string{"app": "api"}},
			{Name: "api-gateway", Labels: map[string]string{"app": "api"}},
			{Name: "frontend-app", Labels: map[string]string{"app": "frontend"}},
			{Name: "API-v2", Labels: map[string]string{"app": "api"}},
		},
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Search for "api" — should match "my-api-service", "api-gateway", "API-v2" (case-insensitive).
	resp := callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"name":      "api",
		"namespace": "prod",
	})
	items := extractItems(t, resp)
	if len(items) != 3 {
		t.Fatalf("name filter 'api': expected 3 items, got %d", len(items))
	}

	// Search for "frontend" — should match only "frontend-app".
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"name":      "frontend",
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 1 {
		t.Fatalf("name filter 'frontend': expected 1 item, got %d", len(items))
	}

	// Search for non-existing name returns empty.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"name":      "nonexistent",
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 0 {
		t.Fatalf("name filter 'nonexistent': expected 0 items, got %d", len(items))
	}
}

// --- Integration test: labels filter (JSONB containment) ---

func TestResourcesFilter_Labels(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResourcesWithLabels(t, db, seedLabelsOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		StartTime:       now,
		Delta:           time.Second,
		Resources: []seedResource{
			{Name: "nginx-1", Labels: map[string]string{"app": "nginx", "tier": "backend"}},
			{Name: "nginx-2", Labels: map[string]string{"app": "nginx", "tier": "frontend"}},
			{Name: "redis", Labels: map[string]string{"app": "redis", "tier": "backend"}},
		},
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Filter by single label.
	resp := callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"labels":    url.QueryEscape(`{"app":"nginx"}`),
		"namespace": "prod",
	})
	items := extractItems(t, resp)
	if len(items) != 2 {
		t.Fatalf("labels filter app=nginx: expected 2 items, got %d", len(items))
	}

	// Filter by two labels (AND — both must match).
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"labels":    url.QueryEscape(`{"app":"nginx","tier":"backend"}`),
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 1 {
		t.Fatalf("labels filter app=nginx+tier=backend: expected 1 item, got %d", len(items))
	}

	// Non-matching labels returns empty.
	resp = callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"labels":    url.QueryEscape(`{"app":"nonexistent"}`),
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 0 {
		t.Fatalf("labels filter nonexistent: expected 0 items, got %d", len(items))
	}
}

// --- Integration test: since filter ---

func TestResourcesFilter_Since(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Seed 10 resources, each 1 hour apart.
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           10,
		StartTime:       now,
		Delta:           time.Hour,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Since 5 hours ago: should get 5 resources (updated_at >= now-5h).
	sinceTime := now.Add(-4*time.Hour - 30*time.Minute) // between res-4 and res-5
	resp := callResourcesGET(t, handler, "apps", "v1", "deployments", map[string]string{
		"since":     sinceTime.Format(time.RFC3339),
		"namespace": "prod",
	})
	items := extractItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("since filter: expected 5 items, got %d", len(items))
	}
}

// --- Integration test: POST method support ---

func TestResourcesPOST_BasicQuery(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           5,
		StartTime:       now,
		Delta:           time.Second,
	})
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-b",
		Namespace:       "staging",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           3,
		StartTime:       now.Add(-100 * time.Second),
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// POST with cluster filter.
	resp := callResourcesPOST(t, handler, map[string]any{
		"group":     "apps",
		"version":   "v1",
		"resource":  "deployments",
		"cluster":   "cluster-a",
		"namespace": "prod",
	})
	items := extractItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("POST cluster filter: expected 5 items, got %d", len(items))
	}

	// POST with namespace filter.
	resp = callResourcesPOST(t, handler, map[string]any{
		"group":     "apps",
		"version":   "v1",
		"resource":  "deployments",
		"namespace": "staging",
	})
	items = extractItems(t, resp)
	if len(items) != 3 {
		t.Fatalf("POST namespace filter: expected 3 items, got %d", len(items))
	}

	// POST with raw=true.
	resp = callResourcesPOST(t, handler, map[string]any{
		"group":     "apps",
		"version":   "v1",
		"resource":  "deployments",
		"cluster":   "cluster-a",
		"namespace": "prod",
		"raw":       true,
		"limit":     2,
	})
	items = extractItems(t, resp)
	if len(items) != 2 {
		t.Fatalf("POST raw+limit: expected 2 items, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if _, ok := first["raw"]; !ok {
		t.Fatal("expected raw field in POST response with raw=true")
	}
}

func TestResourcesPOST_Pagination(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "prod",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           5,
		StartTime:       now,
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Page 1.
	resp := callResourcesPOST(t, handler, map[string]any{
		"group":     "apps",
		"version":   "v1",
		"resource":  "deployments",
		"cluster":   "cluster-a",
		"namespace": "prod",
		"limit":     2,
	})
	items := extractItems(t, resp)
	if len(items) != 2 {
		t.Fatalf("POST page 1: expected 2 items, got %d", len(items))
	}
	cursor, _ := resp["cursor"].(string)
	if cursor == "" {
		t.Fatal("expected cursor for page 1")
	}

	// Page 2 with cursor.
	resp = callResourcesPOST(t, handler, map[string]any{
		"group":     "apps",
		"version":   "v1",
		"resource":  "deployments",
		"cluster":   "cluster-a",
		"namespace": "prod",
		"limit":     2,
		"cursor":    cursor,
	})
	items = extractItems(t, resp)
	if len(items) != 2 {
		t.Fatalf("POST page 2: expected 2 items, got %d", len(items))
	}
}

func TestResourcesPOST_ValidationErrors(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "empty body returns 400",
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown field returns 400",
			body:       `{"group":"apps","version":"v1","resource":"deployments","namespace":"default","unknownField":"x"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{not valid json}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple JSON values returns 400",
			body:       `{"group":"apps","version":"v1","resource":"deployments","namespace":"default"}{"group":"apps","version":"v1","resource":"deployments","namespace":"default"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid composition_id returns 400",
			body:       `{"group":"apps","version":"v1","resource":"deployments","namespace":"default","composition_id":"not-a-uuid"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing group returns 400",
			body:       `{"cluster":"a","namespace":"default"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid cursor returns 400",
			body:       `{"group":"apps","version":"v1","resource":"deployments","namespace":"default","cursor":"!!!not-base64!!!"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body *bytes.Reader
			if tt.body == "" {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader([]byte(tt.body))
			}
			req := httptest.NewRequest("POST", "/resources", body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- Unit test: method not allowed ---

func TestResourcesMethodNotAllowed(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	for _, method := range []string{"PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(method, deploymentQuery(map[string]string{"namespace": "default"}), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: expected 405, got %d", method, rec.Code)
		}
	}
}

// --- Unit test: parseRequest (no Docker needed) ---

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantStatus int    // 0 means success (no error)
		wantGroup  string // expected group on success
		wantPlural string // expected plural on success
	}{
		{
			name:       "valid group/version/resource with namespace",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default",
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "valid panel resource with namespace",
			url:        "/resources?group=widgets.templates.krateo.io&version=v1beta1&resource=panels&namespace=default",
			wantGroup:  "widgets.templates.krateo.io",
			wantPlural: "panels",
		},
		{
			name:       "missing group returns 400",
			url:        "/resources?version=v1&resource=deployments&namespace=default",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty params returns 400",
			url:        "/resources",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "group only is valid",
			url:        "/resources?group=apps",
			wantGroup:  "apps",
		},
		{
			name:       "group + version is valid",
			url:        "/resources?group=apps&version=v1",
			wantGroup:  "apps",
		},
		{
			name:       "group + resource is valid",
			url:        "/resources?group=apps&resource=deployments",
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "query params parsed correctly",
			url:        "/resources?group=apps&version=v1&resource=deployments&cluster=c1&namespace=ns1&raw=true&limit=42&name=api",
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "invalid limit returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=abc",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid composition_id returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&composition_id=not-a-uuid",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid composition_id accepted",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&composition_id=550e8400-e29b-41d4-a716-446655440000",
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "invalid labels JSON returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&labels=not-json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid labels JSON accepted",
			url:        `/resources?group=apps&version=v1&resource=deployments&namespace=default&labels={"app":"nginx"}`,
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "invalid since returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&since=not-a-timestamp",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid since accepted",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&since=2026-03-01T00:00:00Z",
			wantGroup:  "apps",
			wantPlural: "deployments",
		},
		{
			name:       "invalid cursor (bad base64) returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&cursor=!!!not-base64!!!",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid cursor (bad JSON inside) returns 400",
			url:        "/resources?group=apps&version=v1&resource=deployments&namespace=default&cursor=bm90LWpzb24=",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			params, herr := parseRequest(req)

			if tt.wantStatus != 0 {
				if herr == nil {
					t.Fatalf("expected error with status %d, got nil", tt.wantStatus)
				}
				if herr.status != tt.wantStatus {
					t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, herr.status, herr.msg)
				}
				return
			}

			if herr != nil {
				t.Fatalf("unexpected error: %d %s", herr.status, herr.msg)
			}
			if params.ResourceGroup != tt.wantGroup {
				t.Fatalf("expected ResourceGroup=%q, got %q", tt.wantGroup, params.ResourceGroup)
			}
			if params.ResourcePlural != tt.wantPlural {
				t.Fatalf("expected ResourcePlural=%q, got %q", tt.wantPlural, params.ResourcePlural)
			}

			// Verify query params for the params test case.
			if tt.name == "query params parsed correctly" {
				if params.Cluster != "c1" {
					t.Fatalf("expected Cluster=c1, got %q", params.Cluster)
				}
				if params.Namespace != "ns1" {
					t.Fatalf("expected Namespace=ns1, got %q", params.Namespace)
				}
				if !params.Raw {
					t.Fatal("expected Raw=true")
				}
				if params.Limit != 42 {
					t.Fatalf("expected Limit=42, got %d", params.Limit)
				}
				if params.Name != "api" {
					t.Fatalf("expected Name=api, got %q", params.Name)
				}
			}
		})
	}
}

// --- Unit test: parseListParamsJSON (no Docker needed) ---

func TestParseListParamsJSON_OK(t *testing.T) {
	// Build a valid cursor for the test.
	validCursor := string(sqlpkg.EncodeCursor(&sqlpkg.ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	}))

	body := fmt.Sprintf(`{
		"group":"apps",
		"version":"v1",
		"resource":"deployments",
		"cluster":"cluster-a",
		"namespace":"prod",
		"composition_id":"550e8400-e29b-41d4-a716-446655440000",
		"name":"api",
		"labels":{"app":"payments","tier":"backend"},
		"since":"2026-03-01T00:00:00Z",
		"raw":true,
		"limit":42,
		"cursor":%q
	}`, validCursor)

	req := httptest.NewRequest("POST", "/resources", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := parseListParamsJSON(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ResourceGroup != "apps" {
		t.Fatalf("unexpected ResourceGroup: %q", got.ResourceGroup)
	}
	if got.ResourcePlural != "deployments" {
		t.Fatalf("unexpected ResourcePlural: %q", got.ResourcePlural)
	}
	if got.ResourceVersion != "v1" {
		t.Fatalf("unexpected ResourceVersion: %q", got.ResourceVersion)
	}
	if got.Cluster != "cluster-a" {
		t.Fatalf("unexpected Cluster: %q", got.Cluster)
	}
	if got.Namespace != "prod" {
		t.Fatalf("unexpected Namespace: %q", got.Namespace)
	}
	if got.CompositionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("unexpected CompositionID: %q", got.CompositionID)
	}
	if got.Name != "api" {
		t.Fatalf("unexpected Name: %q", got.Name)
	}
	if got.Labels == "" {
		t.Fatal("labels should not be empty")
	}
	if got.Since == nil || got.Since.Format(time.RFC3339) != "2026-03-01T00:00:00Z" {
		t.Fatalf("unexpected Since: %v", got.Since)
	}
	if !got.Raw {
		t.Fatal("expected Raw=true")
	}
	if got.Limit != 42 {
		t.Fatalf("unexpected Limit: %d", got.Limit)
	}
	if string(got.Cursor) != validCursor {
		t.Fatalf("unexpected Cursor: %q", got.Cursor)
	}
}

func TestParseListParamsJSON_DefaultLimit(t *testing.T) {
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(`{"group":"apps","version":"v1","resource":"deployments","namespace":"default","limit":0}`))
	got, err := parseListParamsJSON(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Limit != defaultLimit {
		t.Fatalf("expected default limit %d, got %d", defaultLimit, got.Limit)
	}
}

func TestParseListParamsJSON_UnknownField(t *testing.T) {
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(`{"group":"apps","version":"v1","resource":"deployments","namespace":"default","bad":"x"}`))
	_, err := parseListParamsJSON(req)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestParseListParamsJSON_EmptyBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(""))
	_, err := parseListParamsJSON(req)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestParseListParamsJSON_MultipleValues(t *testing.T) {
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(`{"group":"apps","version":"v1","resource":"deployments","namespace":"default"}{"group":"apps","version":"v1","resource":"deployments","namespace":"default"}`))
	_, err := parseListParamsJSON(req)
	if err == nil {
		t.Fatal("expected error for multiple JSON values")
	}
}

func TestParseListParamsJSON_GroupOnly(t *testing.T) {
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(`{"group":"apps"}`))
	got, err := parseListParamsJSON(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceGroup != "apps" {
		t.Fatalf("expected group=apps, got %q", got.ResourceGroup)
	}
	if got.ResourceVersion != "" {
		t.Fatalf("expected empty version, got %q", got.ResourceVersion)
	}
	if got.ResourcePlural != "" {
		t.Fatalf("expected empty plural, got %q", got.ResourcePlural)
	}
}

// --- Integration test: POST RBAC denied returns 403 ---

func TestResourcesPOST_RBACDenied(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Seed resources so discovery finds targets before RBAC check.
	seedResources(t, db, seedOptions{
		Cluster:         "cluster-a",
		Namespace:       "default",
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourceKind:    "Deployment",
		ResourcePlural:  "deployments",
		Count:           3,
		StartTime:       now,
		Delta:           time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), denyAllAuthorizer{})

	body := `{"group":"apps","version":"v1","resource":"deployments","namespace":"default"}`
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for RBAC denied in POST, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Helpers ---

func setupTestPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}

	connStr += "&sslmode=disable"

	db, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		db.Close()
		container.Terminate(ctx)
	}

	return db, cleanup
}

func applySchema(t *testing.T, db *pgxpool.Pool) {
	t.Helper()

	schema := `
CREATE TABLE IF NOT EXISTS krateo_resources (
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ NULL,
    id                BIGINT GENERATED ALWAYS AS IDENTITY,
    cluster_name      TEXT NOT NULL,
    uid               TEXT NOT NULL,
    global_uid        TEXT NOT NULL,
    namespace         TEXT NOT NULL,
    resource_group    TEXT NOT NULL,
    resource_version  TEXT NOT NULL,
    resource_kind     TEXT NOT NULL,
    resource_plural   TEXT NOT NULL,
    resource_name     TEXT NOT NULL,
    composition_id    UUID NULL,
    raw               JSONB NOT NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_krateo_resources_global_uid
ON krateo_resources (global_uid)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_gvr_page
ON krateo_resources (resource_group, resource_version, resource_kind, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_obj
ON krateo_resources (cluster_name, namespace, resource_group, resource_version, resource_kind, resource_name)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural
ON krateo_resources (resource_plural)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural_page
ON krateo_resources (resource_plural, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_composition
ON krateo_resources (composition_id, updated_at DESC, id DESC)
WHERE composition_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_labels
ON krateo_resources
USING GIN ((raw->'metadata'->'labels'))
WHERE deleted_at IS NULL;
`
	_, err := db.Exec(context.Background(), schema)
	if err != nil {
		t.Fatal(err)
	}
}

type seedOptions struct {
	Cluster         string
	Namespace       string
	ResourceGroup   string // e.g. "apps"
	ResourceVersion string // e.g. "v1"
	ResourceKind    string // e.g. "Deployment"
	ResourcePlural  string // e.g. "deployments"
	Count           int
	StartTime       time.Time
	Delta           time.Duration
}

func seedResources(t *testing.T, db *pgxpool.Pool, opt seedOptions) {
	t.Helper()

	for i := 0; i < opt.Count; i++ {
		uid := fmt.Sprintf("uid-%04d", i)
		name := fmt.Sprintf("res-%04d", i)
		updatedAt := opt.StartTime.Add(-time.Duration(i) * opt.Delta)

		raw := map[string]any{
			"apiVersion": opt.ResourceGroup + "/" + opt.ResourceVersion,
			"kind":       opt.ResourceKind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": opt.Namespace,
			},
		}
		rawJSON, _ := json.Marshal(raw)

		_, err := db.Exec(context.Background(), `
INSERT INTO krateo_resources (
	updated_at,
	cluster_name,
	uid,
	global_uid,
	namespace,
	resource_group,
	resource_version,
	resource_kind,
	resource_plural,
	resource_name,
	raw
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
`,
			updatedAt,
			opt.Cluster,
			uid,
			opt.Cluster+":"+uid,
			opt.Namespace,
			opt.ResourceGroup,
			opt.ResourceVersion,
			opt.ResourceKind,
			opt.ResourcePlural,
			name,
			rawJSON,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// callResourcesGET sends a GET request with group/version/resource and optional extra params.
func callResourcesGET(
	t *testing.T,
	handler http.Handler,
	group, version, resource string,
	extra map[string]string,
) map[string]any {
	t.Helper()

	u := resourceQuery(group, version, resource, extra)

	req := httptest.NewRequest("GET", u, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp
}

func extractItems(t *testing.T, resp map[string]any) []any {
	t.Helper()

	raw, ok := resp["items"]
	if !ok || raw == nil {
		return nil
	}

	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("items is not an array: %#v", raw)
	}

	return items
}

// callResourcesPOST sends a POST request with a JSON body.
func callResourcesPOST(
	t *testing.T,
	handler http.Handler,
	body map[string]any,
) map[string]any {
	t.Helper()

	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/resources", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp
}

// seedResource describes a single resource with a name and labels.
type seedResource struct {
	Name   string
	Labels map[string]string
}

// seedLabelsOptions is used by seedResourcesWithLabels.
type seedLabelsOptions struct {
	Cluster         string
	Namespace       string
	ResourceGroup   string
	ResourceVersion string
	ResourceKind    string
	ResourcePlural  string
	StartTime       time.Time
	Delta           time.Duration
	Resources       []seedResource
}

// seedResourcesWithLabels seeds resources with specific names and labels in the raw JSONB.
func seedResourcesWithLabels(t *testing.T, db *pgxpool.Pool, opt seedLabelsOptions) {
	t.Helper()

	for i, res := range opt.Resources {
		uid := fmt.Sprintf("uid-lbl-%04d", i)
		updatedAt := opt.StartTime.Add(-time.Duration(i) * opt.Delta)

		// Convert string labels to any for JSON.
		labels := make(map[string]any, len(res.Labels))
		for k, v := range res.Labels {
			labels[k] = v
		}

		raw := map[string]any{
			"apiVersion": opt.ResourceGroup + "/" + opt.ResourceVersion,
			"kind":       opt.ResourceKind,
			"metadata": map[string]any{
				"name":      res.Name,
				"namespace": opt.Namespace,
				"labels":    labels,
			},
		}
		rawJSON, _ := json.Marshal(raw)

		_, err := db.Exec(context.Background(), `
INSERT INTO krateo_resources (
	updated_at,
	cluster_name,
	uid,
	global_uid,
	namespace,
	resource_group,
	resource_version,
	resource_kind,
	resource_plural,
	resource_name,
	raw
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
`,
			updatedAt,
			opt.Cluster,
			uid,
			opt.Cluster+":"+uid,
			opt.Namespace,
			opt.ResourceGroup,
			opt.ResourceVersion,
			opt.ResourceKind,
			opt.ResourcePlural,
			res.Name,
			rawJSON,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}
