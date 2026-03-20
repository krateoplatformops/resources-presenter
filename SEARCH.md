# `/resources` Search Guide

This document provides full examples for querying the resources-presenter API.

## Endpoints Overview

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/resources` | List resources matching filters (query parameters) |
| `POST` | `/resources` | List resources matching filters (JSON body) |
| `GET` | `/resources/{global_uid}` | Get a single resource by `global_uid` |

---

## Resource Item Fields

Both endpoints return items with the same fields:

| Field | Type | Present | Description |
| --- | --- | --- | --- |
| `name` | string | Always | Resource name |
| `uid` | string | Always | Kubernetes UID |
| `global_uid` | string | Always | Composite key (`cluster_name:uid`) — uniquely identifies a resource across clusters |
| `namespace` | string | Always | Kubernetes namespace |
| `group` | string | Always | API group (e.g. `apps`) |
| `version` | string | Always | API version (e.g. `v1`) |
| `kind` | string | Always | Resource kind (e.g. `Deployment`) |
| `resource` | string | Always | Resource plural name (e.g. `deployments`) |
| `cluster_name` | string | Always | Cluster where the resource lives |
| `created_at` | RFC3339 | Always | When the resource was first ingested |
| `updated_at` | RFC3339 | Always | When the resource was last updated |
| `composition_id` | UUID | When set | Krateo composition UUID (omitted when null) |
| `raw` | object | Conditional | Full Kubernetes object. **List:** only when `raw=true`. **Detail:** included by default, use `?raw=false` to exclude. |
| `status_raw` | object | Conditional | Kubernetes status subtree (denormalized). **List:** only when `status_raw=true`. **Detail:** always included when present. Omitted when NULL in DB. |

---

## List Endpoint — `GET /resources` and `POST /resources`

### Resource Resolution

The resource type is identified by the **required** `group` parameter, plus optional narrowing filters:

| Parameter | Example | Required? | Description |
| --- | --- | --- | --- |
| `group` | `apps`, `widgets.templates.krateo.io` | **Yes** | Kubernetes API group |
| `version` | `v1`, `v1beta1` | No | API version (narrows discovery) |
| `resource` | `deployments`, `panels` | No | Resource plural name (lowercase, narrows discovery) |

These map directly to the DB columns `resource_group`, `resource_version`, and `resource_plural`.

When `version` or `resource` are omitted, the handler discovers all matching resources within the group and RBAC-filters them before querying. Missing `group` returns `400`.

### Query Parameters (list only)

All filters (except `group`) are optional and are combined with `AND`.

| Parameter | Type | Behavior |
| --- | --- | --- |
| `group` | string | **Required.** API group. |
| `version` | string | Optional. API version (narrows discovery). |
| `resource` | string | Optional. Resource plural name (lowercase, narrows discovery). |
| `namespace` | string | Optional. Exact match on `namespace`. Absent/empty defaults to `default`. `*` matches all namespaces (still filtered by RBAC). |
| `cluster` | string | Optional. Exact match on `cluster_name`. |
| `composition_id` | UUID | Optional. Exact match on `composition_id`. Must be a valid RFC 4122 UUID. |
| `name` | string | Optional. Exact match on `resource_name`. Mutually exclusive with `name_contains`. |
| `name_contains` | string | Optional. Case-insensitive partial match on `resource_name` (`ILIKE %name_contains%`). Mutually exclusive with `name`. |
| `labels` | JSON object (string) | Optional. JSONB containment on `raw->'metadata'->'labels'` (`@>`). Must be valid JSON. |
| `since` | RFC3339 timestamp | Includes resources with `updated_at >= since`. |
| `raw` | boolean | If `true`, include the full `raw` JSONB field in the response. Default: `false`. |
| `status_raw` | boolean | If `true`, include the `status_raw` JSONB field (Kubernetes status subtree). Default: `false`. |
| `limit` | integer | Page size. Default: `100`. Use `-1` for unlimited (returns all results, no pagination). |
| `cursor` | base64 string | Keyset cursor for pagination (opaque token returned by previous response). |

If `since` is not valid RFC3339, `labels` is not valid JSON, or `composition_id` is not a valid UUID, the API returns `400`.
If `cursor` is invalid base64/JSON, the API returns `400`.

### POST JSON Body (list only)

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
  "status_raw": true,
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
- `status_raw` defaults to `false` when omitted.

### List Response

```json
{
  "count": 2,
  "items": [
    {
      "name": "my-api-service",
      "uid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "global_uid": "cluster-a:a1b2c3d4-e5f6-7890-abcd-ef1234567890",
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
      "global_uid": "cluster-a:b2c3d4e5-f6a7-8901-bcde-f12345678901",
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
- `cursor`: present only if there are more pages; absent or empty on the last page

### List Examples

#### 1) GET: no filters (first page)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels"
```

#### 2) GET: filter by cluster + namespace

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "cluster=cluster-a" \
  --data-urlencode "namespace=default"
```

#### 3) GET: exact match on resource name

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "name=my-api-service"
```

Returns only the resource with `resource_name = 'my-api-service'` (case-sensitive, exact match).

#### 3b) GET: search by resource name (contains, case-insensitive)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "name_contains=api"
```

Matches names like `api`, `API`, `my-api-service`, `api-gateway`.

> `name` and `name_contains` are mutually exclusive: providing both returns `400`.

#### 4) GET: filter by labels

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode 'labels={"app":"nginx","tier":"backend"}'
```

`labels` uses JSONB containment (`@>`): all provided key/value pairs must exist in `metadata.labels`.

#### 5) GET: filter by time (`since`)

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "since=2026-03-01T00:00:00Z"
```

Returns resources with `updated_at >= since`.

#### 6) GET: filter by composition_id

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels" \
  --data-urlencode "composition_id=550e8400-e29b-41d4-a716-446655440000"
```

#### 7) GET: include full raw object

```bash
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels" \
  --data-urlencode "raw=true"
```

#### 8) GET: combine all filters

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

#### 9) POST: same query as JSON body

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

### Pagination (list only)

Uses keyset pagination with fixed order: `updated_at DESC, id DESC`.

1. First call without `cursor`.
2. Read `cursor` from response.
3. Send it back in the next call.

The cursor is built from the last returned row (`updated_at`, `id`) and is opaque to clients (base64-encoded JSON).

When there are no more pages, the `cursor` field is absent in the response.

Pagination is **not available** on the detail endpoint — it always returns exactly 0 or 1 items.

#### GET pagination

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

#### POST pagination

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

#### Automatic pagination loop (Bash + jq)

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

### Sorting (list only)

Fixed order: `updated_at DESC, id DESC`. Currently not user-configurable.

---

## Detail Endpoint — `GET /resources/{global_uid}`

Fetches a single resource by its `global_uid`. This endpoint does **not** accept any of the list filters (`group`, `version`, `resource`, `cluster`, `namespace`, `name`, `name_contains`, `labels`, `since`, `limit`, `cursor`).

### Path parameter

| Parameter | Type | Description |
| --- | --- | --- |
| `global_uid` | string | **Required.** Composite key in `cluster_name:uid` format (e.g. `prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890`). Returned in every list response item. |

### Query parameters (detail only)

| Parameter | Type | Default | Behavior |
| --- | --- | --- | --- |
| `raw` | boolean | `true` | Include the full Kubernetes object. Set `?raw=false` to exclude. |

Note: `raw` defaults to `true` here (opposite of the list endpoint where it defaults to `false`).
`status_raw` is always selected on the detail endpoint (no query parameter needed); it is omitted from the response only when NULL in the database.

### Detail Response

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

- `count` is always `0` or `1`
- No `cursor` field — there is no pagination on the detail endpoint
- `raw` is included by default; only absent when `?raw=false` is set
- `status_raw` is always included on the detail endpoint when present in the database; omitted when NULL

### Detail Examples

```bash
# Fetch a single resource (raw included by default)
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources/prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890' | jq

# Without raw
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources/prod-eu:a1b2c3d4-e5f6-7890-abcd-ef1234567890?raw=false' | jq

# Typical workflow: list → pick global_uid → fetch detail
GLOBAL_UID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=apps&version=v1&resource=deployments&namespace=prod&limit=1' \
  | jq -r '.items[0].global_uid')

curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/resources/$GLOBAL_UID" | jq
```

### Detail Error Responses

| Code | Condition |
| --- | --- |
| `400` | Missing `global_uid` path parameter |
| `403` | Forbidden — RBAC denied access to the requested resource |
| `404` | Resource not found — no active resource matches the given `global_uid` |
| `405` | Method not allowed (only GET is supported) |
| `500` | Internal server error |

---

## Error Responses (all endpoints)

Errors are returned as Kubernetes-style `Status` objects:

| Status Code | List | Detail | Condition |
| --- | --- | --- | --- |
| `400` | Yes | Yes | Invalid or missing parameters |
| `403` | Yes | Yes | Forbidden — RBAC denied access |
| `404` | — | Yes | Resource not found |
| `405` | Yes | Yes | Method not allowed |
| `500` | Yes | Yes | Internal server error |
