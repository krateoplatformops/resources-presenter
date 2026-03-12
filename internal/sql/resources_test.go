package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// baseCols returns the column names expected for raw=false queries.
func baseCols() []string {
	return []string{"resource_name", "namespace", "resource_group", "resource_version", "resource_kind", "resource_plural", "cluster_name", "created_at", "updated_at", "composition_id", "id"}
}

// rawCols returns the column names expected for raw=true queries.
func rawCols() []string {
	return append(baseCols(), "raw")
}

// --- Helpers to build common ListParams ---

func panelParams(limit int) ListParams {
	return ListParams{
		ResourceGroup:   "widgets.templates.krateo.io",
		ResourceVersion: "v1beta1",
		ResourcePlural:  "panels",
		Limit:           limit,
	}
}

func deploymentParams(limit int) ListParams {
	return ListParams{
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourcePlural:  "deployments",
		Limit:           limit,
	}
}

// panelRow builds an AddRow call for a Panel resource.
func panelRow(name, ns, cluster string, created, updated time.Time, compID *string, id int64) []any {
	return []any{name, ns, "widgets.templates.krateo.io", "v1beta1", "Panel", "panels", cluster, created, updated, compID, id}
}

// deploymentRow builds an AddRow call for a Deployment resource.
func deploymentRow(name, ns, cluster string, created, updated time.Time, compID *string, id int64) []any {
	return []any{name, ns, "apps", "v1", "Deployment", "deployments", cluster, created, updated, compID, id}
}

// --- panelArgs / deploymentArgs: the 3 required filter args ---
// WithArgs for panels: group, version, plural
func panelArgs(extra ...any) []any {
	args := []any{"widgets.templates.krateo.io", "v1beta1", "panels"}
	return append(args, extra...)
}

func deploymentArgs(extra ...any) []any {
	args := []any{"apps", "v1", "deployments"}
	return append(args, extra...)
}

