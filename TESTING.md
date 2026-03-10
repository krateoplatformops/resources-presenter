# Testing Guide

## Test dependencies

| Dependency | Purpose |
|---|---|
| `github.com/pashagolub/pgxmock/v4` | **Unit tests** — mocks the `pgx` database interface so SQL-layer tests (`internal/sql/resources_test.go`) run instantly without a real database. Verifies query construction, parameter binding, cursor encoding, and row scanning in isolation. |
| `github.com/testcontainers/testcontainers-go` | **Integration tests** — spins up a real PostgreSQL container via Docker at test time. Manages container lifecycle (start, wait-for-ready, stop). |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | **Postgres module** for testcontainers — provides Postgres-specific helpers (username, password, database name, readiness detection via log parsing). Used in `internal/handlers/resources_test.go`. |

**In short**: `pgxmock` = fast, no-Docker unit tests for the SQL layer. `testcontainers` = real-DB integration tests for the HTTP handler layer. Both are test-only dependencies (`require` block in `go.mod`).

## Running tests

All tests require Docker to be running (testcontainers starts a Postgres container automatically).

```bash
# Unit tests only (SQL layer, fast, uses pgxmock)
go test ./internal/sql/ -cover -v

# Integration tests only (handler layer, uses testcontainers)
go test ./internal/handlers/ -cover -v

# Run all tests
go test ./... -cover
```

## Local manual testing (curl)

To test the service manually with curl, you need a running PostgreSQL instance.

### 1. Start Postgres

```bash
docker run -d --name krateo-pg \
  -e POSTGRES_USER=krateo \
  -e POSTGRES_PASSWORD=krateo \
  -e POSTGRES_DB=krateo \
  -p 5432:5432 \
  postgres:18
```

### 2. Create the schema

```bash
docker exec -i krateo-pg psql -U krateo -d krateo < assets/resources.schema.sql
```

### 3. Seed sample data

Generates 500 Panel resources across different clusters, namespaces, and dashboard themes:

```bash
docker exec -i krateo-pg psql -U krateo -d krateo < assets/seed_data.sql
```

Verify:
```bash
docker exec -i krateo-pg psql -U krateo -d krateo -c \
  "SELECT cluster_name, namespace, resource_kind, resource_name FROM krateo_resources ORDER BY updated_at DESC;"
```

### 4. Run the service

```bash
DB_USER=krateo DB_PASS=krateo DB_HOST=localhost DB_NAME=krateo \
  DB_PARAMS="sslmode=disable" DEBUG=true \
  go run .
```

### 5. Test with curl

#### GET examples

```bash
# List panels
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel' | jq

# List panels with full raw objects
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&raw=true' | jq

# Filter by namespace
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&namespace=krateo-system' | jq

# Filter by cluster + namespace
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&cluster=prod-eu&namespace=krateo-system' | jq

# Search by name (case-insensitive, partial match)
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&name=blueprints' | jq

# Filter by labels
curl --get 'http://localhost:8080/resources' \
  --data-urlencode 'group=widgets.templates.krateo.io' \
  --data-urlencode 'version=v1beta1' \
  --data-urlencode 'kind=Panel' \
  --data-urlencode 'labels={"app.kubernetes.io/part-of":"dashboard"}' | jq

# Filter by time (resources updated after a given date)
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&since=2026-03-01T00:00:00Z' | jq

# Pagination (page 1, then page 2)
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&limit=10' | jq
# copy cursor from response, then:
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&limit=10&cursor=<CURSOR>' | jq

# Missing required params (returns 400)
curl -s 'http://localhost:8080/resources' | jq

# Health probes
curl -s http://localhost:8080/livez
curl -s http://localhost:8080/readyz
```

#### POST examples

```bash
# Basic query with filters
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "namespace": "krateo-system"
  }' | jq

# Multiple filters + raw
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "cluster": "prod-eu",
    "namespace": "krateo-system",
    "name": "blueprints",
    "raw": true,
    "limit": 10
  }' | jq

# Filter by labels (note: labels is a JSON object, not a string)
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "labels": {"app.kubernetes.io/part-of": "dashboard"},
    "limit": 50
  }' | jq

# Filter by time
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "since": "2026-03-01T00:00:00Z",
    "limit": 100
  }' | jq

# Pagination via POST (page 1)
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "cluster": "prod-eu",
    "limit": 10
  }' | jq

# Pagination via POST (page 2 — use cursor from page 1)
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "cluster": "prod-eu",
    "limit": 10,
    "cursor": "<CURSOR_FROM_PAGE_1>"
  }' | jq

# All filters combined
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "cluster": "prod-eu",
    "namespace": "krateo-system",
    "name": "blueprints",
    "labels": {"app.kubernetes.io/part-of": "dashboard"},
    "since": "2026-03-01T00:00:00Z",
    "raw": true,
    "limit": 50
  }' | jq
```

### 6. Cleanup

```bash
docker stop krateo-pg && docker rm krateo-pg
```

---

## Benchmarks

Benchmarks live in `internal/sql/bench_test.go` and measure the hot paths of the query pipeline.

### What is benchmarked

