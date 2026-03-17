# resources-presenter

Read-only HTTP API for querying current Kubernetes resource state stored in PostgreSQL.

## Overview

`resources-presenter` serves the current state of a set of Kubernetes resources (one row per resource, identified by `global_uid`). It does **not** ingest data, it reads from a `krateo_resources` table populated by an another component: [resources-ingester](https://github.com/krateoplatformops/resources-ingester).

Key features:

- **GET and POST** query support with filter capabilities
- **Keyset pagination** (`updated_at DESC, id DESC`) for stable, efficient paging
- **Dynamic resource resolution** via `group` (required) plus optional `version` and `resource` filters — discovery-based, no static registry needed
- **JSONB label filtering** via PostgreSQL containment (`@>`)
- **Batch RBAC enforcement** via discovery → `rbac.UserCan()` batch check → filtered query
- **Structured latency logging** for every request (parse, discovery, rbac, query, serialize phases)

## API

### Endpoint

```
GET  /resources?group=<group>[&version=<version>][&resource=<resource>][&namespace=<namespace>]
POST /resources
```

The resource type is identified by the **required** `group` parameter. `version` and `resource` are optional filters that narrow the discovery query. These map directly to the DB columns `resource_group`, `resource_version`, and `resource_plural` in the `krateo_resources` table of the PostgreSQL database.

### Filters

All filters are optional and combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `group` | string | **Required.** API group (e.g. `apps`, `widgets.templates.krateo.io`) |
| `version` | string | Optional. API version (e.g. `v1`, `v1beta1`). Narrows discovery. |
| `resource` | string | Optional. Resource plural name (e.g. `deployments`, `panels`). Lowercase. Narrows discovery. |
| `cluster` | string | Exact match on `cluster_name` |
| `namespace` | string | Exact match on `namespace`. Default: `"default"`. Use `"*"` for all namespaces. See [Namespace Handling](#namespace-handling). |
| `composition_id` | UUID | Exact match on `composition_id` |
| `name` | string | Exact match on `resource_name`. Mutually exclusive with `name_contains`. |
| `name_contains` | string | Case-insensitive partial match (`ILIKE %name_contains%`). Mutually exclusive with `name`. |
| `labels` | JSON object | JSONB containment on `metadata.labels` (`@>`) |
| `since` | RFC3339 | Resources with `updated_at >= since` |
| `raw` | boolean | Include full Kubernetes object (default: `false`) |
| `limit` | integer | Page size (default: `100`). Use `-1` for unlimited (returns all results, no pagination). |
| `cursor` | base64 | Opaque keyset cursor from previous response |

GET uses query parameters. POST uses the same fields as a JSON body (with `labels` as a JSON object, not a string). Note: `kind` is not a query parameter — the `resource` (plural) field is used for filtering. Only `group` is required; all other fields are optional.

### Response

```json
{
  "count": 1,
  "items": [
    {
      "name": "my-panel",
      "uid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "namespace": "krateo-system",
      "group": "widgets.templates.krateo.io",
      "version": "v1beta1",
      "kind": "Panel",
      "resource": "panels",
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

### Namespace Handling

The `namespace` parameter follows Kubernetes API semantics:

| Value | Behavior |
| --- | --- |
| *(absent/empty)* | Defaults to `"default"` — only resources in the `default` namespace are returned |
| `*` | All namespaces — no namespace filter is applied. RBAC is still enforced, so only resources the user has access to will be returned. |
| `prod`, `krateo-system`, etc. | Exact match on the specified namespace |

This mirrors how `kubectl` works: commands target the `default` namespace unless `-n` or `--all-namespaces` is specified.

### Error Responses

Kubernetes-style `Status` objects:

| Code | Condition |
| --- | --- |
| `400` | Invalid/missing parameters (missing group, bad UUID, JSON, timestamp, cursor) |
| `403` | Forbidden — RBAC denied access to all discovered resources |
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

## Testing

```bash
# All tests (requires Docker for testcontainers)
go test ./... -cover

# Unit tests only (fast, uses pgxmock, no Docker)
go test ./internal/sql/ -cover -v

# Integration tests only (real PostgreSQL via testcontainers)
go test ./internal/handlers/ -cover -v
```

See [TESTING.md](TESTING.md) for detailed testing instructions.

## Notes on RBAC

TODO

## Health Probes

```
GET /livez   → 200 OK (checks if service is running)
GET /readyz  → 200 OK (checks PostgreSQL connectivity)
```

## License

See [LICENSE](LICENSE).
