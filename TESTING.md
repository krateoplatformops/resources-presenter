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
# Run all tests
go test ./... -cover

# Unit tests only (SQL layer, fast, uses pgxmock)
go test ./internal/sql/ -cover -v

# Integration tests only (handler layer, uses testcontainers)
go test ./internal/handlers/ -cover -v

# Registry tests (no Docker needed)
go test ./internal/registry/ -cover -v
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
# List panels (short name)
curl -s http://localhost:8080/resources/panels | jq

# List panels with full raw objects
curl -s 'http://localhost:8080/resources/panels?raw=true' | jq

# Filter by namespace
curl -s 'http://localhost:8080/resources/panels?namespace=krateo-system' | jq

# Filter by cluster + namespace
curl -s 'http://localhost:8080/resources/panels?cluster=prod-eu&namespace=krateo-system' | jq

# Search by name (case-insensitive, partial match)
curl -s 'http://localhost:8080/resources/panels?name=blueprints' | jq

# Filter by labels
curl -s 'http://localhost:8080/resources/panels?labels=%7B%22app.kubernetes.io%2Fpart-of%22%3A%22dashboard%22%7D' | jq

# Filter by time (resources updated after a given date)
curl -s 'http://localhost:8080/resources/panels?since=2026-03-01T00:00:00Z' | jq

# Pagination (page 1, then page 2)
curl -s 'http://localhost:8080/resources/panels?limit=10' | jq
# copy cursor from response, then:
curl -s 'http://localhost:8080/resources/panels?limit=10&cursor=<CURSOR>' | jq

# Unknown resource kind (returns 404)
curl -s http://localhost:8080/resources/unknown

# Health probes
curl -s http://localhost:8080/livez
curl -s http://localhost:8080/readyz
```

#### POST examples

```bash
# Basic query with filters
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{"namespace": "krateo-system"}' | jq

# Multiple filters + raw
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{
    "cluster": "prod-eu",
    "namespace": "krateo-system",
    "name": "blueprints",
    "raw": true,
    "limit": 10
  }' | jq

# Filter by labels (note: labels is a JSON object, not a string)
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{
    "labels": {"app.kubernetes.io/part-of": "dashboard"},
    "limit": 50
  }' | jq

# Filter by time
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{
    "since": "2026-03-01T00:00:00Z",
    "limit": 100
  }' | jq

# Pagination via POST (page 1)
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{"cluster": "prod-eu", "limit": 10}' | jq

# Pagination via POST (page 2 — use cursor from page 1)
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{
    "cluster": "prod-eu",
    "limit": 10,
    "cursor": "<CURSOR_FROM_PAGE_1>"
  }' | jq

# All filters combined
curl -s -X POST http://localhost:8080/resources/panels \
  -H 'Content-Type: application/json' \
  -d '{
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
