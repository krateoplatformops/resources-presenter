# Latency Breakdown

## Middleware chain

Every request passes through this middleware chain before reaching the handler:

```
TraceId → Access(starts timer) → CORS → Gzip → UserConfig → Handler(starts its own timer)
```

Two independent log lines are emitted per request, each measuring a different scope:

| Log line | Field | Scope | Includes UserConfig? |
|----------|-------|-------|---------------------|
| `request completed by handler` (DEBUG) | `handler_duration_ms.6_total` | Handler only (phases 1-5) | No |
| `http request` (INFO) | `latency` | Access middleware → end of handler | **Yes** |

The gap between `latency` and `6_total` is the `UserConfig` middleware.

## List handler phases

| Phase | Field | What it does | Typical latency |
|-------|-------|-------------|-----------------|
| 1 | `1_parse` | Query parameter validation | < 0.1 ms |
| 2 | `2_discovery` | `SELECT DISTINCT` to find (group, resource, namespace) tuples | 1-10 ms |
| 3 | `3_rbac_authz` | Batch `rbac.UserCan()` — `SelfSubjectRulesReview` per namespace with fallback to per-target `SelfSubjectAccessReview` | 30-80 ms |
| 4 | `4_query` | PostgreSQL query execution (filtered to allowed targets via IN clause) | 10-100 ms |
| 5 | `5_serialize` | JSON marshaling into response buffer | < 1 ms |
| - | `6_total` | Sum of phases 1-5 | - |

The discovery phase (2) enumerates all distinct (group, resource, namespace) tuples in the DB matching the request filters. These targets are then batch-checked for RBAC in phase 3 using plumbing's `rbac.UserCan()`, which groups targets by namespace and uses `SelfSubjectRulesReview` (with fallback to individual `SelfSubjectAccessReview` per target). Only allowed targets are passed to the list query via a `(resource_plural, namespace) IN (...)` clause.

## Detail handler phases

The detail handler (`GET /resources/{global_uid}`) has a simpler pipeline: no discovery phase since the resource is fetched directly by `global_uid`.

| Phase | Field | What it does | Typical latency |
|-------|-------|-------------|-----------------|
| 1 | `1_parse` | Path parameter extraction, `raw` query param | < 0.1 ms |
| 2 | `2_query` | `SELECT ... WHERE global_uid = $1` — single row fetch | 1-5 ms |
| 3 | `3_rbac_authz` | Single-target RBAC check on the fetched resource's (group, resource, namespace) | 30-80 ms |
| 4 | `4_serialize` | JSON marshaling into response buffer | < 0.1 ms |
| - | `5_total` | Sum of phases 1-4 | - |

The detail handler skips discovery entirely: it fetches the resource first, then checks RBAC on the single result. This is faster than the list flow for single-resource lookups.

## UserConfig middleware

The `UserConfig` middleware ([plumbing/server/use/userconfig.go](https://github.com/krateoplatformops/plumbing/blob/main/server/use/userconfig.go)) runs **before** the handler and performs two operations:

1. **JWT validation** — decodes and verifies the HS256 token from the `Authorization` header
2. **K8s Secret fetch** — calls `endpoints.FromSecret()` to read the `{username}-clientconfig` Secret from the Kubernetes API server

The Secret fetch is a network call to the K8s API and accounts for a large portion of the total latency.

## Example

```sh
[14:29:34.904] DEBUG: request completed by handler {
  "handler_duration_ms": {
    "1_parse": 0.028,
    "2_discovery": 3.412,     ← PostgreSQL: SELECT DISTINCT targets
    "3_rbac_authz": 48.067,   ← batch UserCan (K8s API call #2)
    "4_query": 49.641,        ← PostgreSQL: filtered list query
    "5_serialize": 0.358,
    "6_total": 101.516        ← handler only
  }, ...
}
[14:29:34.910] INFO: http request {
  "latency": "212.795751ms", ← full chain: UserConfig (~111ms) + handler (~101ms)
  ...
}
```

| Component | Network call |
|-----------|-------------|
| UserConfig middleware | K8s API: fetch `{user}-clientconfig` Secret |
| 2_discovery (list handler) | PostgreSQL: `SELECT DISTINCT` for target enumeration |
| 3_rbac_authz (handler) | K8s API: batch `SelfSubjectRulesReview` / `SelfSubjectAccessReview` |
| 4_query (list handler) | PostgreSQL: filtered list query |
| 2_query (detail handler) | PostgreSQL: `SELECT ... WHERE global_uid = $1` (single row) |
| Other (parse + serialize) | None |

The K8s API calls (`UserConfig` + `3_rbac_authz`) are independent operations against different endpoints: one fetches user credentials, the other checks RBAC permissions. For the **list handler**, the two PostgreSQL calls (`2_discovery` + `4_query`) are sequential: discovery must complete before the RBAC check, and the list query uses the RBAC-filtered targets. For the **detail handler**, there is only one PostgreSQL call (`2_query`) that fetches the resource directly, then RBAC is checked on the single result.
