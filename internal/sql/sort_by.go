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

// sortOrderSQL returns the ORDER BY clause for the given sort order.
func sortOrderSQL(s SortBy) string {
	switch s {
	case SortByCreatedAt:
		return "created_at DESC, id DESC"
	case SortByUpdatedAt:
		return "updated_at DESC, id DESC"
	case SortByGlobalUID:
		return "global_uid ASC, id ASC"
	case SortByCompositionID:
		return "composition_id ASC NULLS LAST, id ASC"
	case SortByResource:
		return "resource_group ASC, resource_version ASC, resource_plural ASC, namespace ASC, resource_name ASC, id ASC"
	default:
		return "resource_group ASC, resource_version ASC, resource_plural ASC, namespace ASC, resource_name ASC, id ASC"
	}
}
