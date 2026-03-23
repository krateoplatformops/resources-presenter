# `resources-presenter`

Read-only HTTP API for querying current Kubernetes resource state stored in PostgreSQL.

## Overview

`resources-presenter` serves the current state of a set of Kubernetes resources (one row per resource, identified by `global_uid`). It does **not** ingest data, it reads from a `krateo_resources` table populated by an another component: [resources-ingester](https://github.com/krateoplatformops/resources-ingester).

Key features:
- **GET and POST** query support with filter capabilities to get lists of resources matching criteria
- **Single-resource detail** endpoint via `global_uid`
- **Configurable sorting** via `sort_by` parameter (`resource`, `created_at`, `updated_at`, `global_uid`, `composition_id`)
- **Keyset pagination** for stable, efficient paging (cursor is sort-order-aware)
- **Dynamic resource resolution** via `group` (required) plus optional `version` and `resource` filters: discovery-based, no static registry needed
- **JSONB label filtering** via PostgreSQL containment (`@>`)
- **Batch RBAC enforcement** via discovery → RBAC batch check → filtered query
- **Structured latency logging** for every request (parse, discovery, rbac, query, serialize phases)

## API

### Endpoints

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/resources` | List resources matching filters (query parameters) |
| `POST` | `/resources` | List resources matching filters (JSON body) |
| `GET` | `/resources/{global_uid}` | Get a single resource by `global_uid` |

---

### List Endpoint — `GET /resources` and `POST /resources`

Returns resources matching the provided filters. The resource type is identified by the **required** `group` parameter. `version` and `resource` are optional filters that narrow the discovery query. These map directly to the DB columns `resource_group`, `resource_version`, and `resource_plural` in the `krateo_resources` table.

#### Filters

All filters are optional (except `group`) and combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `group` | string | **Required.** API group (e.g. `apps`, `widgets.templates.krateo.io`) |
| `version` | string | Optional. API version (e.g. `v1`, `v1beta1`). Narrows discovery. |
| `resource` | string | Optional. Resource plural name (e.g. `deployments`, `panels`). Lowercase. Narrows discovery. |
| `cluster` | string | Exact match on `cluster_name` |
| `namespace` | string | Exact match on `namespace`. Default: `"default"`. **Use `"*"` for all namespaces. See [Namespace Handling](#namespace-handling).** |
| `composition_id` | UUID | Exact match on `composition_id` |
| `name` | string | Exact match on `resource_name`. Mutually exclusive with `name_contains`. |
| `name_contains` | string | Case-insensitive partial match (`ILIKE %name_contains%`). Mutually exclusive with `name`. |
| `labels` | JSON object | JSONB containment on `metadata.labels` (`@>`) |
| `since` | RFC3339 | Resources with `updated_at >= since` |
| `raw` | boolean | Include full Kubernetes object (default: `false`) |
| `status_raw` | boolean | Include Kubernetes status subtree (default: `false`) |
| `limit` | integer | Page size (default: `100`). Minimum: `1`, Maximum: `5000`. |
| `cursor` | base64 | Opaque keyset cursor from previous response. Cursor is sort-order-aware: a cursor from one `sort_by` cannot be reused with a different one. |

GET uses query parameters. POST uses the same fields as a JSON body (with `labels` as a JSON object, not a string). Note: `kind` is not a query parameter: the `resource` (plural) field is used for filtering. Only `group` is required; all other fields are optional.

#### List response

```json
{
  "count": 2,
  "items": [
    {
      "name": "my-panel",
      "uid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "global_uid": "prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "namespace": "krateo-system",
      "group": "widgets.templates.krateo.io",
      "version": "v1beta1",
      "kind": "Panel",
      "resource": "panels",
      "cluster_name": "prod-eu",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-06T14:30:00Z"
    },
    {
      "name": "my-panel-2",
      "uid": "b2c3d4e5-f6a7-8901-bcde-f2345678901a",
      "global_uid": "prod-eu:b2c3d4e5-f6a7-8901-bcde-f2345678901a",
      "namespace": "krateo-system",
      "group": "widgets.templates.krateo.io",
      "version": "v1beta1",
      "kind": "Panel",
      "resource": "panels",
      "cluster_name": "prod-eu",
      "created_at": "2026-03-02T11:00:00Z",
      "updated_at": "2026-03-07T15:30:00Z"
    }
  ],
  "cursor": "<base64-opaque-token>"
}
```

- `global_uid` is always present: composite key in `cluster_name:uid` format, uniquely identifies a resource across clusters
- `composition_id` appears only when set (non-null)
- `raw` appears only when `raw=true` is requested
- `status_raw` appears only when `status_raw=true` is requested (list)
- `cursor` is absent on the last page or when results fit in a single page

#### Namespace Handling

The `namespace` parameter follows Kubernetes API semantics:

| Value | Behavior |
| --- | --- |
| *(absent/empty)* | Defaults to `"default"`: only resources in the `default` namespace are returned |
| `*` | All namespaces: no namespace filter is applied. RBAC is still enforced, so only resources the user has access to will be returned. |
| `prod`, `krateo-system`, etc. | Exact match on the specified namespace |

This mirrors how `kubectl` works: commands target the `default` namespace unless `-n` or `--all-namespaces` is specified.

#### Sorting

The `sort_by` parameter controls the sort order of results. The default is `resource` (`group`, `version`, `resource (plural)`, `namespace`, `name`). Other options are `created_at`, `updated_at`, `global_uid`, and `composition_id`.

---

### Detail Endpoint — `GET /resources/{global_uid}`

Fetches a single resource by its `global_uid` (format: `cluster_name:uid`, e.g. `prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890`).

The `global_uid` is returned in every list response item, so clients can use it directly to fetch the full resource detail.

This endpoint does **not** support any of the list filters (`group`, `version`, `resource`, `cluster`, `namespace`, `name`, `name_contains`, `labels`, `since`, `limit`, `cursor`). It takes only the `global_uid` path parameter and the optional `raw` and `status_raw` query parameters to control the exclusion or inclusion of the full Kubernetes object and status subtree.

#### Query parameters

| Parameter | Type | Default | Behavior |
| --- | --- | --- | --- |
| `raw` | boolean | `true` | Include the full Kubernetes object. Set `?raw=false` to exclude. |
| `status_raw` | boolean | `true` | Include the Kubernetes status subtree. Set `?status_raw=false` to exclude. Omitted when NULL in the database. |

Note: `raw` and `status_raw` default to `true` on the detail endpoint (opposite of the list endpoint where they default to `false`).

#### Detail response

Same structure as the list endpoint, with `count: 1` and a single item in the `items` array:

```json
{
  "count": 1,
  "items": [
    {
      "name": "my-panel",
      "uid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "global_uid": "prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "namespace": "krateo-system",
      "group": "widgets.templates.krateo.io",
      "version": "v1beta1",
      "kind": "Panel",
      "resource": "panels",
      "cluster_name": "prod-eu",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-06T14:30:00Z",
      "raw": { "..." },
      "status_raw": { "..." }
    }
  ]
}
```

No `cursor` field: there is no pagination on the detail endpoint.

---

### Error Responses

Kubernetes-style `Status` objects:

| Code | Endpoint | Condition |
| --- | --- | --- |
| `400` | List | Invalid/missing parameters (missing group, bad UUID, JSON, timestamp, cursor) |
| `400` | Detail | Missing `global_uid` path parameter |
| `403` | List | Forbidden: RBAC denied access to all discovered resources. If even one is allowed, the request will succeed with just those allowed. |
| `403` | Detail | Forbidden: RBAC denied access to the requested resource |
| `404` | Detail | Resource not found: no active resource matches the given `global_uid` |
| `405` | List | Method not allowed (only GET and POST) |
| `405` | Detail | Method not allowed (only GET) |
| `500` | Both | Internal server error |

See [SEARCH.md](./docs/SEARCH.md) for full examples including pagination loops.

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
| `DB_READY_TIMEOUT` | `5m` | Max wait for PostgreSQL to become ready |

## Testing

```bash
# All tests (requires Docker for testcontainers)
go test ./... -cover

# Unit tests only (fast, uses pgxmock, no Docker)
go test ./internal/sql/ -cover -v

# Integration tests only (real PostgreSQL via testcontainers)
go test ./internal/handlers/ -cover -v
```

See [TESTING.md](./docs/TESTING.md) for detailed testing instructions.

## Notes on RBAC

**RBAC is enforced at resource level and not on the single object**.
For instance, if a user has access to `widgets.templates.krateo.io/panels` in namespace `krateo-system` then they can see all the panels in that namespace.
However, if they don't have access to `widgets.templates.krateo.io/panels` in `krateo-system` then they can't see any panel in that namespace, even if they have access to a specific panel object like `widgets.templates.krateo.io/panels/my-panel` in `krateo-system`.
This practically means that RBAC is enforced on the resource type (group/version/resource) + namespace and not on the single object and so for a user to have access to a specific object they need to have access to the whole resource type in that namespace.

## Health Probes

```
GET /livez   → 200 OK (checks if service is running)
GET /readyz  → 200 OK (checks PostgreSQL connectivity)
```

## Latency information

Information about latency is logged for each request, broken down into phases.
You can find more information about latency in [LATENCY.md](./docs/LATENCY.md).

## License

See [LICENSE](LICENSE).
