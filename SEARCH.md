# `/resources` Search Guide

This document explains how to query `/resources` in resources-presenter.

Supported methods:

- `GET /resources` with query parameters
- `POST /resources` with a JSON body

## What `/resources` Returns

Each item in the response represents the current state of a Kubernetes resource (one row per `global_uid`).

Each item includes: `name`, `namespace`, `group`, `version`, `kind`, `cluster_name`, `created_at`, `updated_at`, and `composition_id` (when set).
Use `raw=true` to also include the full Kubernetes object under the `raw` field.

## Resource Resolution

The resource type is identified by three **required** parameters:

| Parameter | Example | Description |
| --- | --- | --- |
| `group` | `apps`, `widgets.templates.krateo.io` | Kubernetes API group |
| `version` | `v1`, `v1beta1` | API version |
| `resource` | `deployments`, `panels` | Resource plural name (lowercase) |

These map directly to the DB columns `resource_group`, `resource_version`, and `resource_plural`.

Missing any of the three returns `400`.

## Query Parameters

All filters (except `group`, `version`, `resource`) are optional and are combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `group` | string | **Required.** API group. |
| `version` | string | **Required.** API version. |
| `resource` | string | **Required.** Resource plural name (lowercase). |
| `cluster` | string | Exact match on `cluster_name`. |
| `namespace` | string | Exact match on `namespace`. |
| `composition_id` | UUID | Exact match on `composition_id`. Must be a valid RFC 4122 UUID. |
| `name` | string | Case-insensitive partial match on `resource_name` (`ILIKE %name%`). |
| `labels` | JSON object (string) | JSONB containment on `raw->'metadata'->'labels'` (`@>`). Must be valid JSON. |
| `since` | RFC3339 timestamp | Includes resources with `updated_at >= since`. |
| `raw` | boolean | If `true`, include the full `raw` JSONB field in the response. Default: `false`. |
| `limit` | integer | Page size. Default: `100`, max: `5000`. If `<= 0`, default is used. |
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
  "name": "api",
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

- `group`, `version`, and `resource` are **required** in the JSON body too.
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

### 3) GET: search by resource name (contains, case-insensitive)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "name=api"
```

Matches names like `api`, `API`, `my-api-service`, `api-gateway`.

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
  --data-urlencode "name=api" \
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
    "name": "api",
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
      "namespace": "prod",
      "group": "apps",
      "version": "v1",
      "kind": "Deployment",
      "cluster_name": "cluster-a",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-06T14:30:00Z"
    },
    {
      "name": "api-gateway",
      "namespace": "prod",
      "group": "apps",
      "version": "v1",
      "kind": "Deployment",
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
  - `cluster_name`: cluster where the resource lives
  - `created_at`: when the resource was first ingested (RFC3339)
  - `updated_at`: when the resource was last updated (RFC3339)
  - `composition_id`: Krateo composition UUID (omitted when null)
  - `raw`: full Kubernetes object (only when `raw=true`)
- `cursor`: present only if there are more pages; absent or empty on the last page

## Sorting

Fixed order: `updated_at DESC, id DESC`. Not user-configurable.

## Error Responses

Errors are returned as Kubernetes-style `Status` objects:

| Status Code | Condition |
| --- | --- |
| `400` | Invalid or missing parameters (missing group/version/resource, bad UUID, JSON, timestamp, limit, cursor) |
| `405` | Method not allowed (only GET and POST are supported) |
| `500` | Internal server error (database failure, serialization error) |
