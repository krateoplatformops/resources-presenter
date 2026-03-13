# Latency Breakdown

TODO: to be rewritten

## Middleware chain

Every request passes through this middleware chain before reaching the handler:

```
TraceId → Access(starts timer) → CORS → UserConfig → Handler(starts its own timer)
```

Two independent log lines are emitted per request, each measuring a different scope:

| Log line | Field | Scope | Includes UserConfig? |
|----------|-------|-------|---------------------|
| `request completed by handler` (DEBUG) | `handler_duration_ms.5_total` | Handler only (phases 1-4) | No |
| `http request` (INFO) | `latency` | Access middleware → end of handler | **Yes** |

The gap between `latency` and `5_total` is the `UserConfig` middleware.

## Handler phases

| Phase | Field | What it does | Typical latency |
|-------|-------|-------------|-----------------|
| 1 | `1_parse` | Query parameter validation | < 0.1 ms |
| 2 | `2_rbac_authz` | `SelfSubjectAccessReview` against K8s API | 30-80 ms |
| 3 | `3_query` | PostgreSQL query execution | 10-100 ms |
| 4 | `4_serialize` | JSON marshaling into response buffer | < 1 ms |
| - | `5_total` | Sum of phases 1-4 | - |

## UserConfig middleware

The `UserConfig` middleware ([plumbing/server/use/userconfig.go](../plumbing/server/use/userconfig.go)) runs **before** the handler and performs two operations:

1. **JWT validation** — decodes and verifies the HS256 token from the `Authorization` header
2. **K8s Secret fetch** — calls `endpoints.FromSecret()` to read the `{username}-clientconfig` Secret from the Kubernetes API server

The Secret fetch is a network call to the K8s API and accounts for a large portion of the total latency.

## Example

```
[14:29:34.904] DEBUG: request completed by handler {
  "handler_duration_ms": {
    "1_parse": 0.028,
    "2_rbac_authz": 48.067,   ← SelfSubjectAccessReview (K8s API call #2)
    "3_query": 49.641,        ← PostgreSQL
    "4_serialize": 0.358,
    "5_total": 98.104         ← handler only
  }, ...
}
[14:29:34.910] INFO: http request {
  "latency": "209.795751ms", ← full chain: UserConfig (~111ms) + handler (~98ms)
  ...
}
```

| Component | Network call |
|-----------|-------------|
| UserConfig middleware | K8s API: fetch `{user}-clientconfig` Secret |
| 2_rbac_authz (handler) | K8s API: `SelfSubjectAccessReview` |
| 3_query (handler) | PostgreSQL query |
| Other (parse + serialize) | None |

The two K8s API calls (`UserConfig` + `2_rbac_authz`) are independent operations against different endpoints: one fetches user credentials, the other checks RBAC permissions.
