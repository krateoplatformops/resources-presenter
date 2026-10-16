# `/can-i` Permission Check Guide

`POST /can-i` checks a batch of permissions for the current user in one call: a batch of `SelfSubjectAccessReview`s. It does not use the database.

## Request

```json
{
  "checks": [
    {"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "dev"},
    {"verb": "list", "group": "apps", "resource": "deployments"}
  ]
}
```

| Field | Required | Description |
| --- | --- | --- |
| `checks` | Yes | 1 to 5000 checks |
| `checks[].verb` | Yes | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` or `deletecollection` |
| `checks[].group` | No | API group. `""` is the core group |
| `checks[].resource` | Yes | Resource plural name (e.g. `deployments`) |
| `checks[].namespace` | No | `""` or absent means all namespaces |

- Checks may mix verbs, resources and namespaces.
- Unknown fields are rejected (`400`).
- Object names and subresources are not supported.

## Response

```json
{
  "items": [
    {"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "dev", "allowed": true},
    {"verb": "list", "group": "apps", "resource": "deployments", "namespace": "", "allowed": false}
  ]
}
```

- One item per check, in request order.
- An RBAC error counts as `allowed: false`.

## How checks are resolved

1. For each distinct (verb, group, resource), the cluster-wide permission is checked first. If allowed, every check for it is allowed with no further call.
2. The remaining namespaced checks go through plumbing's batch `rbac.UserCan()`: one `SelfSubjectRulesReview` per namespace, in sequence, with fallback to per-check `SelfSubjectAccessReview`.

| User | K8s calls (N namespaces, one resource) |
| --- | --- |
| Cluster-wide permission (e.g. admin) | 2 |
| Namespace-scoped | 2 + N |
| Cached | 0 |

Decisions share the `/resources` RBAC cache (keyed by user client certificate, verb, group, resource, namespace). A change to the user's RBAC can take up to the cache TTL to show.

## Examples

#### 1) One resource across namespaces

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"checks": [
        {"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "dev"},
        {"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": "prod"}
      ]}' \
  http://localhost:8080/can-i | jq
```

#### 2) Mixed checks

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"checks": [
        {"verb": "get", "group": "widgets.templates.krateo.io", "resource": "panels", "namespace": "krateo-system"},
        {"verb": "delete", "group": "", "resource": "secrets", "namespace": "krateo-system"},
        {"verb": "list", "group": "", "resource": "namespaces"}
      ]}' \
  http://localhost:8080/can-i | jq
```

#### 3) Allowed namespaces only (jq)

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"checks": [{"verb": "get", "resource": "pods", "namespace": "a"}, {"verb": "get", "resource": "pods", "namespace": "b"}]}' \
  http://localhost:8080/can-i | jq '[.items[] | select(.allowed) | .namespace]'
```

#### 4) From a RESTAction

A stage with `exportJwt: true` reaches the presenter through its endpoint Secret. Here `.getNamespaces` is an earlier stage's list of namespace names:

```yaml
- name: canCreate
  dependsOn:
    name: getNamespaces
  endpointRef:
    name: resources-presenter-endpoint
    namespace: krateo-system
  exportJwt: true
  path: /can-i
  verb: POST
  headers:
    - "Content-Type: application/json"
  payload: |
    ${ {"checks": [.getNamespaces[] | {"verb": "create", "group": "composition.krateo.io", "resource": "cmtests", "namespace": .}]} }
```

## Error Responses

| Code | Condition |
| --- | --- |
| `400` | Invalid JSON, unknown field, empty or too many checks, bad verb, missing `resource`, field too long |
| `401` | Missing or invalid token, or no user client config |
| `405` | Method not allowed (only POST) |
| `500` | Internal server error |
