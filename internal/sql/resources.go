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
	StatusRaw       bool // include status_raw JSONB column
	SortBy          SortBy
	Limit           int
	Cursor          EncodedCursor

	// AllowedTargets restricts the query to only these (group, resource, namespace) triples.
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
	Raw           json.RawMessage `json:"raw,omitempty"`        // Raw is included only when the raw=true query parameter is set.
	StatusRaw     json.RawMessage `json:"status_raw,omitempty"` // StatusRaw is included when status_raw=true (list) or by default (detail).
}

// ListResult is the full response for a list query.
type ListResult struct {
	Count  int            `json:"count"`
	Items  []ResourceItem `json:"items"`
	Cursor EncodedCursor  `json:"cursor,omitempty"`
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
	items := make([]ResourceItem, 0, initialCap)
	cursors := make([]rowCursor, 0, initialCap)

	for rows.Next() {
		// Stop scanning promptly when the request is cancelled (client disconnect, timeout).
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

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
			statusRawJSON   []byte
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
		if p.StatusRaw {
			scanDest = append(scanDest, &statusRawJSON)
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
		if p.StatusRaw && statusRawJSON != nil {
			item.StatusRaw = json.RawMessage(statusRawJSON)
		}

		items = append(items, item)
		cursors = append(cursors, rowCursor{
			id:            id,
			updatedAt:     updatedAt,
			createdAt:     createdAt,
			globalUID:     globalUID,
			compositionID: compositionID,
			group:         resourceGroup,
			version:       resourceVersion,
			resource:      resourcePlural,
			namespace:     namespace,
			name:          resourceName,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	sortBy := normaliseSortBy(p.SortBy)
	result := &ListResult{}

	// Pagination: trim to requested limit and compute next-page cursor.
	if p.Limit > 0 {
		hasNextPage := len(items) > p.Limit
		if hasNextPage {
			items = items[:p.Limit]
			cursors = cursors[:p.Limit]
		}

		if hasNextPage && len(cursors) > 0 {
			last := cursors[len(cursors)-1]
			result.Cursor = EncodeCursor(buildCursor(sortBy, last))
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

	// Restrict to RBAC-allowed (resource_group, resource_plural, namespace) triples.
	// AllowedTargets must be populated by the handler after discovery + RBAC filtering.
	// The group column is required to distinguish resources with the same plural name
	// but different API groups (e.g. github:repoes vs gitlab:repoes).
	if len(p.AllowedTargets) > 0 {
		parts := make([]string, len(p.AllowedTargets))
		args := make([]any, 0, len(p.AllowedTargets)*3)
		for i, t := range p.AllowedTargets {
			parts[i] = "(?, ?, ?)"
			args = append(args, t.Group, t.Resource, t.Namespace)
		}
		b.Where(fmt.Sprintf("(resource_group, resource_plural, namespace) IN (%s)", strings.Join(parts, ", ")), args...)
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

	// Resolve sort order (default to SortByResource when unset).
	sortBy := normaliseSortBy(p.SortBy)

	// Keyset pagination cursor
	cur, err := DecodeCursor(p.Cursor)
	if err != nil {
		return "", nil, err
	}
	if err := validateCursorSort(cur, sortBy); err != nil {
		return "", nil, err
	}
	if cur != nil {
		cursorWhere, cursorArgs := cursorCondition(sortBy, cur)
		b.Where(cursorWhere, cursorArgs...)
	}

	b.OrderBy(sortOrderSQL(sortBy))

	// Fetch limit+1 to detect whether a next page exists.
	if p.Limit > 0 {
		b.Limit(p.Limit + 1)
	}

	// Columns
	cols := "resource_name, uid, global_uid, namespace, resource_group, resource_version, resource_kind, resource_plural, cluster_name, created_at, updated_at, composition_id, id"
	if p.Raw {
		cols += ", raw"
	}
	if p.StatusRaw {
		cols += ", status_raw"
	}

	baseSQL := fmt.Sprintf("SELECT %s FROM krateo_resources", cols)
	query, args := b.Render(baseSQL)
	return query, args, nil
}

// GetByGlobalUID fetches a single resource by its global_uid.
// It returns a ListResult with count 0 or 1 for response format consistency.
// When includeRaw is true (the default for the detail endpoint), the full raw
// Kubernetes object is included in the response.
// When includeStatusRaw is true (the default), the status_raw JSONB column is
// included; callers can pass false to omit it.
func GetByGlobalUID(ctx context.Context, db Querier, globalUID string, includeRaw, includeStatusRaw bool) (*ListResult, error) {
	cols := "resource_name, uid, global_uid, namespace, resource_group, resource_version, resource_kind, resource_plural, cluster_name, created_at, updated_at, composition_id, id"
	if includeRaw {
		cols += ", raw"
	}
	if includeStatusRaw {
		cols += ", status_raw"
	}

	query := fmt.Sprintf("SELECT %s FROM krateo_resources WHERE global_uid = $1 AND deleted_at IS NULL LIMIT 2", cols)

	rows, err := db.Query(ctx, query, globalUID)
	if err != nil {
		return nil, fmt.Errorf("get_by_global_uid query: %w", err)
	}
	defer rows.Close()

	result := &ListResult{Items: []ResourceItem{}}

	for rows.Next() {
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
			statusRawJSON   []byte
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
		if includeStatusRaw {
			scanDest = append(scanDest, &statusRawJSON)
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
		if includeStatusRaw && statusRawJSON != nil {
			item.StatusRaw = json.RawMessage(statusRawJSON)
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
