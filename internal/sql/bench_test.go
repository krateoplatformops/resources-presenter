package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// --- Cursor benchmarks ---

func BenchmarkCursorEncode(b *testing.B) {
	c := &ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeCursor(c)
	}
}

func BenchmarkCursorDecode(b *testing.B) {
	encoded := EncodeCursor(&ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecodeCursor(encoded)
	}
}

func BenchmarkCursorRoundtrip(b *testing.B) {
	c := &ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoded := EncodeCursor(c)
		DecodeCursor(encoded)
	}
}

// --- Builder benchmarks ---

func BenchmarkBuildListQuery_Minimal(b *testing.B) {
	p := ListParams{
		ResourceKind: "apps/v1.Deployment",
		Limit:        100,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildListQuery(p, nil)
	}
}

func BenchmarkBuildListQuery_AllFilters(b *testing.B) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursor := EncodeCursor(&ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	})
	p := ListParams{
		ResourceKind:  "apps/v1.Deployment",
		Cluster:       "cluster-a",
		Namespace:     "prod",
		CompositionID: "550e8400-e29b-41d4-a716-446655440000",
		Name:          "api-service",
		Labels:        `{"app":"nginx","tier":"backend"}`,
		Since:         &since,
		Raw:           true,
		Limit:         100,
		Cursor:        cursor,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildListQuery(p, nil)
	}
}

// --- splitResourceKind benchmark ---

func BenchmarkSplitResourceKind(b *testing.B) {
	inputs := []string{
		"apps/v1.Deployment",
		"widgets.templates.krateo.io/v1beta1.Panel",
		"composition.krateo.io/v1-2-2.GithubScaffoldingWithCompositionPage",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		splitResourceKind(inputs[i%len(inputs)])
	}
}

// --- escapeLIKE benchmark ---

func BenchmarkEscapeLIKE(b *testing.B) {
	inputs := []string{
		"simple-name",
		"name_with_underscores",
		"100%match",
		`back\slash`,
		"no-special-chars-at-all-just-a-long-resource-name-here",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		escapeLIKE(inputs[i%len(inputs)])
	}
}

// --- JSON serialization benchmarks ---

func makeListResult(n int, includeRaw bool) *ListResult {
	items := make([]ResourceItem, n)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := range items {
		items[i] = ResourceItem{
			Name:        fmt.Sprintf("resource-%04d", i),
			Namespace:   "default",
			APIVersion:  "apps/v1",
			Kind:        "Deployment",
			ClusterName: "cluster-a",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if includeRaw {
			items[i].Raw = json.RawMessage(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"resource","namespace":"default","labels":{"app":"test","tier":"backend"}},"spec":{"replicas":3}}`)
		}
	}
	return &ListResult{Count: n, Items: items}
}

func BenchmarkJSONMarshal_10(b *testing.B) {
	result := makeListResult(10, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(result)
	}
}

func BenchmarkJSONMarshal_100(b *testing.B) {
	result := makeListResult(100, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(result)
	}
}

func BenchmarkJSONMarshal_1000(b *testing.B) {
	result := makeListResult(1000, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(result)
	}
}

func BenchmarkJSONMarshal_1000_WithRaw(b *testing.B) {
	result := makeListResult(1000, true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(result)
	}
}

// --- ListResources with pgxmock benchmarks ---

func benchmarkListResources(b *testing.B, rowCount int, raw bool) {
	b.Helper()

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)

	params := ListParams{
		ResourceKind: "apps/v1.Deployment",
		Raw:          raw,
		Limit:        rowCount + 1, // ensure no next-page logic triggers
	}

	cols := baseCols()
	if raw {
		cols = rawCols()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		mock, err := pgxmock.NewPool()
		if err != nil {
			b.Fatal(err)
		}

		rows := pgxmock.NewRows(cols)
		for j := 0; j < rowCount; j++ {
			updatedAt := now.Add(-time.Duration(j) * time.Second)
			if raw {
				rows.AddRow(
					fmt.Sprintf("res-%04d", j), "default", "apps/v1.Deployment",
					"cluster-a", created, updatedAt, nil, int64(1000-j),
					[]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"res","namespace":"default"}}`),
				)
			} else {
				rows.AddRow(
					fmt.Sprintf("res-%04d", j), "default", "apps/v1.Deployment",
					"cluster-a", created, updatedAt, nil, int64(1000-j),
				)
			}
		}

		mock.ExpectQuery("SELECT .+ FROM krateo_resources").
			WithArgs("apps/v1.Deployment", rowCount+2).
			WillReturnRows(rows)

		b.StartTimer()

		ListResources(context.Background(), mock, params, nil)

		b.StopTimer()
		mock.Close()
	}
}

func BenchmarkListResources_10rows(b *testing.B)   { benchmarkListResources(b, 10, false) }
func BenchmarkListResources_100rows(b *testing.B)  { benchmarkListResources(b, 100, false) }
func BenchmarkListResources_1000rows(b *testing.B) { benchmarkListResources(b, 1000, false) }
func BenchmarkListResources_1000rows_raw(b *testing.B) {
	benchmarkListResources(b, 1000, true)
}

// baseCols and rawCols are defined in resources_test.go, but we need them here.
// Since both files are in the same package and test binary, they're shared.
