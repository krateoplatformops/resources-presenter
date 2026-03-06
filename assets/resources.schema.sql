-- ============================================================
-- Table: krateo_resources (current state of Kubernetes resources)
-- ============================================================

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
