package sql

import (
	"fmt"
	"strings"
)

// Builder is a minimal SQL query builder for PostgreSQL.
//
// It supports dynamic WHERE clauses, ORDER BY and LIMIT,
// using '?' as logical placeholders that are rendered as
// positional PostgreSQL parameters ($1, $2, ...).
//
// All WHERE conditions are combined using AND, in the order
// they are added.
//
// Copied from events-presenter/internal/sql/builder.go and adapted
// for resources-proxy.
type Builder struct {
	where   []string
	args    []any
	orderBy string
	limit   *int
}

func NewBuilder() *Builder {
	return &Builder{}
}

// Where adds a WHERE condition to the query.
//
// The condition must use '?' as placeholder(s) for arguments,
// which will be replaced with PostgreSQL positional parameters
// ($n) when rendering the final SQL.
//
// Example:
//
//	b.Where("cluster_name = ?", "cluster-a")
func (b *Builder) Where(cond string, args ...any) {
	b.where = append(b.where, cond)
	b.args = append(b.args, args...)
}

// OrderBy sets the ORDER BY clause of the query.
// The provided string is appended verbatim to the SQL.
func (b *Builder) OrderBy(order string) {
	b.orderBy = order
}

// Limit sets a LIMIT clause on the query.
// If n is less than or equal to zero, it is ignored.
func (b *Builder) Limit(n int) {
	if n > 0 {
		b.limit = &n
	}
}

// Render builds the final SQL query by appending WHERE, ORDER BY
// and LIMIT clauses to baseSQL.
//
// It returns the rendered SQL string and the ordered list of
// arguments to be passed to the database driver.
func (b *Builder) Render(baseSQL string) (string, []any) {
	var sb strings.Builder
	sb.WriteString(baseSQL)

	argIdx := 1

	if len(b.where) > 0 {
		sb.WriteString("\nWHERE ")
		for i, cond := range b.where {
			if i > 0 {
				sb.WriteString("\n  AND ")
			}

			var out strings.Builder
			for _, ch := range cond {
				if ch == '?' {
					out.WriteString(fmt.Sprintf("$%d", argIdx))
					argIdx++
				} else {
					out.WriteRune(ch)
				}
			}
			sb.WriteString(out.String())
		}
	}

	if b.orderBy != "" {
		sb.WriteString("\nORDER BY ")
		sb.WriteString(b.orderBy)
	}

	if b.limit != nil {
		sb.WriteString(fmt.Sprintf("\nLIMIT $%d", argIdx))
		b.args = append(b.args, *b.limit)
	}

	return sb.String(), b.args
}
