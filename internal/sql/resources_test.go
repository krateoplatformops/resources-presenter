package sql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// timePtr returns a pointer to a time.Time value (test helper).
func timePtr(t time.Time) *time.Time { return &t }

// baseCols returns the column names expected for raw=false queries.
func baseCols() []string {
	return []string{"resource_name", "uid", "global_uid", "namespace", "resource_group", "resource_version", "resource_kind", "resource_plural", "cluster_name", "created_at", "updated_at", "composition_id", "id"}
}

// rawCols returns the column names expected for raw=true queries.
func rawCols() []string {
	return append(baseCols(), "raw")
}

// statusRawCols returns the column names expected for status_raw=true queries.
func statusRawCols() []string {
	return append(baseCols(), "status_raw")
}

// rawAndStatusRawCols returns the column names expected for raw=true + status_raw=true queries.
func rawAndStatusRawCols() []string {
	return append(rawCols(), "status_raw")
}

// detailCols returns the column names for GetByGlobalUID (raw + status_raw always included).
func detailCols() []string {
	return append(rawCols(), "status_raw")
}

// detailColsNoRaw returns the column names for GetByGlobalUID with includeRaw=false (status_raw still included).
func detailColsNoRaw() []string {
	return append(baseCols(), "status_raw")
}

// detailColsNoStatusRaw returns the column names for GetByGlobalUID with includeRaw=true, includeStatusRaw=false.
func detailColsNoStatusRaw() []string {
	return rawCols()
}

// detailColsMinimal returns the column names for GetByGlobalUID with includeRaw=false, includeStatusRaw=false.
func detailColsMinimal() []string {
	return baseCols()
}

// discoverCols returns the column names for DiscoverTargets queries.
func discoverCols() []string {
	return []string{"resource_group", "resource_plural", "namespace"}
}

// --- Helpers to build common ListParams ---

// panelParams creates ListParams for panels with a single AllowedTarget in the given namespace.
func panelParams(ns string, limit int) ListParams {
	return ListParams{
		ResourceGroup:   "widgets.templates.krateo.io",
		ResourceVersion: "v1beta1",
		ResourcePlural:  "panels",
		AllowedTargets: []ResourceTarget{
			{Group: "widgets.templates.krateo.io", Resource: "panels", Namespace: ns},
		},
		Limit: limit,
	}
}

// deploymentParams creates ListParams for deployments with a single AllowedTarget in the given namespace.
func deploymentParams(ns string, limit int) ListParams {
	return ListParams{
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourcePlural:  "deployments",
		AllowedTargets: []ResourceTarget{
			{Group: "apps", Resource: "deployments", Namespace: ns},
		},
		Limit: limit,
	}
}

// panelRow builds an AddRow call for a Panel resource.
func panelRow(name, ns, cluster string, created, updated time.Time, compID *string, id int64) []any {
	return []any{name, "uid-" + name, cluster + ":uid-" + name, ns, "widgets.templates.krateo.io", "v1beta1", "Panel", "panels", cluster, created, updated, compID, id}
}

// deploymentRow builds an AddRow call for a Deployment resource.
func deploymentRow(name, ns, cluster string, created, updated time.Time, compID *string, id int64) []any {
	return []any{name, "uid-" + name, cluster + ":uid-" + name, ns, "apps", "v1", "Deployment", "deployments", cluster, created, updated, compID, id}
}

// panelArgs builds expected query args: group, version, then IN(group, resource, namespace), then extra.
func panelArgs(ns string, extra ...any) []any {
	args := []any{"widgets.templates.krateo.io", "v1beta1", "widgets.templates.krateo.io", "panels", ns}
	return append(args, extra...)
}

// deploymentArgs builds expected query args: group, version, then IN(group, resource, namespace), then extra.
func deploymentArgs(ns string, extra ...any) []any {
	args := []any{"apps", "v1", "apps", "deployments", ns}
	return append(args, extra...)
}

// --- DiscoverTargets tests ---

