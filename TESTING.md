# Testing Guide

## 1. Unit Tests

Unit tests use `pgxmock` to mock the `pgx` database interface. No Docker, no Kubernetes — they run instantly.

```bash
# SQL layer (query construction, cursor encoding, row scanning, filter combinations)
go test ./internal/sql/ -v -cover

# Config parsing
go test ./internal/config/ -v -cover

# All unit tests
go test ./internal/sql/ ./internal/config/ -v -cover
```

## 2. Integration Tests

Integration tests use `testcontainers-go` to spin up a real PostgreSQL container. They test the HTTP handler layer end-to-end with a real database, but use a **mock authorizer** (no Kubernetes needed).

**Requires:** Docker running.

```bash
# Handler layer (pagination, filtering, RBAC mock, POST, validation)
go test ./internal/handlers/ -v -cover

# All tests (unit + integration)
go test ./... -v -cover
```

## 3. In-Cluster E2E Testing (kind)

This is the only way to test the full authentication and RBAC flow, because:
- `use.UserConfig` middleware reads `{username}-clientconfig` Secrets from Kubernetes
- `rbac.UserCan()` creates `SelfSubjectAccessReview` resources against the K8s API
- Both require in-cluster config (`/var/run/secrets/kubernetes.io/serviceaccount/token`)

### 3.1 Prerequisites

| Tool | Purpose |
|---|---|---|
| `docker` | Container runtime |
| `kind` | Local K8s cluster in Docker |
| `kubectl` | K8s CLI |
| `helm` | Chart templating |
| `krateoctl` | User creation (JWT + clientconfig Secret) |

### 3.2 Create the kind cluster

```bash
kind create cluster --name krateo-e2e

# Check cluster is running and kubectl context is set
kubectl cluster-info --context kind-krateo-e2e

# Create namespace for testing
kubectl create ns krateo-system
```

### 3.3 Deploy PostgreSQL

Deploys an ephemeral Postgres pod with the schema and 500 seed Panel resources pre-loaded via init scripts. No PVC — data is lost when the pod is deleted.

```bash
kubectl apply -f deploy/test-postgres.yaml
kubectl wait --for=condition=ready pod -l app=postgres -n krateo-system --timeout=120s
```

Verify the database is populated:

```bash
kubectl exec -n krateo-system deploy/postgres -- \
  psql -U krateo -d krateo -c "SELECT count(*) FROM krateo_resources;"
```

Expected output: `500`.

```bash
kubectl exec -n krateo-system deploy/postgres -- \
  psql -U krateo -d krateo -c "SELECT cluster_name, namespace, resource_kind, resource_name FROM krateo_resources;"
```

Expected output: a table of 500 rows with various cluster/namespace/kind combinations.


### 3.4 Build and load the service image

```bash
docker build -t resources-presenter:dev .
kind load docker-image resources-presenter:dev --name krateo-e2e
```

### 3.5 Deploy resources-presenter

Render the Helm chart with `values.e2e.yaml` (which points to the local Docker image and has test-specific config) and apply it to the cluster:
```bash
helm template resources-presenter ./chart \
  -n krateo-system \
  -f ./chart/values.e2e.yaml | kubectl apply -f -
```

Wait for the pod to be ready:

```bash
kubectl wait --for=condition=ready pod \
  -l app.kubernetes.io/name=resources-presenter \
  -n krateo-system --timeout=120s
```

Verify readiness:

```bash
kubectl logs -n krateo-system -l app.kubernetes.io/name=resources-presenter
```

You should see:
```sh
[23:02:41.997] DEBUG: database connection URL {
  "cfg.DbURL": "postgres://krateo:krateo@postgres.krateo-system.svc.cluster.local:5432/krateo?connect_timeout=5\u0026sslmode=disable",
  "service": "resources-presenter"
}
[23:02:42.163] INFO: PostgreSQL is ready {
  "service": "resources-presenter"
}
[23:02:42.164] INFO: application is ready {
  "service": "resources-presenter"
}
[23:02:42.164] INFO: starting HTTP server {
  "port": 8080,
  "service": "resources-presenter"
}
```

