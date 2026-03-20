package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	sharedPool      *pgxpool.Pool
	sharedContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: postgres container unavailable, integration tests will be skipped: %v\n", err)
		os.Exit(m.Run())
	}

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: container connection string: %v\n", err)
		container.Terminate(ctx)
		os.Exit(1)
	}
	connStr += "&sslmode=disable"

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: pgxpool.New: %v\n", err)
		container.Terminate(ctx)
		os.Exit(1)
	}

	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: ping: %v\n", err)
		pool.Close()
		container.Terminate(ctx)
		os.Exit(1)
	}

	// Apply schema once for all tests.
	schema := `
CREATE TABLE IF NOT EXISTS krateo_resources (
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ NULL,
    id                BIGINT GENERATED ALWAYS AS IDENTITY,
    cluster_name      TEXT NOT NULL,
    uid               TEXT NOT NULL,
    global_uid        TEXT NOT NULL,
    namespace         TEXT NOT NULL,
    resource_group    TEXT NOT NULL,
    resource_version  TEXT NOT NULL,
    resource_kind     TEXT NOT NULL,
    resource_plural   TEXT NOT NULL,
    resource_name     TEXT NOT NULL,
    composition_id    UUID NULL,
    raw               JSONB NOT NULL,
    status_raw        JSONB NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_krateo_resources_global_uid
ON krateo_resources (global_uid)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_gvr_page
ON krateo_resources (resource_group, resource_version, resource_kind, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_obj
ON krateo_resources (cluster_name, namespace, resource_group, resource_version, resource_kind, resource_name)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural
ON krateo_resources (resource_plural)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural_page
ON krateo_resources (resource_plural, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_composition
ON krateo_resources (composition_id, updated_at DESC, id DESC)
WHERE composition_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_krateo_resources_labels
ON krateo_resources
USING GIN ((raw->'metadata'->'labels'))
WHERE deleted_at IS NULL;
`
	if _, err := pool.Exec(ctx, schema); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: apply schema: %v\n", err)
		pool.Close()
		container.Terminate(ctx)
		os.Exit(1)
	}

	sharedPool = pool
	sharedContainer = container

	code := m.Run()

	pool.Close()
	container.Terminate(ctx)
	os.Exit(code)
}

// testDB returns the shared pool after truncating krateo_resources.
// Each test gets a clean table without spinning up a new container.
// Not safe for t.Parallel() — use separate schemas if parallelism is needed.
func testDB(tb testing.TB) *pgxpool.Pool {
	tb.Helper()

	if sharedPool == nil {
		tb.Skip("shared postgres container not available")
	}

	_, err := sharedPool.Exec(context.Background(), "TRUNCATE krateo_resources RESTART IDENTITY")
	if err != nil {
		tb.Fatalf("truncate krateo_resources: %v", err)
	}

	return sharedPool
}