| Benchmark | What it measures |
|---|---|
| `BenchmarkCursorEncode` | Encoding a keyset cursor to base64 |
| `BenchmarkCursorDecode` | Decoding a base64 cursor back to struct |
| `BenchmarkCursorRoundtrip` | Full encode + decode cycle |
| `BenchmarkBuildListQuery_Minimal` | SQL builder with only resource_kind filter |
| `BenchmarkBuildListQuery_AllFilters` | SQL builder with all 7 filters + cursor active |
| `BenchmarkSplitResourceKind` | Parsing `apiVersion.Kind` strings |
| `BenchmarkEscapeLIKE` | Escaping LIKE special characters |
| `BenchmarkJSONMarshal_10/100/1000` | Serializing result sets of varying sizes |
| `BenchmarkJSONMarshal_1000_WithRaw` | Serializing 1000 items including raw JSONB |
| `BenchmarkListResources_10/100/1000rows` | Full query + scan + pagination with pgxmock |
| `BenchmarkListResources_1000rows_raw` | Same with raw JSONB included |

### Running benchmarks

```bash
# Quick sanity check (1 iteration, fast)
./scripts/bench.sh quick

# Full run (6 iterations for statistical reliability, saves to benchmarks/)
./scripts/bench.sh run

# Full run with custom options
./scripts/bench.sh run -count 10 -benchtime 2s

# Manual (without script)
go test ./internal/sql/ -bench=. -benchmem -count=6 -run='^$'

# Run specific benchmark
go test ./internal/sql/ -bench=BenchmarkListResources -benchmem -count=6 -run='^$'
```

### Key flags explained

| Flag | Purpose |
|---|---|
| `-bench=.` | Regex to match benchmark names (`.` = all) |
| `-benchmem` | Include memory allocation stats (allocs/op, B/op) |
| `-count=N` | Run each benchmark N times (use 5+ for reliable stats) |
| `-run='^$'` | Skip unit tests (only run benchmarks) |
| `-benchtime=2s` | Run each benchmark for 2 seconds instead of default 1s |
| `-timeout=10m` | Extend timeout for long benchmark suites |

### Saving and comparing results

The `scripts/bench.sh` script automates the save-and-compare workflow:

```bash
# 1. Save a baseline (e.g. before making changes in the code)
./scripts/bench.sh baseline

# 2. Make your code changes...

# 3. Run benchmarks again
./scripts/bench.sh run

# 4. Compare against baseline
./scripts/bench.sh compare
```

The `compare` command uses [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat), the official Go benchmark comparison tool. Install it once:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

`benchstat` shows a table with delta percentages and p-values:

```
                          │ baseline.txt │          latest.txt           │
                          │    sec/op    │   sec/op    vs base           │
ListResources_1000rows-8     1.256m ± 3%   0.987m ± 2%  -21.42% (p=0.002)
```

A change is statistically significant when `p < 0.05`. Use `-count=6` or higher for reliable p-values. With `-count=1` or `-count=2`, benchstat cannot compute meaningful statistics.

**Manual comparison (without the script):**

```bash
# Save results to files
go test ./internal/sql/ -bench=. -benchmem -count=6 -run='^$' > bench_before.txt
# ... make changes ...
go test ./internal/sql/ -bench=. -benchmem -count=6 -run='^$' > bench_after.txt

# Compare
benchstat bench_before.txt bench_after.txt
```

### Reading benchmark output

```
BenchmarkListResources_100rows-8    7315    160107 ns/op    55023 B/op    1063 allocs/op
```

| Column | Meaning |
|---|---|
| `-8` | GOMAXPROCS (number of CPUs used) |
| `7315` | Number of iterations the benchmark ran |
| `160107 ns/op` | Nanoseconds per operation (~0.16ms) |
| `55023 B/op` | Bytes allocated per operation |
| `1063 allocs/op` | Heap allocations per operation |

Lower is better for all three metrics. For this service, focus on:
- **ns/op** for latency-sensitive paths (query + scan)
- **B/op** and **allocs/op** for GC pressure under high throughput

### Tips for reliable benchmarks

- Close browsers, Slack, Docker Desktop dashboard — anything CPU-intensive
- Use `-count=6` or more; single runs are noisy
- Run on the same machine for before/after comparisons
- Don't compare results across different machines or Go versions
- The `benchmarks/` directory is gitignored — results are local only

---

## Stress tests

Stress tests live in `internal/sql/stress_test.go` and verify correctness under extreme or edge-case conditions.

### What is tested

| Test | What it verifies |
|---|---|
| `TestListResources_MaxLimit` | Correct behavior at max limit (5000 rows, no next page) |
| `TestListResources_MaxLimitWithNextPage` | Cursor is correctly set when 5001 rows exist |
| `TestListResources_RawLargePayload` | Handling of ~50KB raw JSONB objects |
| `TestListResources_ConcurrentAccess` | 50 goroutines querying simultaneously (race safety) |
| `TestBuildListQuery_AllFilterCombinations` | All 128 combinations of 6 optional filters x 2 policy states |
| `TestEscapeLIKE` | LIKE special character escaping (`%`, `_`, `\`) |

### Running stress tests

```bash
# All stress tests
go test ./internal/sql/ -run='TestListResources_Max|TestListResources_Raw|TestListResources_Concurrent|TestBuildListQuery_AllFilter|TestEscapeLIKE' -v

# Concurrent test with race detector (recommended)
go test ./internal/sql/ -run=TestListResources_Concurrent -race -v

# All SQL tests (unit + stress)
go test ./internal/sql/ -v

# All SQL tests with race detector
go test ./internal/sql/ -race -v
```

### Running everything

```bash
# All tests across all packages (unit + integration + stress)
# Requires Docker for integration tests
go test ./... -v

# Just unit + stress tests (no Docker needed)
go test ./internal/sql/ ./internal/config/ -v

# Full suite with race detection and coverage
go test ./... -race -cover
```