**What the chart deploys:**
- **Deployment** — resources-presenter pod with all env vars from `values.e2e.yaml`
- **Service** — ClusterIP on port 8080
- **ServiceAccount** — with `automountServiceAccountToken: true`
- **ClusterRole** — permission to create `SelfSubjectAccessReview` (for RBAC checks)
- **ClusterRoleBinding** — binds the ServiceAccount to the ClusterRole
- **Role** (in `krateo-system`) — permission to read Secrets (for user clientconfig)
- **RoleBinding** (in `krateo-system`) — binds the ServiceAccount to the Role

### 3.6 Create a test user

`krateoctl add-user` generates a signed JWT **and** creates a `{username}-clientconfig` Secret in Kubernetes with a client certificate for RBAC.

```bash
# The SIGN_KEY must match the one in values.e2e.yaml
# NOTE: flags MUST come before the positional argument (username).
# Go's flag package stops parsing at the first non-flag argument.
TOKEN=$(krateoctl add-user \
  -n krateo-system \
  -k "e2e-test-sign-key" \
  -g devs \
  -d 24h \
  e2euser)

echo "TOKEN=$TOKEN"
```

What this does:
1. Creates a JWT (HS256) signed with `e2e-test-sign-key`, containing claims `{username: "e2euser", groups: ["devs"]}`
2. Generates a client certificate via CertificateSigningRequest (Organization=`devs`)
3. Creates Secret `e2euser-clientconfig` in `krateo-system` with the client cert + key + cluster CA

Verify the Secret was created:

```bash
kubectl get secret e2euser-clientconfig -n krateo-system
```

### 3.7 Grant the test user access to Panels

The RBAC check (`SelfSubjectAccessReview`) verifies that the user has `get` permission on `panels` in `widgets.templates.krateo.io`. Create a Role + RoleBinding for the `devs` group:

```bash
kubectl apply -f deploy/test-user-rbac.yaml
```

This grants:
- **Role** `e2e-test-user-panels` in `krateo-system` — `get`, `list` on `panels` in `widgets.templates.krateo.io`
- **RoleBinding** — binds Group `devs` to the Role

### 3.8 Test the endpoint

Start port-forwarding:

```bash
kubectl port-forward svc/resources-presenter 8080:8080 -n krateo-system &
PF_PID=$!
```

**Health probes** (no auth required):

```bash
curl -v -s http://localhost:8080/livez
curl -v -s http://localhost:8080/readyz
```

**List Panels** (should return 200 with items):

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system' | jq
```

**List Panels with full raw objects:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system&raw=true&limit=2' | jq
```

**Filter by cluster:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system&cluster=prod-eu' | jq
```

**Search by name** (case-insensitive partial match):

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system&name=blueprints' | jq
```

**Filter by labels:**

```bash
curl --get -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources' \
  --data-urlencode 'group=widgets.templates.krateo.io' \
  --data-urlencode 'version=v1beta1' \
  --data-urlencode 'kind=Panel' \
  --data-urlencode 'resource=panels' \
  --data-urlencode 'namespace=krateo-system' \
  --data-urlencode 'labels={"app.kubernetes.io/part-of":"dashboard"}' | jq
```

**Pagination:**

```bash
# Page 1
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system&limit=5' | jq

# Copy the "cursor" value from the response, then:
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system&limit=5&cursor=<CURSOR>' | jq
```

**POST with JSON body:**

```bash
curl -s -X POST http://localhost:8080/resources \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "group": "widgets.templates.krateo.io",
    "version": "v1beta1",
    "kind": "Panel",
    "resource": "panels",
    "namespace": "krateo-system",
    "cluster": "prod-eu",
    "raw": true,
    "limit": 5
  }' | jq
```

**Expected error cases:**

```bash
# Missing namespace → 400
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels' | jq

# Missing auth header → 401
curl -s 'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system' | jq

# RBAC denied (namespace the user has no access to) → 403
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=kube-system' | jq
```

Stop port-forwarding:

```bash
kill $PF_PID
```

### 3.9 Teardown

```bash
kind delete cluster --name krateo-e2e
```

### 3.10 Quick reference (all commands)

