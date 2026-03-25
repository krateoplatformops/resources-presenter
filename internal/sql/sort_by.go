package sql

import "fmt"

// SortBy specifies the column(s) used to order list results.
type SortBy string

const (
	// SortByResource orders by resource identity: group, version, resource, namespace, name (ASC).
	// This is the default sort order, analogous to how kubectl sorts resources.
	SortByResource SortBy = "resource"
	// SortByCreatedAt orders by creation timestamp (DESC), most recent first.
	SortByCreatedAt SortBy = "created_at"
	// SortByUpdatedAt orders by last update timestamp (DESC), most recent first.
	SortByUpdatedAt SortBy = "updated_at"
	// SortByGlobalUID orders by global_uid lexicographically (ASC).
	SortByGlobalUID SortBy = "global_uid"
	// SortByCompositionID orders by composition_id lexicographically (ASC), NULLs last.
	SortByCompositionID SortBy = "composition_id"
)

// DefaultSortBy is the sort order used when the client does not specify one.
const DefaultSortBy = SortByResource

// validSortValues enumerates all accepted sort_by values for error messages.
var validSortValues = []SortBy{
	SortByResource, SortByCreatedAt, SortByUpdatedAt, SortByGlobalUID, SortByCompositionID,
}

// SortOrder specifies ascending or descending direction for the sort.
type SortOrder string

const (
	SortOrderAsc  SortOrder = "asc"
	SortOrderDesc SortOrder = "desc"
)

// ValidateSortOrder returns an error if o is not a recognised sort order direction.
// An empty string is valid and means "use the default for the chosen sort_by column".
func ValidateSortOrder(o SortOrder) error {
	switch o {
	case SortOrderAsc, SortOrderDesc, "":
		return nil
	default:
		return fmt.Errorf("invalid sort_order value %q: must be one of: asc, desc", o)
	}
}

// defaultSortOrder returns the conventional default direction for each SortBy column.
// created_at and updated_at default to DESC (most recent first); all others default to ASC.
func defaultSortOrder(s SortBy) SortOrder {
	switch s {
	case SortByCreatedAt, SortByUpdatedAt:
		return SortOrderDesc
	default:
		return SortOrderAsc
	}
}

// normaliseSortOrder returns the effective SortOrder: if o is empty, it falls back
// to the per-column default for the given SortBy.
func normaliseSortOrder(o SortOrder, s SortBy) SortOrder {
	if o == "" {
		return defaultSortOrder(s)
	}
	return o
}

// ValidateSortBy returns an error if s is not a recognised sort order.
// An empty string is valid and means "use default".
func ValidateSortBy(s SortBy) error {
	switch s {
	case SortByResource, SortByCreatedAt, SortByUpdatedAt, SortByGlobalUID, SortByCompositionID, "":
		return nil
	default:
		return fmt.Errorf("invalid sort_by value %q: must be one of: resource, created_at, updated_at, global_uid, composition_id", s)
	}
}

// normaliseSortBy returns DefaultSortBy when s is empty, otherwise s unchanged.
func normaliseSortBy(s SortBy) SortBy {
	if s == "" {
		return DefaultSortBy
	}
	return s
}

// dirStr returns "ASC" or "DESC" for the given SortOrder.
func dirStr(o SortOrder) string {
	if o == SortOrderDesc {
		return "DESC"
	}
	return "ASC"
}

// nullsClause returns the NULLS FIRST/LAST suffix for nullable columns.
// ASC uses NULLS LAST (NULLs at the end), DESC uses NULLS FIRST (NULLs at the end in reverse).
func nullsClause(o SortOrder) string {
	if o == SortOrderDesc {
		return "NULLS FIRST"
	}
	return "NULLS LAST"
}

// sortOrderSQL returns the ORDER BY clause for the given sort column and direction.
func sortOrderSQL(s SortBy, o SortOrder) string {
	dir := dirStr(o)
	switch s {
	case SortByCreatedAt:
		return fmt.Sprintf("created_at %s, id %s", dir, dir)
	case SortByUpdatedAt:
		return fmt.Sprintf("updated_at %s, id %s", dir, dir)
	case SortByGlobalUID:
		return fmt.Sprintf("global_uid %s, id %s", dir, dir)
	case SortByCompositionID:
		return fmt.Sprintf("composition_id %s %s, id %s", dir, nullsClause(o), dir)
	case SortByResource:
		return fmt.Sprintf("resource_group %s, resource_version %s, resource_plural %s, namespace %s, resource_name %s, id %s",
			dir, dir, dir, dir, dir, dir)
	default:
		return fmt.Sprintf("resource_group %s, resource_version %s, resource_plural %s, namespace %s, resource_name %s, id %s",
			dir, dir, dir, dir, dir, dir)
	}
}
