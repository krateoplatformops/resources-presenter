# `/resources` Search Guide

This document explains how to query `/resources` in resources-presenter.

Supported methods:

- `GET /resources` with query parameters
- `POST /resources` with a JSON body

## What `/resources` Returns

Each item in the response represents the current state of a Kubernetes resource (one row per `global_uid`).

Each item includes: `name`, `uid`, `namespace`, `group`, `version`, `kind`, `resource`, `cluster_name`, `created_at`, `updated_at`, and `composition_id` (when set).
Use `raw=true` to also include the full Kubernetes object under the `raw` field.

## Resource Resolution

The resource type is identified by the **required** `group` parameter, plus optional narrowing filters:

| Parameter | Example | Required? | Description |
| --- | --- | --- | --- |
| `group` | `apps`, `widgets.templates.krateo.io` | **Yes** | Kubernetes API group |
| `version` | `v1`, `v1beta1` | No | API version (narrows discovery) |
| `resource` | `deployments`, `panels` | No | Resource plural name (lowercase, narrows discovery) |

These map directly to the DB columns `resource_group`, `resource_version`, and `resource_plural`.

When `version` or `resource` are omitted, the handler discovers all matching resources within the group and RBAC-filters them before querying. Missing `group` returns `400`.

## Query Parameters

All filters (except `group`) are optional and are combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `group` | string | **Required.** API group. |
| `version` | string | Optional. API version (narrows discovery). |
| `resource` | string | Optional. Resource plural name (lowercase, narrows discovery). |
| `cluster` | string | Exact match on `cluster_name`. |
| `namespace` | string | Exact match on `namespace`. |
| `composition_id` | UUID | Exact match on `composition_id`. Must be a valid RFC 4122 UUID. |
| `name` | string | Exact match on `resource_name`. Mutually exclusive with `name_contains`. |
| `name_contains` | string | Case-insensitive partial match on `resource_name` (`ILIKE %name_contains%`). Mutually exclusive with `name`. |
| `labels` | JSON object (string) | JSONB containment on `raw->'metadata'->'labels'` (`@>`). Must be valid JSON. |
| `since` | RFC3339 timestamp | Includes resources with `updated_at >= since`. |
| `raw` | boolean | If `true`, include the full `raw` JSONB field in the response. Default: `false`. |
| `limit` | integer | Page size. Default: `100`. Use `-1` for unlimited (returns all results, no pagination). |
| `cursor` | base64 string | Keyset cursor for pagination (opaque token returned by previous response). |

If `since` is not valid RFC3339, `labels` is not valid JSON, or `composition_id` is not a valid UUID, the API returns `400`.
If `cursor` is invalid base64/JSON, the API returns `400`.

## POST JSON Body

For `POST /resources`, use the same fields in JSON format.

Example body:

```json
{
  "group": "apps",
  "version": "v1",
  "resource": "deployments",
  "cluster": "cluster-a",
  "namespace": "prod",
  "composition_id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "my-api-service",
  "labels": {
    "app": "payments"
  },
  "since": "2026-03-01T00:00:00Z",
  "raw": true,
  "limit": 100,
  "cursor": "<cursor-from-previous-page>"
}
```

Notes:

- `group` is **required** in the JSON body; `version` and `resource` are optional.
- Unknown JSON fields return `400`.
- Empty body returns `400`.
- `limit` defaults to `100` when omitted or `<= 0`.
- `raw` defaults to `false` when omitted.

## Search Examples

### 1) GET: no filters (first page)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels"
```

### 2) GET: filter by cluster + namespace

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "cluster=cluster-a" \
  --data-urlencode "namespace=default"
```

### 3) GET: exact match on resource name

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "name=my-api-service"
```

Returns only the resource with `resource_name = 'my-api-service'` (case-sensitive, exact match).

### 3b) GET: search by resource name (contains, case-insensitive)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "name_contains=api"
```

Matches names like `api`, `API`, `my-api-service`, `api-gateway`.

> `name` and `name_contains` are mutually exclusive: providing both returns `400`.

### 4) GET: filter by labels

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode 'labels={"app":"nginx","tier":"backend"}'
```

`labels` uses JSONB containment (`@>`): all provided key/value pairs must exist in `metadata.labels`.

### 5) GET: filter by time (`since`)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "since=2026-03-01T00:00:00Z"
```

Returns resources with `updated_at >= since`.

### 6) GET: filter by composition_id

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels" \
  --data-urlencode "composition_id=550e8400-e29b-41d4-a716-446655440000"
```

### 7) GET: include full raw object

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels" \
  --data-urlencode "raw=true"
```

### 8) GET: combine all filters

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "cluster=cluster-a" \
  --data-urlencode "namespace=prod" \
  --data-urlencode "name_contains=api" \
  --data-urlencode 'labels={"app":"payments"}' \
  --data-urlencode "since=2026-03-01T00:00:00Z" \
  --data-urlencode "raw=true" \
  --data-urlencode "limit=100"