```bash
# Full setup (copy-paste block)
kind create cluster --name krateo-e2e
kubectl apply -f deploy/test-postgres.yaml
kubectl wait --for=condition=ready pod -l app=postgres -n krateo-system --timeout=120s
docker build -t resources-presenter:dev .
kind load docker-image resources-presenter:dev --name krateo-e2e
helm template resources-presenter ./chart -n krateo-system -f ./chart/values.e2e.yaml | kubectl apply -f -
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=resources-presenter -n krateo-system --timeout=120s
TOKEN=$(krateoctl add-user -n krateo-system -k "e2e-test-sign-key" -g devs -d 24h e2euser)
kubectl apply -f deploy/test-user-rbac.yaml
kubectl port-forward svc/resources-presenter 8080:8080 -n krateo-system &

# Test
curl -s http://localhost:8080/readyz | jq
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/resources?group=widgets.templates.krateo.io&version=v1beta1&kind=Panel&resource=panels&namespace=krateo-system' | jq

# Teardown
kind delete cluster --name krateo-e2e
```

---

## 4. Benchmarks

Benchmarks live in `internal/sql/bench_test.go` and measure the hot paths of the query pipeline.

### What is benchmarked

| Benchmark | What it measures |
|---|---|
| `BenchmarkCursorEncode` | Encoding a keyset cursor to base64 |
| `BenchmarkCursorDecode` | Decoding a base64 cursor back to struct |
| `BenchmarkCursorRoundtrip` | Full encode + decode cycle |
| `BenchmarkBuildListQuery_Minimal` | SQL builder with only resource_kind filter |
| `BenchmarkBuildListQuery_AllFilters` | SQL builder with all 7 filters + cursor active |
| `BenchmarkSplitResourceKind` | Parsing `apiVersion.Kind` strings |
| `BenchmarkEscapeLIKE` | Escaping LIKE special characters |
| `BenchmarkJSONMarshal_10/100/1000` | Serializing result sets of varying sizes |
| `BenchmarkJSONMarshal_1000_WithRaw` | Serializing 1000 items including raw JSONB |
| `BenchmarkListResources_10/100/1000rows` | Full query + scan + pagination with pgxmock |
| `BenchmarkListResources_1000rows_raw` | Same with raw JSONB included |

### Running benchmarks

```bash
# Quick sanity check (1 iteration, fast)
./scripts/bench.sh quick

# Full run (6 iterations for statistical reliability, saves to benchmarks/)
./scripts/bench.sh run

# Full run with custom options
./scripts/bench.sh run -count 10 -benchtime 2s

# Manual (without script)
go test ./internal/sql/ -bench=. -benchmem -count=6 -run='^$'

# Run specific benchmark
go test ./internal/sql/ -bench=BenchmarkListResources -benchmem -count=6 -run='^$'
```

### Saving and comparing results

```bash
# 1. Save a baseline (before making changes)
./scripts/bench.sh baseline

# 2. Make your code changes...

# 3. Run benchmarks again
./scripts/bench.sh run

# 4. Compare against baseline
./scripts/bench.sh compare
```

The `compare` command uses [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat):

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

### Reading benchmark output

```
BenchmarkListResources_100rows-8    7315    160107 ns/op    55023 B/op    1063 allocs/op
```

| Column | Meaning |
|---|---|
| `-8` | GOMAXPROCS (number of CPUs used) |
| `7315` | Number of iterations the benchmark ran |
| `160107 ns/op` | Nanoseconds per operation (~0.16ms) |
| `55023 B/op` | Bytes allocated per operation |
| `1063 allocs/op` | Heap allocations per operation |

---

## 5. Stress Tests

Stress tests live in `internal/sql/stress_test.go` and verify correctness under extreme or edge-case conditions.

### What is tested

| Test | What it verifies |
|---|---|
| `TestListResources_MaxLimit` | Correct behavior at max limit (5000 rows, no next page) |
| `TestListResources_MaxLimitWithNextPage` | Cursor is correctly set when 5001 rows exist |
| `TestListResources_RawLargePayload` | Handling of ~50KB raw JSONB objects |
| `TestListResources_ConcurrentAccess` | 50 goroutines querying simultaneously (race safety) |
| `TestBuildListQuery_AllFilterCombinations` | All 64 combinations of 6 optional filters |
| `TestEscapeLIKE` | LIKE special character escaping (`%`, `_`, `\`) |

### Running stress tests

```bash
# All stress tests with race detector
go test ./internal/sql/ -race -v

# Concurrent test specifically
go test ./internal/sql/ -run=TestListResources_Concurrent -race -v
```

---

## 6. Running everything

```bash
# Unit + stress tests (no Docker, no K8s)
go test ./internal/sql/ ./internal/config/ -race -cover -v

# Unit + integration tests (Docker required)
go test ./... -race -cover -v

# E2E tests: follow section 3 above (kind cluster required)
```
