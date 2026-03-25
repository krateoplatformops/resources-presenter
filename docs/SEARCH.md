# `/resources` Search Guide

This document provides full examples for querying the `resources-presenter` API.

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
| `global_uid` | string | Always | Composite key (`cluster_name:uid`): uniquely identifies a resource across clusters |
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
| `status_raw` | object | Conditional | Kubernetes status subtree. **List:** only when `status_raw=true`. **Detail:** always included when present. Omitted when NULL in DB. |

---

## List Endpoint: `GET /resources` and `POST /resources`

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
| `limit` | integer | Page size. Default: `100`. Minimum: `1`, Maximum: `5000`. |
| `sort_by` | string | Sort column for results. One of: `resource` (default), `created_at`, `updated_at`, `global_uid`, `composition_id`. See [Sorting](#sorting-list-only). |
| `sort_order` | string | Sort direction: `asc` or `desc`. Default depends on `sort_by`: `created_at` and `updated_at` default to `desc`, all others default to `asc`. See [Sorting](#sorting-list-only). |
| `cursor` | base64 string | Keyset cursor for pagination (opaque token returned by previous response). Sort-aware: a cursor from one `sort_by`/`sort_order` combination cannot be reused with a different one. |

If `since` is not valid RFC3339, `labels` is not valid JSON, or `composition_id` is not a valid UUID, the API returns a `400` error.
If `cursor` is invalid base64/JSON, the API returns a `400` error.

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
  "sort_by": "updated_at",
  "sort_order": "desc",
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
- `sort_by` defaults to `resource` when omitted.
- `sort_order` defaults to a sensible direction per `sort_by` column when omitted (`desc` for `created_at`/`updated_at`, `asc` for all others).

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
  --data-urlencode "status_raw=true" \
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
    "status_raw": true,
    "limit": 100
  }'
```

### Pagination (list only)

Uses keyset pagination. The cursor encodes the sort-relevant columns of the last returned row and is opaque to clients (base64-encoded JSON).

1. First call without `cursor`.
2. Read `cursor` from response.
3. Send it back in the next call (with the **same `sort_by` and `sort_order`**).

The cursor is sort-aware: it is only valid for the `sort_by` and `sort_order` combination that produced it. Changing either between pages returns `400`.

When there are no more pages, the `cursor` field is absent in the response.

Pagination is **not available** on the detail endpoint: it always returns exactly 0 or 1 items.

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
RBAC is enforced on every request.

### Sorting (list only)

Control the order of results with `sort_by` (which column) and `sort_order` (which direction).

#### Sort columns (`sort_by`)

| Value | SQL ORDER BY columns | Default direction | Description |
| --- | --- | --- | --- |
| `resource` (default) | `resource_group, resource_version, resource_plural, namespace, resource_name, id` | `asc` | Lexicographic by fully-qualified resource identity |
| `created_at` | `created_at, id` | `desc` | By creation time (newest first by default) |
| `updated_at` | `updated_at, id` | `desc` | By last update time (newest first by default) |
| `global_uid` | `global_uid, id` | `asc` | By composite key (`cluster:uid`) |
| `composition_id` | `composition_id, id` | `asc` | By composition UUID; NULL handling depends on direction |

Every sort order includes `id` as a tiebreaker to guarantee a deterministic, stable order even when the primary sort column has duplicate values.

#### Sort direction (`sort_order`)

| Value | Description |
| --- | --- |
| `asc` | Ascending order (smallest/oldest first) |
| `desc` | Descending order (largest/newest first) |
| *(omitted)* | Uses the default direction for the chosen `sort_by` column (see table above) |

You can override the default direction by explicitly passing `sort_order`. For example, `sort_by=created_at&sort_order=asc` returns oldest resources first (reversing the default `desc`).

#### NULL handling

`composition_id` is the only nullable sort column. With `asc` (default), NULLs sort **after** all non-NULL values (`NULLS LAST`). With `desc`, NULLs sort **before** all non-NULL values (`NULLS FIRST`). When paginating across the NULL boundary, the cursor tracks whether the last row had a NULL `composition_id` so pagination continues correctly.

#### Invalid values

An unrecognised `sort_by` value returns `400 Bad Request`. An unrecognised `sort_order` value (anything other than `asc`, `desc`, or empty) also returns `400 Bad Request`.

#### Examples

```bash
# Sort by most recently updated (default direction: desc)
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "sort_by=updated_at"

# Sort by creation time ascending (oldest first, overriding default desc)
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "sort_by=created_at" \
  --data-urlencode "sort_order=asc" \
  --data-urlencode "limit=50"

# Sort by composition_id (default: ASC, NULLs last)
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=widgets.templates.krateo.io" \
  --data-urlencode "version=v1beta1" \
  --data-urlencode "resource=panels" \
  --data-urlencode "sort_by=composition_id"

# Sort by resource name descending (overriding default asc)
curl --get "http://localhost:8080/resources" \
  --data-urlencode "group=apps" \
  --data-urlencode "version=v1" \
  --data-urlencode "resource=deployments" \
  --data-urlencode "sort_by=resource" \
  --data-urlencode "sort_order=desc"

# POST with sort_by and sort_order
curl --request POST "http://localhost:8080/resources" \
  --header "Content-Type: application/json" \
  --data '{
    "group": "apps",
    "version": "v1",
    "resource": "deployments",
    "sort_by": "created_at",
    "sort_order": "asc",
    "limit": 100
  }'
```

#### Cursor and sort compatibility

Cursors are bound to the `sort_by` **and** `sort_order` that produced them. If you change either between pages, the API returns `400`:

```
cursor was created with sort_by="updated_at" but request uses sort_by="created_at"; cursors are not reusable across sort orders
```

```
cursor was created with sort_order="desc" but request uses sort_order="asc"; cursors are not reusable across sort orders
```

To change sort column or direction, start a new pagination sequence (omit `cursor`).

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
| `status_raw` | boolean | `true` | Include the Kubernetes status subtree. Set `?status_raw=false` to exclude. |

Note: `raw` and `status_raw` both default to `true` here (opposite of the list endpoint where they default to `false`).

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
- No `cursor` field: there is no pagination on the detail endpoint
- `raw` is included by default; only absent when `?raw=false` is set
- `status_raw` is included by default; only absent when `?status_raw=false` is set

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
| `403` | Forbidden: RBAC denied access to the requested resource |
| `404` | Resource not found — no active resource matches the given `global_uid` |
| `405` | Method not allowed (only GET is supported) |
| `500` | Internal server error |

---

## Error Responses (all endpoints)

Errors are returned as Kubernetes-style `Status` objects:

| Status Code | List | Detail | Condition |
| --- | --- | --- | --- |
| `400` | Yes | Yes | Invalid or missing parameters |
| `403` | Yes | Yes | Forbidden: RBAC denied access |
| `404` | — | Yes | Resource not found |
| `405` | Yes | Yes | Method not allowed |
| `500` | Yes | Yes | Internal server error |
