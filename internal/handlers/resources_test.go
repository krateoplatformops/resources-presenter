package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krateoplatformops/resources-proxy/internal/registry"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testRegistry builds a registry that includes the resource kinds used in tests.
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.NewFromYAML([]byte(`
resources:
  - shortName: deployments
    apiVersion: apps/v1
    kind: Deployment
  - shortName: panels
    apiVersion: widgets.templates.krateo.io/v1beta1
    kind: Panel
`))
	if err != nil {
		t.Fatalf("testRegistry: %v", err)
	}
	return reg
}

// --- Integration test: multi-page pagination ---

func TestResourcesPagination_MultiPage(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:      "cluster-a",
		Namespace:    "default",
		ResourceKind: "apps/v1:Deployment",
		Count:        500,
		StartTime:    now,
		Delta:        time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	const pageSize = 100

	var (
		cursor string
		total  int
		page   int
	)

	for {
		resp := callResources(t, handler, "apps/v1:Deployment", cursor, pageSize)

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
		// This is correct with limit+1 detection — we fetched exactly limit
		// rows (not limit+1), so no next page is signaled.
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
		Cluster:      "cluster-a",
		Namespace:    "prod",
		ResourceKind: "apps/v1:Deployment",
		Count:        10,
		StartTime:    now,
		Delta:        time.Second,
	})
	seedResources(t, db, seedOptions{
		Cluster:      "cluster-b",
		Namespace:    "staging",
		ResourceKind: "apps/v1:Deployment",
		Count:        5,
		StartTime:    now.Add(-100 * time.Second),
		Delta:        time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	// Filter by cluster only.
	resp := callResourcesWithFilters(t, handler, "apps/v1:Deployment", map[string]string{
		"cluster": "cluster-a",
	})
	items := extractItems(t, resp)
	if len(items) != 10 {
		t.Fatalf("cluster filter: expected 10 items, got %d", len(items))
	}

	// Filter by namespace only.
	resp = callResourcesWithFilters(t, handler, "apps/v1:Deployment", map[string]string{
		"namespace": "staging",
	})
	items = extractItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("namespace filter: expected 5 items, got %d", len(items))
	}

	// Filter by both.
	resp = callResourcesWithFilters(t, handler, "apps/v1:Deployment", map[string]string{
		"cluster":   "cluster-a",
		"namespace": "prod",
	})
	items = extractItems(t, resp)
	if len(items) != 10 {
		t.Fatalf("cluster+namespace filter: expected 10 items, got %d", len(items))
	}

	// Non-matching filter returns empty list, not error.
	resp = callResourcesWithFilters(t, handler, "apps/v1:Deployment", map[string]string{
		"cluster": "cluster-nonexistent",
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
		Cluster:      "cluster-a",
		Namespace:    "default",
		ResourceKind: "apps/v1:Deployment",
		Count:        3,
		StartTime:    now,
		Delta:        time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	// Without raw: items should have no raw field.
	resp := callResources(t, handler, "apps/v1:Deployment", "", 50)
	items := extractItems(t, resp)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if _, ok := first["raw"]; ok {
		t.Fatal("expected no raw field when raw=false")
	}

	// With raw=true: items should include raw JSONB.
	resp = callResourcesWithFilters(t, handler, "apps/v1:Deployment", map[string]string{
		"raw": "true",
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

// --- Integration test: unknown resource_kind returns 404 with Kubernetes Status ---

func TestResourcesUnknownKind(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	req := httptest.NewRequest("GET", "/resources/unknown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Response must be JSON.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Kubernetes-style Status response from plumbing/http/response.
	if body["kind"] != "Status" {
		t.Fatalf("expected kind=Status, got %v", body["kind"])
	}
	if body["reason"] != "NotFound" {
		t.Fatalf("expected reason=NotFound, got %v", body["reason"])
	}
	msg, _ := body["message"].(string)
	if msg == "" {
		t.Fatal("expected non-empty message in Status response")
	}
}

// --- Integration test: short name resolution ---

func TestResourcesShortName(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	applySchema(t, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	seedResources(t, db, seedOptions{
		Cluster:      "cluster-a",
		Namespace:    "default",
		ResourceKind: "apps/v1:Deployment",
		Count:        5,
		StartTime:    now,
		Delta:        time.Second,
	})

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	// Use the short name "deployments" instead of the full "apps/v1:Deployment".
	resp := callResources(t, handler, "deployments", "", 50)
	items := extractItems(t, resp)
	if len(items) != 5 {
		t.Fatalf("short name resolution: expected 5 items, got %d", len(items))
	}
}

// --- Integration test: missing resource_kind returns 400 with Kubernetes Status ---

func TestResourcesMissingKind(t *testing.T) {
	db, cleanup := setupTestPostgres(t)
	defer cleanup()

	handler := ResourcesHandler(db, testLogger(), testRegistry(t))

	req := httptest.NewRequest("GET", "/resources/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Response must be JSON.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// Kubernetes-style Status response from plumbing/http/response.
	if body["kind"] != "Status" {
		t.Fatalf("expected kind=Status, got %v", body["kind"])
	}
	if body["reason"] != "BadRequest" {
		t.Fatalf("expected reason=BadRequest, got %v", body["reason"])
	}
}

// --- Unit test: parseRequest (no Docker needed) ---

func TestParseRequest(t *testing.T) {
	reg := testRegistry(t)

	tests := []struct {
		name       string
		url        string
		wantStatus int    // 0 means success (no error)
		wantKind   string // expected resolved DBKind on success
	}{
		{
			name:     "valid short name",
			url:      "/resources/deployments",
			wantKind: "apps/v1:Deployment",
		},
		{
			name:     "valid full format",
			url:      "/resources/apps/v1:Deployment",
			wantKind: "apps/v1:Deployment",
		},
		{
			name:       "unknown kind returns 404",
			url:        "/resources/unknown",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "empty path returns 400",
			url:        "/resources/",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:     "query params parsed correctly",
			url:      "/resources/deployments?cluster=c1&namespace=ns1&raw=true&limit=42",
			wantKind: "apps/v1:Deployment",
		},
		{
			name:       "invalid limit returns 400",
			url:        "/resources/deployments?limit=abc",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid composition_id returns 400",
			url:        "/resources/deployments?composition_id=not-a-uuid",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:     "valid composition_id accepted",
			url:      "/resources/deployments?composition_id=550e8400-e29b-41d4-a716-446655440000",
			wantKind: "apps/v1:Deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			params, _, herr := parseRequest(req, reg)

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
			if params.ResourceKind != tt.wantKind {
				t.Fatalf("expected ResourceKind=%q, got %q", tt.wantKind, params.ResourceKind)
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
			}
		})
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

	//TODO: align with real schema

	// krateo_resources schema — matches deviser/internal/config/assets/resources.schema.sql
	schema := `
CREATE TABLE IF NOT EXISTS krateo_resources (
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    id                BIGINT GENERATED ALWAYS AS IDENTITY,
    cluster_name      TEXT NOT NULL,
    uid               TEXT NOT NULL,
    global_uid        TEXT NOT NULL,
    namespace         TEXT NOT NULL,
    resource_kind     TEXT NOT NULL,
    resource_name     TEXT NOT NULL,
    composition_id    UUID NULL,
    raw               JSONB NOT NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_krateo_resources_global_uid
ON krateo_resources (global_uid);

CREATE INDEX IF NOT EXISTS idx_krateo_resources_kind_page
ON krateo_resources (resource_kind, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_krateo_resources_obj
ON krateo_resources (cluster_name, namespace, resource_kind, resource_name);
`
	_, err := db.Exec(context.Background(), schema)
	if err != nil {
		t.Fatal(err)
	}
}

type seedOptions struct {
	Cluster      string
	Namespace    string
	ResourceKind string // e.g. "apps/v1:Deployment"
	Count        int
	StartTime    time.Time
	Delta        time.Duration
}

func seedResources(t *testing.T, db *pgxpool.Pool, opt seedOptions) {
	t.Helper()

	for i := 0; i < opt.Count; i++ {
		uid := fmt.Sprintf("uid-%04d", i)
		name := fmt.Sprintf("res-%04d", i)
		updatedAt := opt.StartTime.Add(-time.Duration(i) * opt.Delta)

		raw := map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
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
	resource_kind,
	resource_name,
	raw
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
`,
			updatedAt,
			opt.Cluster,
			uid,
			opt.Cluster+":"+uid,
			opt.Namespace,
			opt.ResourceKind,
			name,
			rawJSON,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func callResources(
	t *testing.T,
	handler http.Handler,
	resourceKind string,
	cursor string,
	limit int,
) map[string]any {
	t.Helper()

	url := fmt.Sprintf("/resources/%s?limit=%d", resourceKind, limit)
	if cursor != "" {
		url += "&cursor=" + cursor
	}

	req := httptest.NewRequest("GET", url, nil)
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

func callResourcesWithFilters(
	t *testing.T,
	handler http.Handler,
	resourceKind string,
	filters map[string]string,
) map[string]any {
	t.Helper()

	url := fmt.Sprintf("/resources/%s?limit=200", resourceKind)
	for k, v := range filters {
		url += "&" + k + "=" + v
	}

	req := httptest.NewRequest("GET", url, nil)
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
