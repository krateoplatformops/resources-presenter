package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier abstracts the pgx query interface so it can be mocked in tests.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ListParams contains the parsed and validated query parameters for listing resources.
type ListParams struct {
	ResourceGroup   string // e.g. "apps", "" for core
	ResourceVersion string // e.g. "v1"
	ResourcePlural  string // e.g. "deployments"
	Cluster         string
	Namespace       string
	CompositionID   string // UUID as string; empty means no filter
	Name            string // case-insensitive partial match (ILIKE %name%)
	Labels          string // raw JSON string for JSONB containment (@>)
	Since           *time.Time
	Raw             bool
	Limit           int
	Cursor          EncodedCursor
}

// ResourceItem is the response DTO for a single resource.
type ResourceItem struct {
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`
	Group         string    `json:"group"`
	Version       string    `json:"version"`
	Kind          string    `json:"kind"`
	ClusterName   string    `json:"cluster_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	CompositionID *string   `json:"composition_id,omitempty"`
	// Raw is included only when the raw=true query parameter is set.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// ListResult is the full response for a list query.
type ListResult struct {
	Count  int            `json:"count"`
	Items  []ResourceItem `json:"items"`
	Cursor EncodedCursor  `json:"cursor,omitempty"`
}

// rowCursor holds the pagination-relevant fields for each scanned row.
type rowCursor struct {
	updatedAt time.Time
	id        int64
}

// ListResources queries krateo_resources with the given filters, keyset pagination,
// and access policy. It fetches limit+1 rows to determine if a next page exists.
func ListResources(ctx context.Context, db Querier, p ListParams) (*ListResult, error) {
	query, args, err := buildListQuery(p)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	// Pre-allocate to avoid repeated growing for large result sets.
	items := make([]ResourceItem, 0, p.Limit+1)
	cursors := make([]rowCursor, 0, p.Limit+1)

	for rows.Next() {
		var (
			resourceName    string
			namespace       string
			resourceGroup   string
			resourceVersion string
			resourceKind    string
			clusterName     string
			createdAt       time.Time
			updatedAt       time.Time
			compositionID   *string
			id              int64
			rawJSON         []byte
		)

		scanDest := []any{
			&resourceName, &namespace,
			&resourceGroup, &resourceVersion, &resourceKind,
			&clusterName, &createdAt, &updatedAt, &compositionID,
			&id,
		}
		if p.Raw {
			scanDest = append(scanDest, &rawJSON)
		}

		if err := rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		item := ResourceItem{
			Name:          resourceName,
			Namespace:     namespace,
			Group:         resourceGroup,
			Version:       resourceVersion,
			Kind:          resourceKind,
			ClusterName:   clusterName,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
			CompositionID: compositionID,
		}
		if p.Raw && rawJSON != nil {
			item.Raw = json.RawMessage(rawJSON)
		}

		items = append(items, item)
		cursors = append(cursors, rowCursor{updatedAt: updatedAt, id: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	result := &ListResult{}

	// We fetched limit+1 rows. If we got more than limit, there's a next page.
	hasNextPage := len(items) > p.Limit
	if hasNextPage {
		items = items[:p.Limit]
		cursors = cursors[:p.Limit]
	}

	if hasNextPage && len(cursors) > 0 {
		last := cursors[len(cursors)-1]
		result.Cursor = EncodeCursor(&ResourcesCursor{
			UpdatedAt: last.updatedAt,
			ID:        last.id,
		})
	}

	if items == nil {
		items = []ResourceItem{}
	}
	result.Count = len(items)
	result.Items = items
	return result, nil
}

// buildListQuery constructs the SQL and args for listing resources
// using the Builder from this package.
func buildListQuery(p ListParams) (string, []any, error) {
	b := NewBuilder()

	// GVR decomposition: group, version, plural are required filters.
	b.Where("resource_group = ?", p.ResourceGroup)
	b.Where("resource_version = ?", p.ResourceVersion)
	b.Where("resource_plural = ?", p.ResourcePlural)

	// Soft-delete filter: only return active rows.
	b.WhereRaw("deleted_at IS NULL")

	if p.Cluster != "" {
		b.Where("cluster_name = ?", p.Cluster)
	}

	if p.Namespace != "" {
		b.Where("namespace = ?", p.Namespace)
	}

	if p.CompositionID != "" {
		b.Where("composition_id = ?", p.CompositionID)
	}

	if p.Name != "" {
		b.Where("resource_name ILIKE ?", "%"+escapeLIKE(p.Name)+"%")
	}

	if p.Labels != "" {
		b.Where("raw->'metadata'->'labels' @> ?::jsonb", p.Labels)
	}

	if p.Since != nil {
		b.Where("updated_at >= ?", *p.Since)
	}

	// Keyset pagination cursor
	cur, err := DecodeCursor(p.Cursor)
	if err != nil {
		return "", nil, err
	}
	if cur != nil {
		b.Where("(updated_at, id) < (?, ?)", cur.UpdatedAt, cur.ID)
	}

	b.OrderBy("updated_at DESC, id DESC")

	// Fetch limit+1 to detect next page
	b.Limit(p.Limit + 1)

	// Columns
	cols := "resource_name, namespace, resource_group, resource_version, resource_kind, cluster_name, created_at, updated_at, composition_id, id"
	if p.Raw {
		cols += ", raw"
	}

	baseSQL := fmt.Sprintf("SELECT %s FROM krateo_resources", cols)
	query, args := b.Render(baseSQL)
	return query, args, nil
}

// escapeLIKE escapes PostgreSQL LIKE/ILIKE special characters (%, _, \).
func escapeLIKE(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
