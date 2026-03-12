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
	p := deploymentParams(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildListQuery(p)
	}
}

func BenchmarkBuildListQuery_AllFilters(b *testing.B) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cursor := EncodeCursor(&ResourcesCursor{
		UpdatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		ID:        42,
	})
	p := deploymentParams(100)
	p.Cluster = "cluster-a"
	p.Namespace = "prod"
	p.CompositionID = "550e8400-e29b-41d4-a716-446655440000"
	p.Name = "api-service"
	p.Labels = `{"app":"nginx","tier":"backend"}`
	p.Since = &since
	p.Raw = true
	p.Cursor = cursor

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildListQuery(p)
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
			Group:       "apps",
			Version:     "v1",
			Kind:        "Deployment",
			Resource:    "deployments",
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

	params := deploymentParams(rowCount + 1) // ensure no next-page logic triggers
	params.Raw = raw

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
				row := deploymentRow(fmt.Sprintf("res-%04d", j), "default", "cluster-a", created, updatedAt, nil, int64(1000-j))
				row = append(row, []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"res","namespace":"default"}}`))
				rows.AddRows(row)
			} else {
				rows.AddRows(deploymentRow(
					fmt.Sprintf("res-%04d", j), "default", "cluster-a",
					created, updatedAt, nil, int64(1000-j),
				))
			}
		}

		mock.ExpectQuery("SELECT .+ FROM krateo_resources").
			WithArgs(deploymentArgs(rowCount + 2)...).
			WillReturnRows(rows)

		b.StartTimer()

		ListResources(context.Background(), mock, params)

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