```

### 9) POST: same query as JSON body

```bash
curl --request POST "http://localhost:8080/resources" \
  --header "Content-Type: application/json" \
  --data '{
    "group": "apps",
    "version": "v1",
    "resource": "deployments",
    "cluster": "cluster-a",
    "namespace": "prod",
    "name_contains": "api",
    "labels": {"app": "payments"},
    "since": "2026-03-01T00:00:00Z",
    "raw": true,
    "limit": 100
  }'
```

## Pagination with `cursor`

Uses keyset pagination with fixed order: `updated_at DESC, id DESC`.

1. First call without `cursor`.
2. Read `cursor` from response.
3. Send it back in the next call.

The cursor is built from the last returned row (`updated_at`, `id`) and is opaque to clients (base64-encoded JSON).

When there are no more pages, the `cursor` field is absent in the response.

### GET pagination

```bash
# page 1
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "cluster=cluster-a" \
  --data-urlencode "limit=100"

# page 2 (use cursor from page 1 response)
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "cluster=cluster-a" \
  --data-urlencode "limit=100" \
  --data-urlencode "cursor=<CURSOR_PAGE_1>"
```

### POST pagination

```bash
# page 1
curl --request POST "http://localhost:8080/resources" \
  --header "Content-Type: application/json" \
  --data '{
    "group": "apps",
    "version": "v1",
    "resource": "deployments",
    "cluster": "cluster-a",
    "limit": 100
  }'

# page 2 (use cursor from page 1 response)
curl --request POST "http://localhost:8080/resources" \
  --header "Content-Type: application/json" \
  --data '{
    "group": "apps",
    "version": "v1",
    "resource": "deployments",
    "cluster": "cluster-a",
    "limit": 100,
    "cursor": "<CURSOR_PAGE_1>"
  }'
```

### Automatic pagination loop (Bash + jq)

```bash
BASE_URL="http://localhost:8080/resources"
CURSOR=""

while true; do
  if [ -n "$CURSOR" ]; then
    RESP=$(curl --silent --get "$BASE_URL" \
      --data-urlencode "group=apps" \
      --data-urlencode "version=v1" \
      --data-urlencode "resource=deployments" \
      --data-urlencode "cluster=cluster-a" \
      --data-urlencode "limit=100" \
      --data-urlencode "cursor=$CURSOR")
  else
    RESP=$(curl --silent --get "$BASE_URL" \
      --data-urlencode "group=apps" \
      --data-urlencode "version=v1" \
      --data-urlencode "resource=deployments" \
      --data-urlencode "cluster=cluster-a" \
      --data-urlencode "limit=100")
  fi

  # process current page
  echo "$RESP" | jq '.items[]'

  # read next cursor
  CURSOR=$(echo "$RESP" | jq -r '.cursor // ""')
  [ -z "$CURSOR" ] && break
done
```

If you change filters between pages, pagination continuity is broken.

## Response Format

```json
{
  "count": 2,
  "items": [
    {
      "name": "my-api-service",
      "uid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "namespace": "prod",
      "group": "apps",
      "version": "v1",
      "kind": "Deployment",
      "resource": "deployments",
      "cluster_name": "cluster-a",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-06T14:30:00Z"
    },
    {
      "name": "api-gateway",
      "uid": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "namespace": "prod",
      "group": "apps",
      "version": "v1",
      "kind": "Deployment",
      "resource": "deployments",
      "cluster_name": "cluster-a",
      "created_at": "2026-02-15T08:00:00Z",
      "updated_at": "2026-03-06T14:25:00Z",
      "composition_id": "550e8400-e29b-41d4-a716-446655440000",
      "raw": { "..." }
    }
  ],
  "cursor": "<base64-opaque-token>"
}
```

- `count`: number of items in this page
- `items`: array of resources with the following fields:
  - `name`: resource name
  - `namespace`: Kubernetes namespace
  - `group`: API group (e.g. `apps`)
  - `version`: API version (e.g. `v1`)
  - `kind`: resource kind (e.g. `Deployment`)
  - `resource`: resource plural name (e.g. `deployments`) — useful for constructing API server URLs
  - `cluster_name`: cluster where the resource lives
  - `created_at`: when the resource was first ingested (RFC3339)
  - `updated_at`: when the resource was last updated (RFC3339)
  - `composition_id`: Krateo composition UUID (omitted when null)
  - `raw`: full Kubernetes object (only when `raw=true`)
- `cursor`: present only if there are more pages; absent or empty on the last page

## Sorting

Fixed order: `updated_at DESC, id DESC`. Currently not user-configurable.

## Error Responses

Errors are returned as Kubernetes-style `Status` objects:

| Status Code | Condition |
| --- | --- |
| `400` | Invalid or missing parameters (missing group, bad UUID, JSON, timestamp, limit, cursor) |
| `403` | Forbidden — RBAC denied access to all discovered resources |
| `405` | Method not allowed (only GET and POST are supported) |
| `500` | Internal server error (database failure, serialization error) |