func TestListResources_NoResults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs(51)...). // group, version, plural, limit+1
		WillReturnRows(pgxmock.NewRows(baseCols()))

	params := panelParams(50)

	result, err := ListResources(context.Background(), mock, params)
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
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(panelRow("panel-1", "default", "cluster-a", created, now, nil, int64(10))).
		AddRows(panelRow("panel-2", "default", "cluster-a", created, now.Add(-time.Minute), nil, int64(9)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs(51)...).
		WillReturnRows(rows)

	params := panelParams(50)

	result, err := ListResources(context.Background(), mock, params)
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
	if item.Group != "widgets.templates.krateo.io" {
		t.Errorf("expected group widgets.templates.krateo.io, got %s", item.Group)
	}
	if item.Version != "v1beta1" {
		t.Errorf("expected version v1beta1, got %s", item.Version)
	}
	if item.Kind != "Panel" {
		t.Errorf("expected kind Panel, got %s", item.Kind)
	}
	if item.Resource != "panels" {
		t.Errorf("expected resource panels, got %s", item.Resource)
	}
	if item.ClusterName != "cluster-a" {
		t.Errorf("expected cluster_name cluster-a, got %s", item.ClusterName)
	}
	if !item.CreatedAt.Equal(created) {
		t.Errorf("expected created_at %v, got %v", created, item.CreatedAt)
	}
	if !item.UpdatedAt.Equal(now) {
		t.Errorf("expected updated_at %v, got %v", now, item.UpdatedAt)
	}
	if item.CompositionID != nil {
		t.Errorf("expected nil composition_id, got %v", item.CompositionID)
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
	created := now.Add(-24 * time.Hour)
	limit := 2

	// Return limit+1 = 3 rows to indicate there's a next page
	rows := pgxmock.NewRows(baseCols()).
		AddRows(panelRow("panel-1", "default", "cluster-a", created, now, nil, int64(30))).
		AddRows(panelRow("panel-2", "default", "cluster-a", created, now.Add(-time.Second), nil, int64(20))).
		AddRows(panelRow("panel-3", "default", "cluster-a", created, now.Add(-2*time.Second), nil, int64(10)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs(limit+1)...).
		WillReturnRows(rows)

	params := panelParams(limit)

	result, err := ListResources(context.Background(), mock, params)
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
	created := now.Add(-24 * time.Hour)
	rawObj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "nginx", "namespace": "prod"},
	}
	rawJSON, _ := json.Marshal(rawObj)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, rawJSON)
	rows := pgxmock.NewRows(rawCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("prod", 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.Namespace = "prod"
	params.Raw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.Group != "apps" {
		t.Errorf("expected group apps, got %s", item.Group)
	}
	if item.Version != "v1" {
		t.Errorf("expected version v1, got %s", item.Version)
	}
	if item.Kind != "Deployment" {
		t.Errorf("expected kind Deployment, got %s", item.Kind)
	}
	if item.Resource != "deployments" {
		t.Errorf("expected resource deployments, got %s", item.Resource)
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

	created := cursorTime.Add(-24 * time.Hour)
	rows := pgxmock.NewRows(baseCols()).
		AddRows(panelRow("panel-old", "default", "cluster-a", created, cursorTime.Add(-time.Minute), nil, int64(15)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs(cursorTime, cursorID, 51)...).
		WillReturnRows(rows)

	params := panelParams(50)
	params.Cursor = cursor

	result, err := ListResources(context.Background(), mock, params)
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

func TestBuildListQuery_MinimalFilters(t *testing.T) {
	p := deploymentParams(50)

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 4 args: group + version + plural + limit+1
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "apps" {
		t.Errorf("arg[0] = %v, want apps", args[0])
	}
	if args[1] != "v1" {
		t.Errorf("arg[1] = %v, want v1", args[1])
	}
	if args[2] != "deployments" {
		t.Errorf("arg[2] = %v, want deployments", args[2])
	}
	if args[3] != 51 {
		t.Errorf("arg[3] = %v, want 51", args[3])
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_AllFilters(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{UpdatedAt: cursorTime, ID: cursorID})

	p := deploymentParams(25)
	p.Cluster = "cluster-a"
	p.Namespace = "prod"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.Name = "nginx"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + cluster + namespace + composition_id + name + labels + since + cursor(2) + limit = 12
	if len(args) != 12 {
		t.Fatalf("expected 12 args, got %d: %v", len(args), args)
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

// --- ValidateCursor tests ---

func TestValidateCursor_Empty(t *testing.T) {
	if err := ValidateCursor(""); err != nil {
		t.Fatalf("expected nil for empty cursor, got %v", err)
	}
}

func TestValidateCursor_Valid(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	})
	if err := ValidateCursor(cursor); err != nil {
		t.Fatalf("expected nil for valid cursor, got %v", err)
	}
}

func TestValidateCursor_InvalidBase64(t *testing.T) {
	if err := ValidateCursor("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestValidateCursor_InvalidJSON(t *testing.T) {
	if err := ValidateCursor(EncodedCursor("bm90LWpzb24=")); err == nil {
		t.Fatal("expected error for invalid JSON inside cursor")
	}
}

// --- Filter-specific query builder tests ---

func TestBuildListQuery_ClusterFilter(t *testing.T) {
	p := deploymentParams(50)
	p.Cluster = "cluster-a"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + cluster + limit = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[3] != "cluster-a" {
		t.Errorf("arg[3] = %v, want cluster-a", args[3])
	}

	if !strings.Contains(query, "cluster_name = $4") {
		t.Errorf("expected cluster_name clause, got:\n%s", query)
	}
}

func TestBuildListQuery_CompositionIDFilter(t *testing.T) {
	p := deploymentParams(50)
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + composition_id + limit = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[3] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("arg[3] = %v, want UUID", args[3])
	}

	if !strings.Contains(query, "composition_id = $4") {
		t.Errorf("expected composition_id clause, got:\n%s", query)
	}
}

// --- New filter query builder tests ---

func TestBuildListQuery_NameFilter(t *testing.T) {
	p := deploymentParams(50)
	p.Name = "api"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + name_pattern + limit = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[3] != "%api%" {
		t.Errorf("arg[3] = %v, want %%api%%", args[3])
	}
	if !strings.Contains(query, "resource_name ILIKE") {
		t.Errorf("expected ILIKE clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_LabelsFilter(t *testing.T) {
	p := deploymentParams(50)
	p.Labels = `{"app":"nginx"}`

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + labels_json + limit = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[3] != `{"app":"nginx"}` {
		t.Errorf("arg[3] = %v, want labels JSON", args[3])
	}
	if !strings.Contains(query, "raw->'metadata'->'labels' @>") {
		t.Errorf("expected JSONB containment clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_SinceFilter(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	p := deploymentParams(50)
	p.Since = &since

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + since + limit = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if !args[3].(time.Time).Equal(since) {
		t.Errorf("arg[3] = %v, want %v", args[3], since)
	}
	if !strings.Contains(query, "updated_at >= ") {
		t.Errorf("expected updated_at >= clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_AllNewFilters(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{UpdatedAt: cursorTime, ID: cursorID})

	p := deploymentParams(25)
	p.Cluster = "cluster-a"
	p.Namespace = "prod"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.Name = "api"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + plural + cluster + namespace + composition_id + name + labels + since + cursor(2) + limit = 12
	if len(args) != 12 {
		t.Fatalf("expected 12 args, got %d: %v", len(args), args)
	}

	if !strings.Contains(query, "resource_name ILIKE") {
		t.Errorf("expected ILIKE clause, got:\n%s", query)
	}
	if !strings.Contains(query, "raw->'metadata'->'labels' @>") {
		t.Errorf("expected JSONB containment clause, got:\n%s", query)
	}
	if !strings.Contains(query, "updated_at >= ") {
		t.Errorf("expected updated_at >= clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

// --- ListResources with new filters through the full mock path ---

func TestListResources_NameFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("my-api-service", "prod", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("%api%", 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.Name = "api"

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Name != "my-api-service" {
		t.Errorf("expected name my-api-service, got %s", result.Items[0].Name)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_LabelsFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs(`{"app":"nginx"}`, 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.Labels = `{"app":"nginx"}`

	result, err := ListResources(context.Background(), mock, params)
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

func TestListResources_SinceFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs(since, 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.Since = &since

	result, err := ListResources(context.Background(), mock, params)
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

// TestBuildListQuery_PlaceholderIndexing verifies that $N placeholders
// are sequential and correctly numbered across all filters + cursor + limit.
func TestBuildListQuery_PlaceholderIndexing(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{UpdatedAt: cursorTime, ID: cursorID})

	p := deploymentParams(25)
	p.Cluster = "cluster-a"
	p.Namespace = "prod"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.Name = "api"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify placeholder numbering: $1...$12
	// $1=group, $2=version, $3=plural, (deleted_at IS NULL = no arg),
	// $4=cluster, $5=namespace, $6=composition_id,
	// $7=name, $8=labels, $9=since, $10/$11=cursor, $12=limit
	if !strings.Contains(query, "resource_group = $1") {
		t.Errorf("expected resource_group = $1, got:\n%s", query)
	}
	if !strings.Contains(query, "resource_version = $2") {
		t.Errorf("expected resource_version = $2, got:\n%s", query)
	}
	if !strings.Contains(query, "resource_plural = $3") {
		t.Errorf("expected resource_plural = $3, got:\n%s", query)
	}
	if !strings.Contains(query, "deleted_at IS NULL") {
		t.Errorf("expected deleted_at IS NULL, got:\n%s", query)
	}
	if !strings.Contains(query, "cluster_name = $4") {
		t.Errorf("expected cluster_name = $4, got:\n%s", query)
	}
	if !strings.Contains(query, "namespace = $5") {
		t.Errorf("expected namespace = $5, got:\n%s", query)
	}
	if !strings.Contains(query, "composition_id = $6") {
		t.Errorf("expected composition_id = $6, got:\n%s", query)
	}
	if !strings.Contains(query, "resource_name ILIKE $7") {
		t.Errorf("expected resource_name ILIKE $7, got:\n%s", query)
	}
	if !strings.Contains(query, "@> $8::jsonb") {
		t.Errorf("expected @> $8::jsonb, got:\n%s", query)
	}
	if !strings.Contains(query, "updated_at >= $9") {
		t.Errorf("expected updated_at >= $9, got:\n%s", query)
	}
	if !strings.Contains(query, "(updated_at, id) < ($10, $11)") {
		t.Errorf("expected cursor placeholders ($10, $11), got:\n%s", query)
	}
	if !strings.Contains(query, "LIMIT $12") {
		t.Errorf("expected LIMIT $12, got:\n%s", query)
	}
	if len(args) != 12 {
		t.Fatalf("expected 12 args, got %d", len(args))
	}

	t.Logf("query: %s", query)
}

func TestBuildListQuery_InvalidCursor(t *testing.T) {
	p := deploymentParams(50)
	p.Cursor = "!!!invalid!!!"

	_, _, err := buildListQuery(p)
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

	params := deploymentParams(50)
	params.Cursor = "!!!not-base64!!!"

	_, err = ListResources(context.Background(), mock, params)
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
		WithArgs(deploymentArgs(51)...).
		WillReturnError(fmt.Errorf("connection refused"))

	params := deploymentParams(50)

	_, err = ListResources(context.Background(), mock, params)
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
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("cluster-a", 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.Cluster = "cluster-a"

	result, err := ListResources(context.Background(), mock, params)
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
	created := now.Add(-24 * time.Hour)
	compID := "550e8400-e29b-41d4-a716-446655440000"

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("nginx", "prod", "cluster-a", created, now, &compID, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("550e8400-e29b-41d4-a716-446655440000", 51)...).
		WillReturnRows(rows)

	params := deploymentParams(50)
	params.CompositionID = "550e8400-e29b-41d4-a716-446655440000"

	result, err := ListResources(context.Background(), mock, params)
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
