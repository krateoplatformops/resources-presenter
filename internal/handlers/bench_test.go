package handlers

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Handler-level benchmarks ---
//
// These benchmarks test the full handler pipeline (parse → discovery → RBAC → query → serialize)
// against a real PostgreSQL instance via testcontainers (shared container from TestMain).
// They measure end-to-end handler throughput and memory allocation.
//
// Run with:
//   go test ./internal/handlers/ -bench=BenchmarkHandler -benchmem -count=3

// benchSeed truncates the table and seeds resources for benchmarking.
func benchSeed(b *testing.B, count int) *pgxpool.Pool {
	b.Helper()

	db := testDB(b)
	applySchema(b, db)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seedResources(b, db, seedOptions{
		Cluster: "cluster-a", Namespace: "default",
		ResourceGroup: "apps", ResourceVersion: "v1",
		ResourceKind: "Deployment", ResourcePlural: "deployments",
		Count: count, StartTime: now, Delta: time.Second,
	})

	return db
}

// BenchmarkHandler_List_100_NoRaw benchmarks listing 100 resources without raw.
func BenchmarkHandler_List_100_NoRaw(b *testing.B) {
	db := benchSeed(b, 100)

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_List_100_Raw benchmarks listing 100 resources with raw=true.
func BenchmarkHandler_List_100_Raw(b *testing.B) {
	db := benchSeed(b, 100)

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100&raw=true", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_List_1000_NoRaw benchmarks listing 1000 resources without raw.
func BenchmarkHandler_List_1000_NoRaw(b *testing.B) {
	db := benchSeed(b, 1000)

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=-1", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_List_1000_Raw benchmarks listing 1000 resources with raw=true.
func BenchmarkHandler_List_1000_Raw(b *testing.B) {
	db := benchSeed(b, 1000)

	handler := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=-1&raw=true", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_Detail benchmarks fetching a single resource by global_uid.
func BenchmarkHandler_Detail(b *testing.B) {
	db := benchSeed(b, 100)

	handler := ResourceDetailHandler(db, testLogger(), allowAllAuthorizer{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources/cluster-a:uid-0050", nil)
		req.SetPathValue("global_uid", "cluster-a:uid-0050")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_Gzip_100_Raw benchmarks the gzip middleware with 100 raw resources.
func BenchmarkHandler_Gzip_100_Raw(b *testing.B) {
	db := benchSeed(b, 100)

	inner := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})
	handler := Gzip()(inner)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100&raw=true", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkHandler_GzipCompressionRatio measures the gzip compression ratio
// and verifies the compressed response is valid. Reports uncompressed size,
// compressed size, and the ratio as custom metrics.
func BenchmarkHandler_GzipCompressionRatio(b *testing.B) {
	db := benchSeed(b, 100)

	inner := ResourcesHandler(db, testLogger(), allowAllAuthorizer{})

	// Measure uncompressed size.
	reqPlain := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100&raw=true", nil)
	recPlain := httptest.NewRecorder()
	inner.ServeHTTP(recPlain, reqPlain)
	plainSize := recPlain.Body.Len()

	// Measure compressed size.
	gzHandler := Gzip()(inner)
	reqGz := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100&raw=true", nil)
	reqGz.Header.Set("Accept-Encoding", "gzip")
	recGz := httptest.NewRecorder()
	gzHandler.ServeHTTP(recGz, reqGz)
	gzSize := recGz.Body.Len()

	ratio := float64(plainSize) / float64(gzSize)
	b.ReportMetric(float64(plainSize), "uncompressed_bytes")
	b.ReportMetric(float64(gzSize), "compressed_bytes")
	b.ReportMetric(ratio, "compression_ratio")

	// Verify compressed response is valid gzip with correct JSON.
	gr, err := gzip.NewReader(recGz.Body)
	if err != nil {
		b.Fatalf("invalid gzip response: %v", err)
	}
	defer gr.Close()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		b.Fatalf("gzip decompress error: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(decompressed, &resp); err != nil {
		b.Fatalf("invalid JSON after decompression: %v", err)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 100 {
		b.Fatalf("expected 100 items after decompression, got %d", len(items))
	}

	// Run the actual benchmark.
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/resources?group=apps&version=v1&resource=deployments&namespace=default&limit=100&raw=true", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		gzHandler.ServeHTTP(rec, req)
	}
}
