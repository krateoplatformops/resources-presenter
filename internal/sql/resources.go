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

// ResourceTarget represents a unique (group, resource, namespace) combination
// discovered from the database. Used for RBAC target enumeration and for
// restricting query results to allowed resources.
type ResourceTarget struct {
	Group     string // resource_group column in krateo_resources table of the database
	Resource  string // resource_plural column in krateo_resources table of the database
	Namespace string
}

// ListParams contains the parsed and validated query parameters for listing resources.
type ListParams struct {
	ResourceGroup   string // e.g. "apps", "" for core
	ResourceVersion string // e.g. "v1"
	ResourcePlural  string // e.g. "deployments"
	Cluster         string
	Namespace       string
	CompositionID   string // UUID as string; empty means no filter
	Name            string // exact match on resource_name
	NameContains    string // case-insensitive partial match (ILIKE %name%)
	Labels          string // raw JSON string for JSONB containment (@>)
	Since           *time.Time
	Raw             bool
	Limit           int
	Cursor          EncodedCursor

	// AllowedTargets restricts the query to only these (resource, namespace) pairs.
	// When set, it replaces the individual ResourcePlural and Namespace filters.
	// Populated by the handler after discovery and RBAC filtering.
	AllowedTargets []ResourceTarget
}

// ResourceItem is the response DTO for a single resource.
type ResourceItem struct {
	Name          string          `json:"name"`
	Uid           string          `json:"uid"`
	GlobalUID     string          `json:"global_uid"`
	Namespace     string          `json:"namespace"`
	Group         string          `json:"group"`
	Version       string          `json:"version"`
	Kind          string          `json:"kind"`
	Resource      string          `json:"resource"`
	ClusterName   string          `json:"cluster_name"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	CompositionID *string         `json:"composition_id,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"` // Raw is included only when the raw=true query parameter is set.
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

// DiscoverTargets queries krateo_resources for distinct (group, resource, namespace)
// tuples matching the provided filters. The result is used to enumerate RBAC targets
// before the main list query.
//
// Filters applied:
// - ResourceGroup (required)
// - ResourceVersion, ResourcePlural, Namespace, Cluster (all optional, to narrow down the result set)
// Only active rows are considered.

func DiscoverTargets(ctx context.Context, db Querier, p ListParams) ([]ResourceTarget, error) {
	b := NewBuilder()

	b.Where("deleted_at IS NULL")
	b.Where("resource_group = ?", p.ResourceGroup)
	if p.ResourceVersion != "" {
		b.Where("resource_version = ?", p.ResourceVersion)
	}
	if p.ResourcePlural != "" {
		b.Where("resource_plural = ?", p.ResourcePlural)
	}
	if p.Cluster != "" {
		b.Where("cluster_name = ?", p.Cluster)
	}
	if p.Namespace != "" {
		b.Where("namespace = ?", p.Namespace)
	}

	baseSQL := "SELECT DISTINCT resource_group, resource_plural, namespace FROM krateo_resources"
	query, args := b.Render(baseSQL)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	defer rows.Close()

	var targets []ResourceTarget
	for rows.Next() {
		var t ResourceTarget
		if err := rows.Scan(&t.Group, &t.Resource, &t.Namespace); err != nil {
			return nil, fmt.Errorf("discover scan: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("discover rows: %w", err)
	}

	return targets, nil
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
	initialCap := p.Limit + 1
	if p.Limit <= 0 {
		initialCap = 256 // reasonable default for unlimited queries
	}
	items := make([]ResourceItem, 0, initialCap)
	cursors := make([]rowCursor, 0, initialCap)

	for rows.Next() {
		var (
			resourceName    string
			uid             string
			globalUID       string
			namespace       string
			resourceGroup   string
			resourceVersion string
			resourceKind    string
			resourcePlural  string
			clusterName     string
			createdAt       time.Time
			updatedAt       time.Time
			compositionID   *string
			id              int64
			rawJSON         []byte
		)

		scanDest := []any{
			&resourceName, &uid, &globalUID, &namespace,
			&resourceGroup, &resourceVersion, &resourceKind, &resourcePlural,
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
			Uid:           uid,
			GlobalUID:     globalUID,
			Namespace:     namespace,
			Group:         resourceGroup,
			Version:       resourceVersion,
			Kind:          resourceKind,
			Resource:      resourcePlural,
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

	// Pagination: only applies when a finite limit is set.
	// When Limit == -1 (unlimited), all rows are returned with no cursor.
	if p.Limit > 0 {
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

	// Soft-delete filter: only return active rows.
	b.Where("deleted_at IS NULL")

	// Group is always required.
	b.Where("resource_group = ?", p.ResourceGroup)

	// Version is optional; when set it narrows the result set.
	if p.ResourceVersion != "" {
		b.Where("resource_version = ?", p.ResourceVersion)
	}

	// Restrict to RBAC-allowed (resource_plural, namespace) pairs.
	// AllowedTargets must be populated by the handler after discovery + RBAC filtering.
	if len(p.AllowedTargets) > 0 {
		parts := make([]string, len(p.AllowedTargets))
		args := make([]any, 0, len(p.AllowedTargets)*2)
		for i, t := range p.AllowedTargets {
			parts[i] = "(?, ?)"
			args = append(args, t.Resource, t.Namespace)
		}
		b.Where(fmt.Sprintf("(resource_plural, namespace) IN (%s)", strings.Join(parts, ", ")), args...)
	}

	if p.Cluster != "" {
		b.Where("cluster_name = ?", p.Cluster)
	}

	if p.CompositionID != "" {
		b.Where("composition_id = ?", p.CompositionID)
	}

	if p.Name != "" {
		b.Where("resource_name = ?", p.Name)
	}

	if p.NameContains != "" {
		b.Where("resource_name ILIKE ?", "%"+escapeLIKE(p.NameContains)+"%")
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

	// Fetch limit+1 to detect next page; skip when unlimited (Limit == -1).
	if p.Limit > 0 {
		b.Limit(p.Limit + 1)
	}

	// Columns
	cols := "resource_name, uid, global_uid, namespace, resource_group, resource_version, resource_kind, resource_plural, cluster_name, created_at, updated_at, composition_id, id"
	if p.Raw {
		cols += ", raw"
	}

	baseSQL := fmt.Sprintf("SELECT %s FROM krateo_resources", cols)
	query, args := b.Render(baseSQL)
	return query, args, nil
}

// GetByGlobalUID fetches a single resource by its global_uid.
// It returns a ListResult with count 0 or 1 for response format consistency.
// When includeRaw is true (the default for the detail endpoint), the full raw
// Kubernetes object is included in the response.
func GetByGlobalUID(ctx context.Context, db Querier, globalUID string, includeRaw bool) (*ListResult, error) {
	cols := "resource_name, uid, global_uid, namespace, resource_group, resource_version, resource_kind, resource_plural, cluster_name, created_at, updated_at, composition_id, id"
	if includeRaw {
		cols += ", raw"
	}

	query := fmt.Sprintf("SELECT %s FROM krateo_resources WHERE global_uid = $1 AND deleted_at IS NULL", cols)

	rows, err := db.Query(ctx, query, globalUID)
	if err != nil {
		return nil, fmt.Errorf("get_by_global_uid query: %w", err)
	}
	defer rows.Close()

	result := &ListResult{Items: []ResourceItem{}}

	if rows.Next() {
		var (
			resourceName    string
			uid             string
			gUID            string
			namespace       string
			resourceGroup   string
			resourceVersion string
			resourceKind    string
			resourcePlural  string
			clusterName     string
			createdAt       time.Time
			updatedAt       time.Time
			compositionID   *string
			id              int64
			rawJSON         []byte
		)

		scanDest := []any{
			&resourceName, &uid, &gUID, &namespace,
			&resourceGroup, &resourceVersion, &resourceKind, &resourcePlural,
			&clusterName, &createdAt, &updatedAt, &compositionID,
			&id,
		}
		if includeRaw {
			scanDest = append(scanDest, &rawJSON)
		}

		if err := rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("get_by_global_uid scan: %w", err)
		}

		item := ResourceItem{
			Name:          resourceName,
			Uid:           uid,
			GlobalUID:     gUID,
			Namespace:     namespace,
			Group:         resourceGroup,
			Version:       resourceVersion,
			Kind:          resourceKind,
			Resource:      resourcePlural,
			ClusterName:   clusterName,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
			CompositionID: compositionID,
		}
		if includeRaw && rawJSON != nil {
			item.Raw = json.RawMessage(rawJSON)
		}

		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get_by_global_uid rows: %w", err)
	}

	result.Count = len(result.Items)
	return result, nil
}

// escapeLIKE escapes PostgreSQL LIKE/ILIKE special characters (%, _, \).
func escapeLIKE(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
