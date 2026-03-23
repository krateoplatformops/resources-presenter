package sql

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// EncodedCursor is an opaque base64-encoded pagination token.
type EncodedCursor string

// ResourcesCursor holds the internal keyset pagination state.
// Each sort order populates only the fields it needs; the SortBy field
// records which order produced the cursor so that we can reject mismatches.
type ResourcesCursor struct {
	SortBy SortBy `json:"s"`

	// Tiebreaker: always present for every sort order.
	ID int64 `json:"i"`

	// SortByUpdatedAt / SortByCreatedAt
	UpdatedAt *time.Time `json:"u,omitempty"`
	CreatedAt *time.Time `json:"c,omitempty"`

	// SortByGlobalUID
	GlobalUID string `json:"g,omitempty"`

	// SortByCompositionID — two fields are needed because composition_id is
	// a nullable column. After JSON round-tripping, a nil *string is ambiguous:
	// it could mean "the DB value was NULL" or "this field wasn't populated
	// because the cursor uses a different sort order". CompositionIDNull
	// resolves that ambiguity.
	//
	// Encoding (see buildCursor in resources.go):
	//   DB row has composition_id = 'abc' → CompositionID=&"abc", CompositionIDNull=false
	//   DB row has composition_id = NULL  → CompositionID=nil,    CompositionIDNull=true
	//   Cursor uses a different sort      → CompositionID=nil,    CompositionIDNull=false
	//
	// Decoding (see compositionIDCursorCondition in resources.go):
	//   CompositionIDNull=false, CompositionID!=nil → row had a value; generate:
	//       ((composition_id > ? OR (composition_id = ? AND id > ?))
	//        OR composition_id IS NULL)
	//   CompositionIDNull=true → row had NULL; we're in the trailing NULL zone:
	//       composition_id IS NULL AND id > ?
	CompositionID     *string `json:"p,omitempty"`
	CompositionIDNull bool    `json:"pn,omitempty"`

	// SortByResource (group/version/resource/namespace/name)
	Group     string `json:"rg,omitempty"`
	Version   string `json:"rv,omitempty"`
	Resource  string `json:"rr,omitempty"`
	Namespace string `json:"rn,omitempty"`
	Name      string `json:"rm,omitempty"`
}

// rowCursor holds the pagination-relevant fields for each scanned row.
// All fields are populated during scanning; only the ones relevant to the
// current SortBy are used when building the next-page cursor.
type rowCursor struct {
	id            int64
	updatedAt     time.Time
	createdAt     time.Time
	globalUID     string
	compositionID *string
	group         string
	version       string
	resource      string
	namespace     string
	name          string
}

// EncodeCursor encodes a ResourcesCursor to a base64 string.
func EncodeCursor(c *ResourcesCursor) EncodedCursor {
	if c == nil {
		return ""
	}
	data, _ := json.Marshal(c)
	return EncodedCursor(base64.RawURLEncoding.EncodeToString(data))
}

// DecodeCursor decodes a base64 string into ResourcesCursor.
func DecodeCursor(s EncodedCursor) (*ResourcesCursor, error) {
	if s == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(string(s))
	if err != nil {
		return nil, err
	}
	var c ResourcesCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ValidateCursor checks that the cursor is a valid encoded cursor.
// Returns nil if the cursor is empty or valid, an error otherwise.
// Use this at the HTTP boundary to return 400 for bad cursors instead of 500.
func ValidateCursor(s EncodedCursor) error {
	if s == "" {
		return nil
	}
	_, err := DecodeCursor(s)
	return err
}

// validateCursorSort checks that the cursor's sort order matches the request's sort order.
// Returns a user-facing error when they differ.
func validateCursorSort(cur *ResourcesCursor, sortBy SortBy) error {
	if cur == nil {
		return nil
	}
	if cur.SortBy != sortBy {
		return fmt.Errorf("cursor was created with sort_by=%q but request uses sort_by=%q; cursors are not reusable across sort orders", cur.SortBy, sortBy)
	}
	return nil
}

// buildCursor constructs a ResourcesCursor from the last row of the current page.
func buildCursor(sortBy SortBy, rc rowCursor) *ResourcesCursor {
	c := &ResourcesCursor{
		SortBy: sortBy,
		ID:     rc.id,
	}
	switch sortBy {
	case SortByCreatedAt:
		c.CreatedAt = &rc.createdAt
	case SortByUpdatedAt:
		c.UpdatedAt = &rc.updatedAt
	case SortByGlobalUID:
		c.GlobalUID = rc.globalUID
	case SortByCompositionID:
		if rc.compositionID == nil {
			c.CompositionIDNull = true
		} else {
			c.CompositionID = rc.compositionID
		}
	case SortByResource:
		c.Group = rc.group
		c.Version = rc.version
		c.Resource = rc.resource
		c.Namespace = rc.namespace
		c.Name = rc.name
	}
	return c
}

// cursorCondition returns a WHERE clause fragment and args for keyset pagination
// based on the current sort order.
//
// DESC sorts use (col, id) < (?, ?) to page forward (toward older rows).
// ASC sorts use (col, id) > (?, ?) to page forward (toward later rows).
//
// For composition_id (nullable), we handle NULLs explicitly so rows with NULL
// sort after all non-NULL values (NULLS LAST).
func cursorCondition(s SortBy, cur *ResourcesCursor) (string, []any) {
	switch s {
	case SortByCreatedAt:
		return "(created_at, id) < (?, ?)", []any{*cur.CreatedAt, cur.ID}
	case SortByUpdatedAt:
		return "(updated_at, id) < (?, ?)", []any{*cur.UpdatedAt, cur.ID}
	case SortByGlobalUID:
		return "(global_uid, id) > (?, ?)", []any{cur.GlobalUID, cur.ID}
	case SortByCompositionID:
		return compositionIDCursorCondition(cur)
	case SortByResource:
		return "(resource_group, resource_version, resource_plural, namespace, resource_name, id) > (?, ?, ?, ?, ?, ?)",
			[]any{cur.Group, cur.Version, cur.Resource, cur.Namespace, cur.Name, cur.ID}
	default:
		return "(resource_group, resource_version, resource_plural, namespace, resource_name, id) > (?, ?, ?, ?, ?, ?)",
			[]any{cur.Group, cur.Version, cur.Resource, cur.Namespace, cur.Name, cur.ID}
	}
}

// compositionIDCursorCondition builds the keyset condition for composition_id
// sorting (ASC NULLS LAST). There are two cases:
//
//  1. Cursor has a non-NULL composition_id: the next page contains rows with
//     a strictly greater composition_id, OR the same composition_id with a greater id,
//     OR rows with NULL composition_id (which sort last).
//
//  2. Cursor has a NULL composition_id: the remaining rows all have NULL
//     composition_id, so we only need the id tiebreaker.
func compositionIDCursorCondition(cur *ResourcesCursor) (string, []any) {
	if cur.CompositionIDNull {
		// Cursor row had NULL → only NULL rows remain; page by id.
		return "composition_id IS NULL AND id > ?", []any{cur.ID}
	}
	// Cursor row had a value → skip past it, also include all NULLs.
	return "((composition_id > ? OR (composition_id = ? AND id > ?)) OR composition_id IS NULL)",
		[]any{*cur.CompositionID, *cur.CompositionID, cur.ID}
}
