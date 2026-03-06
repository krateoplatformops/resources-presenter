# Testing Guide

## Test dependencies

| Dependency | Purpose |
|---|---|
| `github.com/pashagolub/pgxmock/v4` | **Unit tests** — mocks the `pgx` database interface so SQL-layer tests (`internal/sql/resources_test.go`) run instantly without a real database. Verifies query construction, parameter binding, cursor encoding, and row scanning in isolation. |
| `github.com/testcontainers/testcontainers-go` | **Integration tests** — spins up a real PostgreSQL container via Docker at test time. Manages container lifecycle (start, wait-for-ready, stop). |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | **Postgres module** for testcontainers — provides Postgres-specific helpers (username, password, database name, readiness detection via log parsing). Used in `internal/handlers/resources_test.go`. |

**In short**: `pgxmock` = fast, no-Docker unit tests for the SQL layer. `testcontainers` = real-DB integration tests for the HTTP handler layer. Both are test-only dependencies (`require` block in `go.mod`).

## Running tests

All tests require Docker to be running (testcontainers starts a Postgres container automatically).

```bash
# Run all tests
go test ./... -cover

# Unit tests only (SQL layer, fast, uses pgxmock)
go test ./internal/sql/ -cover -v

# Integration tests only (handler layer, uses testcontainers)
go test ./internal/handlers/ -cover -v

# Registry tests (no Docker needed)
go test ./internal/registry/ -cover -v
```

## Local manual testing (curl)

To test the service manually with curl, you need a running PostgreSQL instance.

### 1. Start Postgres

```bash
docker run -d --name krateo-pg \
  -e POSTGRES_USER=krateo \
  -e POSTGRES_PASSWORD=krateo \
  -e POSTGRES_DB=krateo \
  -p 5432:5432 \
  postgres:18
```

### 2. Create the schema

```bash
docker exec -i krateo-pg psql -U krateo -d krateo <<'SQL'
CREATE TABLE IF NOT EXISTS krateo_resources (
    -- Timestamps for ingestion and updates
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Stable row id for deterministic keyset pagination
    id                BIGINT GENERATED ALWAYS AS IDENTITY,

    -- Cluster / object identity
    cluster_name      TEXT NOT NULL,
    uid               TEXT NOT NULL,
    global_uid        TEXT NOT NULL, -- cluster_name:uid

    namespace         TEXT NOT NULL,
    resource_kind     TEXT NOT NULL, -- include apiVersion, e.g. apps/v1:Deployment
    resource_name     TEXT NOT NULL,

    -- Optional domain identifier
    composition_id    UUID NULL,

    -- Full Kubernetes object
    raw               JSONB NOT NULL,

    PRIMARY KEY (id)
);

-- One current row per physical Kubernetes object.
CREATE UNIQUE INDEX IF NOT EXISTS uq_krateo_resources_global_uid
ON krateo_resources (global_uid);

-- Fast listing by API resource type with stable keyset pagination.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_kind_page
ON krateo_resources (resource_kind, updated_at DESC, id DESC);

-- Common direct lookups.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_obj
ON krateo_resources (cluster_name, namespace, resource_kind, resource_name);

-- Optional composition filter.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_composition
ON krateo_resources (composition_id, updated_at DESC, id DESC)
WHERE composition_id IS NOT NULL;

-- Generic metadata.* filtering (name/namespace/labels/annotations/...).
CREATE INDEX IF NOT EXISTS idx_krateo_resources_metadata
ON krateo_resources
USING GIN ((raw->'metadata'));

-- Generic raw filtering using containment/jsonpath predicates.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_raw
ON krateo_resources
USING GIN (raw jsonb_path_ops);
SQL
```

### 3. (Optional) Seed sample data

Generates 500 Panel resources across different clusters, namespaces, and dashboard themes:

```bash
docker exec -i krateo-pg psql -U krateo -d krateo <<'SQL'
DO $$
DECLARE
  clusters   TEXT[] := ARRAY['prod-eu', 'prod-us', 'staging', 'dev'];
  namespaces TEXT[] := ARRAY['krateo-system', 'demo-system', 'ns-1', 'ns-2'];
  titles     TEXT[] := ARRAY['Blueprints', 'Deployments', 'Costs', 'Pipelines', 'Alerts', 'Metrics', 'Logs', 'Clusters', 'Users', 'Quotas'];
  sections   TEXT[] := ARRAY['dashboard', 'overview', 'admin'];
  c TEXT; ns TEXT; title TEXT; sec TEXT;
  i INT := 1;
  uid_val TEXT;
  name_val TEXT;
  raw_val JSONB;
BEGIN
  FOR rep IN 1..7 LOOP                                  -- repeat to reach 500
    FOREACH c IN ARRAY clusters LOOP
      FOREACH ns IN ARRAY namespaces[1:2] LOOP          -- 2 namespaces per cluster
        FOR ti IN 1..array_length(titles, 1) LOOP
          IF i > 500 THEN RETURN; END IF;
        title  := titles[ti];
        sec    := sections[1 + (i % array_length(sections, 1))];
        name_val := sec || '-' || lower(replace(title, ' ', '-')) || '-panel-' || i;
        uid_val  := 'uid-' || lpad(i::TEXT, 4, '0');

        raw_val := jsonb_build_object(
          'apiVersion', 'widgets.templates.krateo.io/v1beta1',
          'kind',       'Panel',
          'metadata',   jsonb_build_object(
            'name',      name_val,
            'namespace', ns,
            'labels',    jsonb_build_object('app.kubernetes.io/part-of', sec),
            'annotations', jsonb_build_object('krateo.io/verbose', (i % 2 = 0)::TEXT)
          ),
          'spec', jsonb_build_object(
            'widgetData', jsonb_build_object(
              'title',   title,
              'actions', '{}'::jsonb,
              'items',   jsonb_build_array(
                jsonb_build_object('resourceRefId', name_val || '-row')
              )
            ),
            'resourcesRefs', jsonb_build_object(
              'items', jsonb_build_array(
                jsonb_build_object(
                  'id',         name_val || '-row',
                  'apiVersion', 'widgets.templates.krateo.io/v1beta1',
                  'name',       name_val || '-row',
                  'namespace',  ns,
                  'resource',   'rows',
                  'verb',       'GET'
                )
              )
            )
          )
        );

        INSERT INTO krateo_resources
          (updated_at, cluster_name, uid, global_uid, namespace, resource_kind, resource_name, raw)
        VALUES
          (now() - (i || ' minutes')::INTERVAL,
           c, uid_val, c || ':' || uid_val, ns,
           'widgets.templates.krateo.io/v1beta1:Panel', name_val, raw_val);

          i := i + 1;
        END LOOP;
      END LOOP;
    END LOOP;
  END LOOP;
END $$;
SQL
```

Verify:
```bash
docker exec -i krateo-pg psql -U krateo -d krateo -c \
  "SELECT cluster_name, namespace, resource_kind, resource_name FROM krateo_resources ORDER BY updated_at DESC;"
```

### 4. Run the service

```bash
DB_USER=krateo DB_PASS=krateo DB_HOST=localhost DB_NAME=krateo \
  DB_PARAMS="sslmode=disable" DEBUG=true \
  go run .
```

### 5. Test with curl

```bash
# List panels (short name)
curl -s http://localhost:8080/resources/panels | jq

# List panels with full raw objects
curl -s 'http://localhost:8080/resources/panels?raw=true' | jq
# TODO: myabe instead of raw=true, full=true or something more explicit (to be implemented)

# Filter by namespace
curl -s 'http://localhost:8080/resources/panels?namespace=krateo-system' | jq
# TODO: maybe not namespace but namespaces=ns1,ns2 (to be implemented)

# Unknown resource kind (returns 404)
curl -s http://localhost:8080/resources/unknown

# Health probes
curl -s http://localhost:8080/livez
curl -s http://localhost:8080/readyz
```

### 6. Cleanup

```bash
docker stop krateo-pg && docker rm krateo-pg
```