func TestDiscoverTargets_GroupOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows(discoverCols()).
		AddRows([]any{"apps", "deployments", "default"}).
		AddRows([]any{"apps", "deployments", "prod"}).
		AddRows([]any{"apps", "statefulsets", "default"})

	mock.ExpectQuery("SELECT DISTINCT .+ FROM krateo_resources").
		WithArgs("apps").
		WillReturnRows(rows)

	p := ListParams{ResourceGroup: "apps"}
	targets, err := DiscoverTargets(context.Background(), mock, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(targets))
	}
	if targets[0].Group != "apps" || targets[0].Resource != "deployments" || targets[0].Namespace != "default" {
		t.Errorf("unexpected target[0]: %+v", targets[0])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTargets_GroupAndVersion(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows(discoverCols()).
		AddRows([]any{"apps", "deployments", "default"})

	mock.ExpectQuery("SELECT DISTINCT .+ FROM krateo_resources").
		WithArgs("apps", "v1").
		WillReturnRows(rows)

	p := ListParams{ResourceGroup: "apps", ResourceVersion: "v1"}
	targets, err := DiscoverTargets(context.Background(), mock, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTargets_AllFilters(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows(discoverCols()).
		AddRows([]any{"apps", "deployments", "prod"})

	// group, version, plural, cluster, namespace
	mock.ExpectQuery("SELECT DISTINCT .+ FROM krateo_resources").
		WithArgs("apps", "v1", "deployments", "cluster-a", "prod").
		WillReturnRows(rows)

	p := ListParams{
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		ResourcePlural:  "deployments",
		Cluster:         "cluster-a",
		Namespace:       "prod",
	}
	targets, err := DiscoverTargets(context.Background(), mock, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTargets_NoResults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT DISTINCT .+ FROM krateo_resources").
		WithArgs("nonexistent").
		WillReturnRows(pgxmock.NewRows(discoverCols()))

	p := ListParams{ResourceGroup: "nonexistent"}
	targets, err := DiscoverTargets(context.Background(), mock, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(targets))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTargets_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT DISTINCT .+ FROM krateo_resources").
		WithArgs("apps").
		WillReturnError(fmt.Errorf("connection refused"))

	p := ListParams{ResourceGroup: "apps"}
	_, err = DiscoverTargets(context.Background(), mock, p)
	if err == nil {
		t.Fatal("expected error for query failure")
	}
	if !strings.Contains(err.Error(), "discover") {
		t.Errorf("expected error to mention 'discover', got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- ListResources tests ---

func TestListResources_NoResults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs("default", 51)...).
		WillReturnRows(pgxmock.NewRows(baseCols()))

	params := panelParams("default", 50)

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
		WithArgs(panelArgs("default", 51)...).
		WillReturnRows(rows)

	params := panelParams("default", 50)

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.Name != "panel-1" {
		t.Errorf("expected name panel-1, got %s", item.Name)
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

	rows := pgxmock.NewRows(baseCols()).
		AddRows(panelRow("panel-1", "default", "cluster-a", created, now, nil, int64(30))).
		AddRows(panelRow("panel-2", "default", "cluster-a", created, now.Add(-time.Second), nil, int64(20))).
		AddRows(panelRow("panel-3", "default", "cluster-a", created, now.Add(-2*time.Second), nil, int64(10)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs("default", limit+1)...).
		WillReturnRows(rows)

	params := panelParams("default", limit)

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

	cur, err := DecodeCursor(result.Cursor)
	if err != nil {
		t.Fatalf("failed to decode cursor: %v", err)
	}
	if cur.ID != 20 {
		t.Errorf("expected cursor ID=20, got %d", cur.ID)
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

	params := deploymentParams("prod", 50)
	params.Raw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.Raw == nil {
		t.Fatal("expected raw to be populated")
	}

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
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: timePtr(cursorTime), ID: cursorID})

	created := cursorTime.Add(-24 * time.Hour)
	rows := pgxmock.NewRows(baseCols()).
		AddRows(panelRow("panel-old", "default", "cluster-a", created, cursorTime.Add(-time.Minute), nil, int64(15)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(panelArgs("default", cursorTime, cursorID, 51)...).
		WillReturnRows(rows)

	params := panelParams("default", 50)
	params.SortBy = SortByUpdatedAt
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

// --- buildListQuery tests ---

func TestBuildListQuery_MinimalFilters(t *testing.T) {
	p := deploymentParams("default", 50)

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + IN(group, resource, namespace) + limit = 2 + 3 + 1 = 6
	if len(args) != 6 {
		t.Fatalf("expected 6 args, got %d: %v", len(args), args)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_AllFilters(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: timePtr(cursorTime), ID: cursorID})

	p := deploymentParams("prod", 25)
	p.SortBy = SortByUpdatedAt
	p.Cluster = "cluster-a"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.NameContains = "nginx"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + IN(group, resource, namespace) + cluster + composition_id + name + labels + since + cursor(2) + limit = 13
	if len(args) != 13 {
		t.Fatalf("expected 13 args, got %d: %v", len(args), args)
	}

	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_MultipleAllowedTargets(t *testing.T) {
	p := ListParams{
		ResourceGroup:   "apps",
		ResourceVersion: "v1",
		AllowedTargets: []ResourceTarget{
			{Group: "apps", Resource: "deployments", Namespace: "default"},
			{Group: "apps", Resource: "deployments", Namespace: "prod"},
			{Group: "apps", Resource: "statefulsets", Namespace: "default"},
		},
		Limit: 50,
	}

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + IN(3 * 3) + limit = 2 + 9 + 1 = 12
	if len(args) != 12 {
		t.Fatalf("expected 12 args, got %d: %v", len(args), args)
	}

	if !strings.Contains(query, "IN") {
		t.Errorf("expected IN clause, got:\n%s", query)
	}

	t.Logf("query: %s", query)
}

func TestBuildListQuery_NoVersion(t *testing.T) {
	p := ListParams{
		ResourceGroup: "apps",
		AllowedTargets: []ResourceTarget{
			{Group: "apps", Resource: "deployments", Namespace: "default"},
		},
		Limit: 50,
	}

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + IN(group, resource, namespace) + limit = 1 + 3 + 1 = 5
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}

	t.Logf("query: %s", query)
}

// --- Cursor encode/decode tests ---

func TestCursorRoundtrip(t *testing.T) {
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	original := &ResourcesCursor{
		SortBy:    SortByUpdatedAt,
		UpdatedAt: &ts,
		ID:        42,
	}

	encoded := EncodeCursor(original)
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %d, want %d", decoded.ID, original.ID)
	}
	if decoded.SortBy != original.SortBy {
		t.Errorf("SortBy = %q, want %q", decoded.SortBy, original.SortBy)
	}
	if decoded.UpdatedAt == nil || !decoded.UpdatedAt.Equal(*original.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", decoded.UpdatedAt, original.UpdatedAt)
	}
}

func TestCursorEncodeNil(t *testing.T) {
	if encoded := EncodeCursor(nil); encoded != "" {
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
	if _, err := DecodeCursor("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestCursorDecodeInvalidJSON(t *testing.T) {
	if _, err := DecodeCursor(EncodedCursor("bm90LWpzb24=")); err == nil {
		t.Fatal("expected error for invalid JSON inside cursor")
	}
}

func TestValidateCursor_Empty(t *testing.T) {
	if err := ValidateCursor(""); err != nil {
		t.Fatalf("expected nil for empty cursor, got %v", err)
	}
}

func TestValidateCursor_Valid(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{
		SortBy:    SortByUpdatedAt,
		UpdatedAt: timePtr(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
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
	p := deploymentParams("default", 50)
	p.Cluster = "cluster-a"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + IN(group, resource, namespace) + cluster + limit = 2 + 3 + 1 + 1 = 7
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[5] != "cluster-a" {
		t.Errorf("arg[5] = %v, want cluster-a", args[5])
	}
	if !strings.Contains(query, "cluster_name = $6") {
		t.Errorf("expected cluster_name = $6, got:\n%s", query)
	}
}

func TestBuildListQuery_CompositionIDFilter(t *testing.T) {
	p := deploymentParams("default", 50)
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[5] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("arg[5] = %v, want UUID", args[5])
	}
	if !strings.Contains(query, "composition_id = $6") {
		t.Errorf("expected composition_id = $6, got:\n%s", query)
	}
}

func TestBuildListQuery_NameContainsFilter(t *testing.T) {
	p := deploymentParams("default", 50)
	p.NameContains = "api"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[5] != "%api%" {
		t.Errorf("arg[5] = %v, want %%api%%", args[5])
	}
	if !strings.Contains(query, "resource_name ILIKE") {
		t.Errorf("expected ILIKE clause, got:\n%s", query)
	}
}

func TestBuildListQuery_NameExactFilter(t *testing.T) {
	p := deploymentParams("default", 50)
	p.Name = "my-deployment"

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[5] != "my-deployment" {
		t.Errorf("arg[5] = %v, want my-deployment", args[5])
	}
	if !strings.Contains(query, "resource_name = ") {
		t.Errorf("expected exact match clause, got:\n%s", query)
	}
	if strings.Contains(query, "ILIKE") {
		t.Errorf("expected no ILIKE clause for exact match, got:\n%s", query)
	}
}

func TestBuildListQuery_LabelsFilter(t *testing.T) {
	p := deploymentParams("default", 50)
	p.Labels = `{"app":"nginx"}`

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[5] != `{"app":"nginx"}` {
		t.Errorf("arg[5] = %v, want labels JSON", args[5])
	}
	if !strings.Contains(query, "raw->'metadata'->'labels' @>") {
		t.Errorf("expected JSONB containment clause, got:\n%s", query)
	}
}

func TestBuildListQuery_SinceFilter(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	p := deploymentParams("default", 50)
	p.Since = &since

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if !args[5].(time.Time).Equal(since) {
		t.Errorf("arg[5] = %v, want %v", args[5], since)
	}
	if !strings.Contains(query, "updated_at >= ") {
		t.Errorf("expected updated_at >= clause, got:\n%s", query)
	}
}

func TestBuildListQuery_AllNewFilters(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: timePtr(cursorTime), ID: cursorID})

	p := deploymentParams("prod", 25)
	p.SortBy = SortByUpdatedAt
	p.Cluster = "cluster-a"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.NameContains = "api"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// group + version + IN(group, resource, namespace) + cluster + composition_id + name + labels + since + cursor(2) + limit = 13
	if len(args) != 13 {
		t.Fatalf("expected 13 args, got %d: %v", len(args), args)
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

// --- Placeholder indexing test ---

func TestBuildListQuery_PlaceholderIndexing(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursorID := int64(42)
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: timePtr(cursorTime), ID: cursorID})

	p := deploymentParams("prod", 25)
	p.SortBy = SortByUpdatedAt
	p.Cluster = "cluster-a"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.NameContains = "api"
	p.Labels = `{"app":"nginx"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// $1=group, $2=version, ($3,$4,$5)=IN(group,resource,ns), deleted_at IS NULL,
	// $6=cluster, $7=composition_id, $8=name, $9=labels,
	// $10=since, ($11,$12)=cursor, $13=limit
	checks := []struct {
		pattern string
	}{
		{"resource_group = $1"},
		{"resource_version = $2"},
		{"IN (($3, $4, $5))"},
		{"deleted_at IS NULL"},
		{"cluster_name = $6"},
		{"composition_id = $7"},
		{"resource_name ILIKE $8"},
		{"@> $9::jsonb"},
		{"updated_at >= $10"},
		{"(updated_at, id) < ($11, $12)"},
		{"LIMIT $13"},
	}
	for _, c := range checks {
		if !strings.Contains(query, c.pattern) {
			t.Errorf("expected %q in query, got:\n%s", c.pattern, query)
		}
	}
	if len(args) != 13 {
		t.Fatalf("expected 13 args, got %d", len(args))
	}

	t.Logf("query: %s", query)
}

func TestBuildListQuery_InvalidCursor(t *testing.T) {
	p := deploymentParams("default", 50)
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

	params := deploymentParams("default", 50)
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
		WithArgs(deploymentArgs("default", 51)...).
		WillReturnError(fmt.Errorf("connection refused"))

	params := deploymentParams("default", 50)

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
		AddRows(deploymentRow("nginx", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", "cluster-a", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
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
		AddRows(deploymentRow("nginx", "default", "cluster-a", created, now, &compID, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", "550e8400-e29b-41d4-a716-446655440000", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
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

func TestListResources_NameContainsFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("my-api-service", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", "%api%", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
	params.NameContains = "api"

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

func TestListResources_NameExactFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("my-api-service", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", "my-api-service", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
	params.Name = "my-api-service"

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Name != "my-api-service" {
		t.Errorf("expected name 'my-api-service', got %q", result.Items[0].Name)
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
		AddRows(deploymentRow("nginx", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", `{"app":"nginx"}`, 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
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
		AddRows(deploymentRow("nginx", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", since, 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
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

// --- Limit edge-case tests ---

func TestBuildListQuery_ZeroLimitUsesDefault(t *testing.T) {
	// When limit is 0 (or negative), the handler normalizes it to defaultLimit before
	// reaching the SQL layer. This test verifies that Limit=0 produces no LIMIT clause
	// (the SQL layer only adds LIMIT when Limit > 0).
	p := deploymentParams("default", 0)

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Limit=0 means the SQL layer does not add a LIMIT clause.
	if strings.Contains(query, "LIMIT") {
		t.Errorf("expected no LIMIT clause for Limit=0, got:\n%s", query)
	}

	t.Logf("query: %s", query)
}

// --- Context cancellation test ---

func TestListResources_CancelledContext(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	// Return rows, but the context will be cancelled before scanning.
	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("res-1", "default", "cluster-a", created, now, nil, int64(5))).
		AddRows(deploymentRow("res-2", "default", "cluster-a", created, now.Add(-time.Second), nil, int64(4)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", 51)...).
		WillReturnRows(rows)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before scanning.

	params := deploymentParams("default", 50)

	_, err = ListResources(ctx, mock, params)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	// The cancellation may be caught either by db.Query (wrapped as "query: context canceled")
	// or by our ctx.Err() check in the scan loop (raw context.Canceled). Both are correct.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled (possibly wrapped), got: %v", err)
	}
}

// --- UID field test ---

// --- GetByGlobalUID tests ---

func TestGetByGlobalUID_Found(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(detailCols()).
		AddRows(append(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5)), []byte(`{"kind":"Deployment"}`), nil))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.GlobalUID != "cluster-a:uid-nginx" {
		t.Errorf("expected global_uid=cluster-a:uid-nginx, got %q", item.GlobalUID)
	}
	if item.Name != "nginx" {
		t.Errorf("expected name=nginx, got %q", item.Name)
	}
	if item.Raw == nil {
		t.Error("expected raw to be populated when includeRaw=true")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-x:uid-missing").
		WillReturnRows(pgxmock.NewRows(detailCols()))

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-x:uid-missing", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 0 {
		t.Fatalf("expected count=0, got %d", result.Count)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result.Items))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_WithoutRaw(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, nil) // status_raw (NULL)
	rows := pgxmock.NewRows(detailColsNoRaw()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}
	if result.Items[0].Raw != nil {
		t.Error("expected raw to be nil when includeRaw=false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-fail").
		WillReturnError(fmt.Errorf("connection refused"))

	// includeRaw=true, but error happens before scanning so columns don't matter.
	_, err = GetByGlobalUID(context.Background(), mock, "cluster-a:uid-fail", true, true)
	if err == nil {
		t.Fatal("expected error for query failure")
	}
	if !strings.Contains(err.Error(), "get_by_global_uid") {
		t.Errorf("expected error to mention 'get_by_global_uid', got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_UidField(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	rows := pgxmock.NewRows(baseCols()).
		AddRows(deploymentRow("nginx", "default", "cluster-a", created, now, nil, int64(1)))

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Uid != "uid-nginx" {
		t.Errorf("expected uid=uid-nginx, got %q", result.Items[0].Uid)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// --- StatusRaw tests ---

func TestListResources_StatusRawTrue(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	statusObj := map[string]any{"phase": "Running", "replicas": 3}
	statusJSON, _ := json.Marshal(statusObj)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, statusJSON)
	rows := pgxmock.NewRows(statusRawCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("prod", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("prod", 50)
	params.StatusRaw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.StatusRaw == nil {
		t.Fatal("expected status_raw to be populated")
	}

	var parsed map[string]any
	if err := json.Unmarshal(item.StatusRaw, &parsed); err != nil {
		t.Fatalf("status_raw is not valid JSON: %v", err)
	}
	if parsed["phase"] != "Running" {
		t.Errorf("expected status_raw phase=Running, got %v", parsed["phase"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_StatusRawNull(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	// status_raw is NULL for this row.
	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, nil) // NULL status_raw
	rows := pgxmock.NewRows(statusRawCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("prod", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("prod", 50)
	params.StatusRaw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.StatusRaw != nil {
		t.Errorf("expected nil status_raw for NULL, got %s", string(item.StatusRaw))
	}

	// Verify it is omitted from JSON output.
	data, _ := json.Marshal(item)
	if strings.Contains(string(data), "status_raw") {
		t.Errorf("expected status_raw to be omitted from JSON when NULL, got: %s", string(data))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListResources_RawAndStatusRaw(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	rawObj := map[string]any{"kind": "Deployment"}
	rawJSON, _ := json.Marshal(rawObj)
	statusObj := map[string]any{"phase": "Running"}
	statusJSON, _ := json.Marshal(statusObj)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, rawJSON, statusJSON)
	rows := pgxmock.NewRows(rawAndStatusRawCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("prod", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("prod", 50)
	params.Raw = true
	params.StatusRaw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	if item.Raw == nil {
		t.Fatal("expected raw to be populated")
	}
	if item.StatusRaw == nil {
		t.Fatal("expected status_raw to be populated")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildListQuery_StatusRawColumn(t *testing.T) {
	p := deploymentParams("default", 50)
	p.StatusRaw = true

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "status_raw") {
		t.Errorf("expected status_raw in SELECT columns, got:\n%s", query)
	}
}

func TestBuildListQuery_RawAndStatusRawColumns(t *testing.T) {
	p := deploymentParams("default", 50)
	p.Raw = true
	p.StatusRaw = true

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, ", raw") {
		t.Errorf("expected raw in SELECT columns, got:\n%s", query)
	}
	if !strings.Contains(query, ", status_raw") {
		t.Errorf("expected status_raw in SELECT columns, got:\n%s", query)
	}
}

func TestGetByGlobalUID_StatusRawPopulated(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	statusObj := map[string]any{"phase": "Running"}
	statusJSON, _ := json.Marshal(statusObj)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, []byte(`{"kind":"Deployment"}`), statusJSON)
	rows := pgxmock.NewRows(detailCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}

	item := result.Items[0]
	if item.StatusRaw == nil {
		t.Fatal("expected status_raw to be populated in detail response")
	}
	var parsed map[string]any
	if err := json.Unmarshal(item.StatusRaw, &parsed); err != nil {
		t.Fatalf("status_raw is not valid JSON: %v", err)
	}
	if parsed["phase"] != "Running" {
		t.Errorf("expected phase=Running, got %v", parsed["phase"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_StatusRawNull(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, []byte(`{"kind":"Deployment"}`), nil) // NULL status_raw
	rows := pgxmock.NewRows(detailCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}

	item := result.Items[0]
	if item.Raw == nil {
		t.Fatal("expected raw to be populated")
	}
	if item.StatusRaw != nil {
		t.Errorf("expected nil status_raw for NULL, got %s", string(item.StatusRaw))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_NoRaw_StatusRawStillIncluded(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	statusObj := map[string]any{"phase": "Pending"}
	statusJSON, _ := json.Marshal(statusObj)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	row = append(row, statusJSON) // no raw, but status_raw is present
	rows := pgxmock.NewRows(detailColsNoRaw()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}

	item := result.Items[0]
	if item.Raw != nil {
		t.Error("expected raw to be nil when includeRaw=false")
	}
	if item.StatusRaw == nil {
		t.Fatal("expected status_raw to be populated even when includeRaw=false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_StatusRawDisabled(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	row := append(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5)), []byte(`{"kind":"Deployment"}`))
	rows := pgxmock.NewRows(detailColsNoStatusRaw()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}

	item := result.Items[0]
	if item.Raw == nil {
		t.Fatal("expected raw to be populated")
	}
	if item.StatusRaw != nil {
		t.Error("expected nil status_raw when includeStatusRaw=false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetByGlobalUID_BothDisabled(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	row := deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5))
	rows := pgxmock.NewRows(detailColsMinimal()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 1 {
		t.Fatalf("expected count=1, got %d", result.Count)
	}

	item := result.Items[0]
	if item.Raw != nil {
		t.Error("expected nil raw when includeRaw=false")
	}
	if item.StatusRaw != nil {
		t.Error("expected nil status_raw when includeStatusRaw=false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetByGlobalUID_DuplicateDetection verifies that when LIMIT 2 returns
// two rows (uniqueness violation), the result correctly reports count=2
// so the handler can detect and return 500.
// --- SortBy tests ---

func TestValidateSortBy_AllValid(t *testing.T) {
	for _, v := range []SortBy{"", SortByResource, SortByCreatedAt, SortByUpdatedAt, SortByGlobalUID, SortByCompositionID} {
		if err := ValidateSortBy(v); err != nil {
			t.Errorf("ValidateSortBy(%q) unexpected error: %v", v, err)
		}
	}
}

func TestValidateSortBy_Invalid(t *testing.T) {
	for _, v := range []SortBy{"invalid", "name", "gvrnn", "UPDATED_AT", " created_at"} {
		if err := ValidateSortBy(v); err == nil {
			t.Errorf("ValidateSortBy(%q) expected error, got nil", v)
		}
	}
}

func TestBuildListQuery_DefaultSortIsResource(t *testing.T) {
	p := deploymentParams("default", 50)
	// SortBy is zero-value (""), should default to SortByResource.

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "ORDER BY resource_group ASC") {
		t.Errorf("expected resource sort order, got:\n%s", query)
	}
}

func TestBuildListQuery_SortByUpdatedAt(t *testing.T) {
	p := deploymentParams("default", 50)
	p.SortBy = SortByUpdatedAt

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "ORDER BY updated_at DESC, id DESC") {
		t.Errorf("expected updated_at DESC sort, got:\n%s", query)
	}
}

func TestBuildListQuery_SortByCreatedAt(t *testing.T) {
	p := deploymentParams("default", 50)
	p.SortBy = SortByCreatedAt

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "ORDER BY created_at DESC, id DESC") {
		t.Errorf("expected created_at DESC sort, got:\n%s", query)
	}
}

func TestBuildListQuery_SortByGlobalUID(t *testing.T) {
	p := deploymentParams("default", 50)
	p.SortBy = SortByGlobalUID

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "ORDER BY global_uid ASC, id ASC") {
		t.Errorf("expected global_uid ASC sort, got:\n%s", query)
	}
}

func TestBuildListQuery_SortByCompositionID(t *testing.T) {
	p := deploymentParams("default", 50)
	p.SortBy = SortByCompositionID

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "ORDER BY composition_id ASC NULLS LAST, id ASC") {
		t.Errorf("expected composition_id ASC NULLS LAST sort, got:\n%s", query)
	}
}

func TestBuildListQuery_SortByResource(t *testing.T) {
	p := deploymentParams("default", 50)
	p.SortBy = SortByResource

	query, _, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "ORDER BY resource_group ASC, resource_version ASC, resource_plural ASC, namespace ASC, resource_name ASC, id ASC"
	if !strings.Contains(query, expected) {
		t.Errorf("expected resource sort order, got:\n%s", query)
	}
}

func TestBuildListQuery_CursorSortMismatch(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: timePtr(time.Now()), ID: 1})

	p := deploymentParams("default", 50)
	p.SortBy = SortByCreatedAt
	p.Cursor = cursor

	_, _, err := buildListQuery(p)
	if err == nil {
		t.Fatal("expected error for cursor sort mismatch")
	}
	if !strings.Contains(err.Error(), "sort_by") {
		t.Errorf("expected sort_by mismatch error, got: %v", err)
	}
}

func TestBuildListQuery_CursorCreatedAt(t *testing.T) {
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByCreatedAt, CreatedAt: &ts, ID: 42})

	p := deploymentParams("default", 50)
	p.SortBy = SortByCreatedAt
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "(created_at, id) < (") {
		t.Errorf("expected created_at cursor condition, got:\n%s", query)
	}
	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_CursorGlobalUID(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByGlobalUID, GlobalUID: "cluster-a:uid-100", ID: 100})

	p := deploymentParams("default", 50)
	p.SortBy = SortByGlobalUID
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "(global_uid, id) > (") {
		t.Errorf("expected global_uid cursor condition, got:\n%s", query)
	}
	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_CursorCompositionIDNonNull(t *testing.T) {
	compID := "550e8400-e29b-41d4-a716-446655440000"
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByCompositionID, CompositionID: &compID, ID: 42})

	p := deploymentParams("default", 50)
	p.SortBy = SortByCompositionID
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "composition_id >") {
		t.Errorf("expected composition_id > condition, got:\n%s", query)
	}
	if !strings.Contains(query, "composition_id IS NULL") {
		t.Errorf("expected NULL fallback in composition_id cursor, got:\n%s", query)
	}
	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_CursorCompositionIDNull(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{SortBy: SortByCompositionID, CompositionIDNull: true, ID: 42})

	p := deploymentParams("default", 50)
	p.SortBy = SortByCompositionID
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "composition_id IS NULL AND id >") {
		t.Errorf("expected NULL composition_id cursor condition, got:\n%s", query)
	}
	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestBuildListQuery_CursorResource(t *testing.T) {
	cursor := EncodeCursor(&ResourcesCursor{
		SortBy:    SortByResource,
		Group:     "apps",
		Version:   "v1",
		Resource:  "deployments",
		Namespace: "default",
		Name:      "nginx",
		ID:        42,
	})

	p := deploymentParams("default", 50)
	p.SortBy = SortByResource
	p.Cursor = cursor

	query, args, err := buildListQuery(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(query, "(resource_group, resource_version, resource_plural, namespace, resource_name, id) > (") {
		t.Errorf("expected resource cursor condition, got:\n%s", query)
	}
	t.Logf("query: %s", query)
	t.Logf("args: %v", args)
}

func TestCursorRoundtrip_AllSortTypes(t *testing.T) {
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	compID := "550e8400-e29b-41d4-a716-446655440000"

	tests := []struct {
		name   string
		cursor *ResourcesCursor
	}{
		{"updated_at", &ResourcesCursor{SortBy: SortByUpdatedAt, UpdatedAt: &ts, ID: 1}},
		{"created_at", &ResourcesCursor{SortBy: SortByCreatedAt, CreatedAt: &ts, ID: 2}},
		{"global_uid", &ResourcesCursor{SortBy: SortByGlobalUID, GlobalUID: "c:uid-1", ID: 3}},
		{"composition_id_non_null", &ResourcesCursor{SortBy: SortByCompositionID, CompositionID: &compID, ID: 4}},
		{"composition_id_null", &ResourcesCursor{SortBy: SortByCompositionID, CompositionIDNull: true, ID: 5}},
		{"resource", &ResourcesCursor{SortBy: SortByResource, Group: "apps", Version: "v1", Resource: "deployments", Namespace: "default", Name: "nginx", ID: 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeCursor(tt.cursor)
			decoded, err := DecodeCursor(encoded)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if decoded.SortBy != tt.cursor.SortBy {
				t.Errorf("SortBy = %q, want %q", decoded.SortBy, tt.cursor.SortBy)
			}
			if decoded.ID != tt.cursor.ID {
				t.Errorf("ID = %d, want %d", decoded.ID, tt.cursor.ID)
			}
		})
	}
}

func TestGetByGlobalUID_DuplicateDetection(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	row1 := append(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(5)), []byte(`{"kind":"Deployment"}`), nil)
	row2 := append(deploymentRow("nginx", "prod", "cluster-a", created, now, nil, int64(6)), []byte(`{"kind":"Deployment"}`), nil)
	rows := pgxmock.NewRows(detailCols()).AddRows(row1).AddRows(row2)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources WHERE global_uid").
		WithArgs("cluster-a:uid-nginx").
		WillReturnRows(rows)

	result, err := GetByGlobalUID(context.Background(), mock, "cluster-a:uid-nginx", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != 2 {
		t.Fatalf("expected count=2 for duplicate detection, got %d", result.Count)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
