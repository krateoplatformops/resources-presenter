package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/krateoplatformops/resources-proxy/internal/access"
	"github.com/pashagolub/pgxmock/v4"
)

func TestListResources_NoResults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("widgets.templates.krateo.io/v1beta1:Panel", 51). // limit+1
		WillReturnRows(pgxmock.NewRows(cols))

	params := ListParams{
		ResourceKind: "widgets.templates.krateo.io/v1beta1:Panel",
		Limit:        50,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result.Items))
	}

	if result.Cursor != "" {
		t.Fatal("expected empty cursor for empty results")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_SinglePage(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("panel-1", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(10), now).
		AddRow("panel-2", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(9), now.Add(-time.Minute))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("widgets.templates.krateo.io/v1beta1:Panel", 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "widgets.templates.krateo.io/v1beta1:Panel",
		Limit:        50,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}

	// Verify DTO fields
	item := result.Items[0]
	if item.Name != "panel-1" {
		t.Errorf("expected name panel-1, got %s", item.Name)
	}
	if item.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", item.Namespace)
	}
	if item.APIVersion != "widgets.templates.krateo.io/v1beta1" {
		t.Errorf("expected apiVersion widgets.templates.krateo.io/v1beta1, got %s", item.APIVersion)
	}
	if item.Kind != "Panel" {
		t.Errorf("expected kind Panel, got %s", item.Kind)
	}
	if item.Raw != nil {
		t.Error("expected nil raw when raw=false")
	}

	if result.Cursor != "" {
		t.Fatal("expected empty cursor for single page")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_Paginated(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	limit := 2

	// Return limit+1 = 3 rows to indicate there's a next page
	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("panel-1", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(30), now).
		AddRow("panel-2", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(20), now.Add(-time.Second)).
		AddRow("panel-3", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(10), now.Add(-2*time.Second))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("widgets.templates.krateo.io/v1beta1:Panel", limit+1).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "widgets.templates.krateo.io/v1beta1:Panel",
		Limit:        limit,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != limit {
		t.Fatalf("expected %d items, got %d", limit, len(result.Items))
	}

	if result.Cursor == "" {
		t.Fatal("expected non-empty cursor for paginated results")
	}

	// Decode the opaque cursor and verify it points to the last returned item (panel-2, id=20)
	cur, err := DecodeCursor(result.Cursor)
	if err != nil {
		t.Fatalf("failed to decode cursor: %v", err)
	}
	if cur.ID != 20 {
		t.Errorf("expected cursor ID=20, got %d", cur.ID)
	}
	expectedTime := now.Add(-time.Second)
	if !cur.UpdatedAt.Equal(expectedTime) {
		t.Errorf("expected cursor UpdatedAt=%v, got %v", expectedTime, cur.UpdatedAt)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_RawTrue(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rawObj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "nginx", "namespace": "prod"},
	}
	rawJSON, _ := json.Marshal(rawObj)

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at", "raw"}
	rows := pgxmock.NewRows(cols).
		AddRow("nginx", "prod", "apps/v1:Deployment", int64(5), now, rawJSON)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("apps/v1:Deployment", "prod", 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Namespace:    "prod",
		Raw:          true,
		Limit:        50,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.APIVersion != "apps/v1" {
		t.Errorf("expected apiVersion apps/v1, got %s", item.APIVersion)
	}
	if item.Kind != "Deployment" {
		t.Errorf("expected kind Deployment, got %s", item.Kind)
	}
	if item.Raw == nil {
		t.Fatal("expected raw to be populated")
	}

	// Verify raw is valid JSON
	var parsed map[string]any
	if err := json.Unmarshal(item.Raw, &parsed); err != nil {
		t.Fatalf("raw is not valid JSON: %v", err)
	}
	if parsed["kind"] != "Deployment" {
		t.Errorf("expected raw kind=Deployment, got %v", parsed["kind"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_WithCursor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	cursorTime := time.Date(2026, 3, 1, 11, 59, 0, 0, time.UTC)
	cursorID := int64(20)
	cursor := EncodeCursor(&ResourcesCursor{UpdatedAt: cursorTime, ID: cursorID})

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("panel-old", "default", "widgets.templates.krateo.io/v1beta1:Panel", int64(15), cursorTime.Add(-time.Minute))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("widgets.templates.krateo.io/v1beta1:Panel", cursorTime, cursorID, 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "widgets.templates.krateo.io/v1beta1:Panel",
		Limit:        50,
		Cursor:       cursor,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	if result.Cursor != "" {
		t.Fatal("expected empty cursor for last page")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSplitResourceKind(t *testing.T) {
	tests := []struct {
		input      string
		apiVersion string
		kind       string
	}{
		{"apps/v1:Deployment", "apps/v1", "Deployment"},
		{"widgets.templates.krateo.io/v1beta1:Panel", "widgets.templates.krateo.io/v1beta1", "Panel"},
		{"v1:Pod", "v1", "Pod"},
		{"NoColon", "", "NoColon"},
	}

	for _, tt := range tests {
		apiVersion, kind := splitResourceKind(tt.input)
		if apiVersion != tt.apiVersion || kind != tt.kind {
			t.Errorf("splitResourceKind(%q) = (%q, %q), want (%q, %q)",
				tt.input, apiVersion, kind, tt.apiVersion, tt.kind)
		}
	}
}

func TestBuildListQuery_MinimalFilters(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}

	query, args, err := buildListQuery(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 2 args: resource_kind + limit+1
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "apps/v1:Deployment" {
		t.Errorf("arg[0] = %v, want apps/v1:Deployment", args[0])
	}
	if args[1] != 51 {
		t.Errorf("arg[1] = %v, want 51", args[1])
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_AllFilters(t *testing.T) {
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{UpdatedAt: cursorTime, ID: cursorID})

	p := ListParams{
		ResourceKind:  "apps/v1:Deployment",
		Cluster:       "cluster-a",
		Namespace:     "prod",
		CompositionID: "550e8400-e29b-41d4-a716-446655440000",
		Raw:           true,
		Limit:         25,
		Cursor:        cursor,
	}

	query, args, err := buildListQuery(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + cluster + namespace + composition_id + cursor(2) + limit = 7
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

// --- Cursor encode/decode tests ---

func TestCursorRoundtrip(t *testing.T) {
	original := &ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	}

	encoded := EncodeCursor(original)
	if encoded == "" {
		t.Fatal("expected non-empty encoded cursor")
	}

	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %d, want %d", decoded.ID, original.ID)
	}
	if !decoded.UpdatedAt.Equal(original.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", decoded.UpdatedAt, original.UpdatedAt)
	}
}

func TestCursorEncodeNil(t *testing.T) {
	encoded := EncodeCursor(nil)
	if encoded != "" {
		t.Fatalf("expected empty string for nil cursor, got %q", encoded)
	}
}

func TestCursorDecodeEmpty(t *testing.T) {
	decoded, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded != nil {
		t.Fatalf("expected nil for empty cursor, got %+v", decoded)
	}
}

func TestCursorDecodeInvalidBase64(t *testing.T) {
	_, err := DecodeCursor("!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestCursorDecodeInvalidJSON(t *testing.T) {
	// Valid base64 but not valid JSON ("not-json" in base64).
	_, err := DecodeCursor(EncodedCursor("bm90LWpzb24="))
	if err == nil {
		t.Fatal("expected error for invalid JSON inside cursor")
	}
}

// --- Access policy query builder tests ---

func TestBuildListQuery_WithPolicy_NamespacesOnly(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}
	policy := &access.Policy{
		AllowedNamespaces: []string{"prod", "staging"},
	}

	query, args, err := buildListQuery(p, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + allowed_namespaces + limit = 3
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}

	if !strings.Contains(query, "namespace = ANY") {
		t.Errorf("expected namespace = ANY clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_WithPolicy_ClustersOnly(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}
	policy := &access.Policy{
		AllowedClusters: []string{"cluster-a"},
	}

	query, args, err := buildListQuery(p, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + allowed_clusters + limit = 3
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}

	if !strings.Contains(query, "cluster_name = ANY") {
		t.Errorf("expected cluster_name = ANY clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_WithPolicy_Both(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}
	policy := &access.Policy{
		AllowedNamespaces: []string{"prod"},
		AllowedClusters:   []string{"cluster-a", "cluster-b"},
	}

	query, args, err := buildListQuery(p, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + allowed_namespaces + allowed_clusters + limit = 4
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}

	if !strings.Contains(query, "namespace = ANY") {
		t.Errorf("expected namespace = ANY clause, got:\n%s", query)
	}
	if !strings.Contains(query, "cluster_name = ANY") {
		t.Errorf("expected cluster_name = ANY clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

// --- Filter-specific query builder tests ---

func TestBuildListQuery_ClusterFilter(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Cluster:      "cluster-a",
		Limit:        50,
	}

	query, args, err := buildListQuery(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + cluster + limit = 3
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "cluster-a" {
		t.Errorf("arg[1] = %v, want cluster-a", args[1])
	}

	if !strings.Contains(query, "cluster_name = $2") {
		t.Errorf("expected cluster_name clause, got:\n%s", query)
	}
}

func TestBuildListQuery_CompositionIDFilter(t *testing.T) {
	p := ListParams{
		ResourceKind:  "apps/v1:Deployment",
		CompositionID: "550e8400-e29b-41d4-a716-446655440000",
		Limit:         50,
	}

	query, args, err := buildListQuery(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resource_kind + composition_id + limit = 3
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("arg[1] = %v, want UUID", args[1])
	}

	if !strings.Contains(query, "composition_id = $2") {
		t.Errorf("expected composition_id clause, got:\n%s", query)
	}
}

func TestBuildListQuery_InvalidCursor(t *testing.T) {
	p := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
		Cursor:       "!!!invalid!!!",
	}

	_, _, err := buildListQuery(p, nil)
	if err == nil {
		t.Fatal("expected error for invalid cursor")
	}
}

// --- Error propagation tests ---

func TestListResources_InvalidCursor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	params := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
		Cursor:       "!!!not-base64!!!",
	}

	_, err = ListResources(context.Background(), mock, params, nil)
	if err == nil {
		t.Fatal("expected error for invalid cursor")
	}

	if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("expected error to mention 'cursor', got: %v", err)
	}
}

func TestListResources_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("apps/v1:Deployment", 51).
		WillReturnError(fmt.Errorf("connection refused"))

	params := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}

	_, err = ListResources(context.Background(), mock, params, nil)
	if err == nil {
		t.Fatal("expected error for query failure")
	}

	if !strings.Contains(err.Error(), "query") {
		t.Errorf("expected error to mention 'query', got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- ListResources with filters through the full mock path ---

func TestListResources_ClusterFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("nginx", "prod", "apps/v1:Deployment", int64(1), now)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("apps/v1:Deployment", "cluster-a", 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Cluster:      "cluster-a",
		Limit:        50,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_CompositionIDFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("nginx", "prod", "apps/v1:Deployment", int64(1), now)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("apps/v1:Deployment", "550e8400-e29b-41d4-a716-446655440000", 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind:  "apps/v1:Deployment",
		CompositionID: "550e8400-e29b-41d4-a716-446655440000",
		Limit:         50,
	}

	result, err := ListResources(context.Background(), mock, params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_WithPolicy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	cols := []string{"resource_name", "namespace", "resource_kind", "id", "updated_at"}
	rows := pgxmock.NewRows(cols).
		AddRow("nginx", "prod", "apps/v1:Deployment", int64(1), now)

	policy := &access.Policy{
		AllowedNamespaces: []string{"prod", "staging"},
		AllowedClusters:   []string{"cluster-a"},
	}

	// resource_kind + allowed_namespaces + allowed_clusters + limit = 4
	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs("apps/v1:Deployment", []string{"prod", "staging"}, []string{"cluster-a"}, 51).
		WillReturnRows(rows)

	params := ListParams{
		ResourceKind: "apps/v1:Deployment",
		Limit:        50,
	}

	result, err := ListResources(context.Background(), mock, params, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
