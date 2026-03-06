# resource-proxy

Read-only HTTP API for querying current Kubernetes resource state stored in PostgreSQL.

## Overview

`resource-proxy` serves the current state of a set of Kubernetes resources (one row per resource, identified by `global_uid`). It does **not** ingest data, it reads from a `krateo_resources` table populated by an another component: [resources-ingester](https://github.com/krateoplatformops/resources-ingester).

Key features:

- **GET and POST** query support with filter capabilities
- **Keyset pagination** (`updated_at DESC, id DESC`) for stable, efficient paging
- **Resource kind registry** resolves short names (e.g. `panels`) to full `apiVersion:Kind` format
- **JSONB label filtering** via PostgreSQL containment (`@>`)
- **Access policy hook** for future authentication/authorization integration (TODO)
- **Structured latency logging** for every request (parse, query, serialize phases)

## Quick Start

### Prerequisites for local development

- Go 1.25+
- Docker (for PostgreSQL)

### Run locally

```bash
# 1. Start PostgreSQL
docker run -d --name krateo-pg \
  -e POSTGRES_USER=krateo \
  -e POSTGRES_PASSWORD=krateo \
  -e POSTGRES_DB=krateo \
  -p 5432:5432 \
  postgres:18

# 2. Create the schema
docker exec -i krateo-pg psql -U krateo -d krateo < assets/resources.schema.sql

# 3. (Optional) Seed sample data
docker exec -i krateo-pg psql -U krateo -d krateo < assets/seed_data.sql

# 4. Run the service
DB_USER=krateo DB_PASS=krateo DB_HOST=localhost DB_NAME=krateo \
  DB_PARAMS="sslmode=disable" DEBUG=true \
  go run .

# 5. Query
curl -s http://localhost:8080/resources/panels | jq
```

## API

### Endpoint

```
GET  /resources/{resource_kind}
POST /resources/{resource_kind}
```

`resource_kind` can be a short name (`panels`) or the full format (`widgets.templates.krateo.io/v1beta1:Panel`).

### Filters

All filters are optional and combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `cluster` | string | Exact match on `cluster_name` |
| `namespace` | string | Exact match on `namespace` |
| `composition_id` | UUID | Exact match on `composition_id` |
| `name` | string | Case-insensitive partial match (`ILIKE %name%`) |
| `labels` | JSON object | JSONB containment on `metadata.labels` (`@>`) |
| `since` | RFC3339 | Resources with `updated_at >= since` |
| `raw` | boolean | Include full Kubernetes object (default: `false`) |
| `limit` | integer | Page size (default: `100`, max: `5000`) |
| `cursor` | base64 | Opaque keyset cursor from previous response |

GET uses query parameters. POST uses the same fields as a JSON body (with `labels` as a JSON object, not a string).

### Response

```json
{
  "count": 1,
  "items": [
    {
      "name": "my-panel",
      "namespace": "krateo-system",
      "apiVersion": "widgets.templates.krateo.io/v1beta1",
      "kind": "Panel",
      "cluster_name": "prod-eu",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-06T14:30:00Z"
    }
  ],
  "cursor": "<base64-opaque-token>"
}
```

- `composition_id` appears only when set (non-null)
- `raw` appears only when `raw=true` is requested
- `cursor` is absent on the last page or when results fit in a single page

### Error Responses

Kubernetes-style `Status` objects:

| Code | Condition |
| --- | --- |
| `400` | Invalid parameters (bad UUID, JSON, timestamp, cursor) |
| `404` | Unknown resource kind |
| `405` | Method not allowed |
| `500` | Internal server error |

See [SEARCH.md](SEARCH.md) for full examples including pagination loops.

## Configuration

| Environment Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `8080` | HTTP server port |
| `DEBUG` | `false` | Enable debug-level logging |
| `DB_USER` | | PostgreSQL username |
| `DB_PASS` | | PostgreSQL password |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_NAME` | | PostgreSQL database name |
| `DB_PARAMS` | | Extra connection params (e.g. `sslmode=disable`) |
| `DB_READY_TIMEOUT` | `4m` | Max wait for PostgreSQL to become ready |

## Project Structure

```
.
├── main.go                          # Entrypoint: wiring, server, graceful shutdown
├── internal/
│   ├── config/                      # Environment-based configuration
│   ├── handlers/                    # HTTP handler (GET/POST routing, parsing, response)
│   │   ├── resources.go
│   │   └── resources_test.go        # Integration tests (testcontainers + PostgreSQL)
│   ├── sql/                         # Query builder, pagination, cursor encoding
│   │   ├── resources.go
│   │   ├── builder.go               # SQL builder with positional $N params
│   │   ├── cursor.go                # Keyset cursor encode/decode
│   │   └── resources_test.go        # Unit tests (pgxmock)
│   ├── registry/                    # Resource kind short name resolution
│   │   └── resources.yaml           # Embedded registry data
│   └── access/                      # Access policy stub (future auth)
├── assets/
│   └── resources.schema.sql         # DDL for krateo_resources table
├── chart/                           # Helm chart for Kubernetes deployment
├── Dockerfile                       # Multi-stage build (distroless runtime)
├── SEARCH.md                        # Full API reference with curl examples
└── TESTING.md                       # Testing guide and local manual testing
```

## Testing

```bash
# All tests (requires Docker for testcontainers)
go test ./... -cover

# Unit tests only (fast, uses pgxmock, no Docker)
go test ./internal/sql/ -cover -v

# Integration tests only (real PostgreSQL via testcontainers)
go test ./internal/handlers/ -cover -v

# Registry tests (no Docker)
go test ./internal/registry/ -cover -v
```

See [TESTING.md](TESTING.md) for manual testing with curl (including seed data and POST examples).

## Health Probes

```
GET /livez   → 200 OK (checks if service is running)
GET /readyz  → 200 OK (checks PostgreSQL connectivity)
```

## License

See [LICENSE](LICENSE).
