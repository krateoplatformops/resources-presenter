package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krateoplatformops/resources-proxy/internal/access"
)

// Querier abstracts the pgx query interface so it can be mocked in tests.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ListParams contains the parsed and validated query parameters for listing resources.
type ListParams struct {
	ResourceKind  string
	Cluster       string
	Namespace     string
	CompositionID string // UUID as string; empty means no filter
	Raw           bool
	Limit         int
	Cursor        EncodedCursor
}

// ResourceItem is the minimal response DTO for a single resource.
type ResourceItem struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
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
func ListResources(ctx context.Context, db Querier, p ListParams, policy *access.Policy) (*ListResult, error) {
	query, args, err := buildListQuery(p, policy)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var items []ResourceItem
	var cursors []rowCursor

	for rows.Next() {
		var (
			resourceName string
			namespace    string
			resourceKind string
			id           int64
			updatedAt    time.Time
			rawJSON      []byte
		)

		if p.Raw {
			if err := rows.Scan(&resourceName, &namespace, &resourceKind, &id, &updatedAt, &rawJSON); err != nil {
				return nil, fmt.Errorf("scan: %w", err)
			}
		} else {
			if err := rows.Scan(&resourceName, &namespace, &resourceKind, &id, &updatedAt); err != nil {
				return nil, fmt.Errorf("scan: %w", err)
			}
		}

		apiVersion, kind := splitResourceKind(resourceKind)

		item := ResourceItem{
			Name:       resourceName,
			Namespace:  namespace,
			APIVersion: apiVersion,
			Kind:       kind,
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

	result.Count = len(items)
	result.Items = items
	return result, nil
}

// buildListQuery constructs the SQL and args for listing resources
// using the Builder from this package.
func buildListQuery(p ListParams, policy *access.Policy) (string, []any, error) {
	b := NewBuilder()

	// resource_kind is required
	b.Where("resource_kind = ?", p.ResourceKind)

	if p.Cluster != "" {
		b.Where("cluster_name = ?", p.Cluster)
	}

	if p.Namespace != "" {
		b.Where("namespace = ?", p.Namespace)
	}

	if p.CompositionID != "" {
		b.Where("composition_id = ?", p.CompositionID)
	}

	// TODO(auth): when policy is non-nil, add access control filters here.
	// NOTE: The ANY(?) clauses below rely on pgx automatically encoding []string
	// as a PostgreSQL array parameter. If the database driver is ever changed, this
	// implicit conversion may break. The positional parameter ($N) receives a Go
	// slice which pgx serializes as '{val1,val2,...}'.
	if policy != nil {
		if len(policy.AllowedNamespaces) > 0 {
			b.Where("namespace = ANY(?)", policy.AllowedNamespaces)
		}
		if len(policy.AllowedClusters) > 0 {
			b.Where("cluster_name = ANY(?)", policy.AllowedClusters)
		}
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
	cols := "resource_name, namespace, resource_kind, id, updated_at"
	if p.Raw {
		cols += ", raw"
	}

	baseSQL := fmt.Sprintf("SELECT %s FROM krateo_resources", cols)
	query, args := b.Render(baseSQL)
	return query, args, nil
}

// splitResourceKind splits "apiVersion:Kind" into its two parts.
// For example, "apps/v1:Deployment" -> ("apps/v1", "Deployment").
func splitResourceKind(rk string) (apiVersion, kind string) {
	idx := strings.LastIndex(rk, ":")
	if idx < 0 {
		return "", rk
	}
	return rk[:idx], rk[idx+1:]
}
