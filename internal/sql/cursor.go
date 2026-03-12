package sql

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// EncodedCursor is an opaque base64-encoded pagination token.
type EncodedCursor string

// ResourcesCursor holds the internal keyset pagination state.
type ResourcesCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        int64     `json:"id"`
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
