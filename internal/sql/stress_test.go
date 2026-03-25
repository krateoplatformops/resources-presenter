package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestListResources_MaxLimit verifies behavior at the maximum allowed limit (5000 rows).
func TestListResources_MaxLimit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	const limit = 5000
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	// Return exactly limit rows (no next page).
	rows := pgxmock.NewRows(baseCols())
	for i := 0; i < limit; i++ {
		rows.AddRows(deploymentRow(
			fmt.Sprintf("res-%05d", i), "default", "cluster-a",
			created, now.Add(-time.Duration(i)*time.Millisecond), nil, int64(limit-i),
		))
	}

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", limit+1)...).
		WillReturnRows(rows)

	params := deploymentParams("default", limit)

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != limit {
		t.Fatalf("expected %d items, got %d", limit, result.Count)
	}
	if result.Cursor != "" {
		t.Fatal("expected empty cursor when exactly limit rows returned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestListResources_MaxLimitWithNextPage verifies cursor is set when limit+1 rows exist.
func TestListResources_MaxLimitWithNextPage(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	const limit = 5000
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	// Return limit+1 rows to trigger next-page cursor.
	rows := pgxmock.NewRows(baseCols())
	for i := 0; i < limit+1; i++ {
		rows.AddRows(deploymentRow(
			fmt.Sprintf("res-%05d", i), "default", "cluster-a",
			created, now.Add(-time.Duration(i)*time.Millisecond), nil, int64(limit+1-i),
		))
	}

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", limit+1)...).
		WillReturnRows(rows)

	params := deploymentParams("default", limit)

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Count != limit {
		t.Fatalf("expected %d items, got %d", limit, result.Count)
	}
	if result.Cursor == "" {
		t.Fatal("expected non-empty cursor when more rows exist")
	}

	// Verify cursor points to the last returned item (default sort is SortByResource).
	cur, err := DecodeCursor(result.Cursor)
	if err != nil {
		t.Fatalf("cursor decode error: %v", err)
	}
	if cur.SortBy != SortByResource {
		t.Errorf("cursor SortBy=%q, want %q", cur.SortBy, SortByResource)
	}
	lastItem := result.Items[limit-1]
	if cur.Name != lastItem.Name {
		t.Errorf("cursor Name=%q, last item Name=%q", cur.Name, lastItem.Name)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestListResources_RawLargePayload verifies handling of large JSONB payloads.
func TestListResources_RawLargePayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	// Build a ~50KB raw JSON object.
	bigSpec := make(map[string]any)
	for i := 0; i < 500; i++ {
		bigSpec[fmt.Sprintf("field_%04d", i)] = fmt.Sprintf("value-%0100d", i)
	}
	rawObj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "big-resource", "namespace": "default"},
		"spec":       bigSpec,
	}
	rawJSON, _ := json.Marshal(rawObj)

	row := deploymentRow("big-resource", "default", "cluster-a", created, now, nil, int64(1))
	row = append(row, rawJSON)
	rows := pgxmock.NewRows(rawCols()).AddRows(row)

	mock.ExpectQuery("SELECT .+ FROM krateo_resources").
		WithArgs(deploymentArgs("default", 51)...).
		WillReturnRows(rows)

	params := deploymentParams("default", 50)
	params.Raw = true

	result, err := ListResources(context.Background(), mock, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Raw == nil {
		t.Fatal("expected raw to be populated")
	}
	if len(result.Items[0].Raw) < 40000 {
		t.Fatalf("expected large raw payload, got %d bytes", len(result.Items[0].Raw))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestListResources_ConcurrentAccess verifies ListResources is safe for concurrent use.
func TestListResources_ConcurrentAccess(t *testing.T) {
	const goroutines = 50

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			mock, err := pgxmock.NewPool()
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: mock create: %w", id, err)
				return
			}
			defer mock.Close()

			rowCount := 10 + (id % 20) // vary between 10–29 rows
			rows := pgxmock.NewRows(baseCols())
			for j := 0; j < rowCount; j++ {
				rows.AddRows(deploymentRow(
					fmt.Sprintf("res-%d-%04d", id, j), "default", "cluster-a",
					created, now.Add(-time.Duration(j)*time.Second), nil, int64(j+1),
				))
			}

			mock.ExpectQuery("SELECT .+ FROM krateo_resources").
				WithArgs(deploymentArgs("default", 51)...).
				WillReturnRows(rows)

			params := deploymentParams("default", 50)

			result, err := ListResources(context.Background(), mock, params)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", id, err)
				return
			}

			if result.Count != rowCount {
				errs <- fmt.Errorf("goroutine %d: expected %d items, got %d", id, rowCount, result.Count)
				return
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				errs <- fmt.Errorf("goroutine %d: expectations: %w", id, err)
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestBuildListQuery_AllFilterCombinations exhaustively tests that the builder
// generates valid SQL for all 2^5 = 32 combinations of optional filters.
// SortBy is fixed to SortByUpdatedAt for this test.
func TestBuildListQuery_AllFilterCombinations(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursor := EncodeCursor(&ResourcesCursor{
		SortBy:    SortByUpdatedAt,
		SortOrder: SortOrderDesc,
		UpdatedAt: timePtr(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
		ID:        42,
	})

	type filter struct {
		name  string
		apply func(p *ListParams)
		args  int // number of args this filter adds
	}

	filters := []filter{
		{"cluster", func(p *ListParams) { p.Cluster = "cluster-a" }, 1},
		{"composition_id", func(p *ListParams) { p.CompositionID = "550e8400-e29b-41d4-a716-446655440000" }, 1},
		{"name_contains", func(p *ListParams) { p.NameContains = "api" }, 1},
		{"labels", func(p *ListParams) { p.Labels = `{"app":"nginx"}` }, 1},
		{"since", func(p *ListParams) { p.Since = &since }, 1},
	}

	// Iterate over all 2^5 = 32 bitmask combinations.
	for mask := 0; mask < (1 << len(filters)); mask++ {
		p := deploymentParams("default", 50)

		var names []string
		expectedArgs := 5 // group + version + IN(group, resource, namespace) are always present
		for i, f := range filters {
			if mask&(1<<i) != 0 {
				f.apply(&p)
				names = append(names, f.name)
				expectedArgs += f.args
			}
		}

		// Add cursor for odd masks to test cursor interplay.
		hasCursor := mask%3 == 0 && mask > 0
		if hasCursor {
			p.SortBy = SortByUpdatedAt
			p.Cursor = cursor
			expectedArgs += 2 // cursor adds 2 args: updated_at and id (SortByUpdatedAt)
			names = append(names, "cursor")
		}

		expectedArgs++ // limit

		testName := "filters"
		if len(names) > 0 {
			testName += "+" + names[0]
			for _, n := range names[1:] {
				testName += "+" + n
			}
		} else {
			testName += "+none"
		}

		t.Run(testName, func(t *testing.T) {
			query, args, err := buildListQuery(p)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if len(args) != expectedArgs {
				t.Fatalf("expected %d args, got %d\nquery: %s\nargs: %v", expectedArgs, len(args), query, args)
			}
			if query == "" {
				t.Fatal("empty query")
			}
		})
	}
}

// TestEscapeLIKE verifies special characters are properly escaped.
func TestEscapeLIKE(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"100%", `100\%`},
		{"under_score", `under\_score`},
		{`back\slash`, `back\\slash`},
		{`all%_\special`, `all\%\_\\special`},
		{"", ""},
	}

	for _, tt := range tests {
		got := escapeLIKE(tt.input)
		if got != tt.want {
			t.Errorf("escapeLIKE(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
