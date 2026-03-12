-- ============================================================
-- Table: krateo_resources (current state of Kubernetes resources)
-- ============================================================

CREATE TABLE IF NOT EXISTS krateo_resources (
    -- Timestamps for ingestion and updates
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ NULL,

    -- Stable row id for deterministic keyset pagination
    id                BIGINT GENERATED ALWAYS AS IDENTITY,

    -- Cluster / object identity
    cluster_name      TEXT NOT NULL,
    uid               TEXT NOT NULL,
    global_uid        TEXT NOT NULL UNIQUE, -- cluster_name:uid

    namespace         TEXT NOT NULL,

    -- Kubernetes API identifiers (GVR decomposition)
    resource_group    TEXT NOT NULL,       -- e.g. apps (empty for core)
    resource_version  TEXT NOT NULL,       -- e.g. v1
    resource_kind     TEXT NOT NULL,       -- e.g. Deployment
    resource_plural   TEXT NOT NULL,
    resource_name     TEXT NOT NULL,

    -- Optional domain identifier
    composition_id    UUID NULL,

    -- Full Kubernetes object
    raw               JSONB NOT NULL,

    PRIMARY KEY (id)
);

-- Fast keyset pagination by GVR (group + version + kind).
CREATE INDEX IF NOT EXISTS idx_krateo_resources_gvr_page
ON krateo_resources (resource_group, resource_version, resource_kind, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

-- Fast direct lookup for a full Kubernetes object.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_obj
ON krateo_resources (cluster_name, namespace, resource_group, resource_version, resource_kind, resource_name)
WHERE deleted_at IS NULL;

-- Fast filters and GROUP BY by resource_plural.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural
ON krateo_resources (resource_plural)
WHERE deleted_at IS NULL;

-- Keyset pagination when listing by resource_plural.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_plural_page
ON krateo_resources (resource_plural, updated_at DESC, id DESC)
WHERE deleted_at IS NULL;

-- Fast lookup by GVR inside cluster and namespace.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_cluster_ns_gvr_plural
ON krateo_resources (cluster_name, namespace, resource_group, resource_version, resource_plural)
WHERE deleted_at IS NULL;

-- Fast lookup by resource_plural inside cluster and namespace.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_cluster_ns_plural
ON krateo_resources (cluster_name, namespace, resource_plural)
WHERE deleted_at IS NULL;

-- Optional composition-based listing for active rows.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_composition
ON krateo_resources (composition_id, updated_at DESC, id DESC)
WHERE composition_id IS NOT NULL;

-- Phase 2: label search on metadata.labels only (avoids a full raw JSONB index).
CREATE INDEX IF NOT EXISTS idx_krateo_resources_labels
ON krateo_resources
USING GIN ((raw->'metadata'->'labels'))
WHERE deleted_at IS NULL;

-- Cleanup support index for hard-delete jobs on soft-deleted rows.
CREATE INDEX IF NOT EXISTS idx_krateo_resources_deleted_at
ON krateo_resources (deleted_at)
WHERE deleted_at IS NOT NULL;
