// Package rows implements browsing and editing the contents of user tables.
package rows

import (
	"fmt"
	"strings"

	"github.com/litebase/litebase/internal/database"
)

// Operator is a comparison available in the row browser and the generated API.
type Operator string

const (
	OpEq         Operator = "eq"
	OpNeq        Operator = "neq"
	OpGt         Operator = "gt"
	OpGte        Operator = "gte"
	OpLt         Operator = "lt"
	OpLte        Operator = "lte"
	OpLike       Operator = "like"
	OpNotLike    Operator = "not_like"
	OpContains   Operator = "contains"
	OpStartsWith Operator = "starts_with"
	OpEndsWith   Operator = "ends_with"
	OpIn         Operator = "in"
	OpNotIn      Operator = "not_in"
	OpIsNull     Operator = "is_null"
	OpIsNotNull  Operator = "is_not_null"
	OpBetween    Operator = "between"
)

// sqlOperators maps each operator onto a fixed SQL fragment.
//
// The fragment is chosen by lookup, never built from input, so an unrecognised
// operator cannot introduce SQL of its own.
var sqlOperators = map[Operator]string{
	OpEq: "=", OpNeq: "<>", OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<=",
	OpLike: "LIKE", OpNotLike: "NOT LIKE", OpContains: "LIKE",
	OpStartsWith: "LIKE", OpEndsWith: "LIKE",
}

// Filter is one condition in a row query.
type Filter struct {
	Column string   `json:"column"`
	Op     Operator `json:"op"`
	Value  any      `json:"value"`
	// Values carries the operands for IN, NOT IN and BETWEEN.
	Values []any `json:"values"`
}

// Sort is one ordering term.
type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// ValidOperator reports whether op is supported.
func ValidOperator(op Operator) bool {
	switch op {
	case OpIn, OpNotIn, OpIsNull, OpIsNotNull, OpBetween:
		return true
	}
	_, ok := sqlOperators[op]
	return ok
}

// maxInValues bounds an IN list so a single request cannot generate an
// unbounded statement or exhaust SQLite's parameter limit.
const maxInValues = 500

// buildCondition renders one filter as a SQL fragment plus its bound
// arguments.
//
// The column name is validated against the table's real columns by the caller
// and then quoted; the value always travels as a "?" placeholder.
func buildCondition(f Filter, columnTypes map[string]string) (string, []any, error) {
	if _, ok := columnTypes[f.Column]; !ok {
		return "", nil, fmt.Errorf("unknown column %q", f.Column)
	}
	quoted, err := database.QuoteIdent(f.Column)
	if err != nil {
		return "", nil, err
	}
	declared := columnTypes[f.Column]

	switch f.Op {
	case OpIsNull:
		return quoted + " IS NULL", nil, nil
	case OpIsNotNull:
		return quoted + " IS NOT NULL", nil, nil

	case OpIn, OpNotIn:
		if len(f.Values) == 0 {
			// An empty IN list is a contradiction; express it without letting
			// SQLite parse an empty parenthesis group.
			if f.Op == OpIn {
				return "0 = 1", nil, nil
			}
			return "1 = 1", nil, nil
		}
		if len(f.Values) > maxInValues {
			return "", nil, fmt.Errorf("filter on %q has %d values; the maximum is %d",
				f.Column, len(f.Values), maxInValues)
		}
		args := make([]any, 0, len(f.Values))
		for _, v := range f.Values {
			bound, err := bindFor(v, declared)
			if err != nil {
				return "", nil, fmt.Errorf("filter on %q: %w", f.Column, err)
			}
			args = append(args, bound)
		}
		// One placeholder per value; the count comes from the list length, not
		// from any user-supplied text.
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
		op := "IN"
		if f.Op == OpNotIn {
			op = "NOT IN"
		}
		return quoted + " " + op + " (" + placeholders + ")", args, nil

	case OpBetween:
		if len(f.Values) != 2 {
			return "", nil, fmt.Errorf("filter on %q with 'between' needs exactly two values", f.Column)
		}
		lo, err := bindFor(f.Values[0], declared)
		if err != nil {
			return "", nil, err
		}
		hi, err := bindFor(f.Values[1], declared)
		if err != nil {
			return "", nil, err
		}
		return quoted + " BETWEEN ? AND ?", []any{lo, hi}, nil
	}

	sqlOp, ok := sqlOperators[f.Op]
	if !ok {
		return "", nil, fmt.Errorf("unsupported operator %q", f.Op)
	}

	// The pattern operators wrap the value with wildcards. The wildcards are
	// added to the bound argument, so they cannot escape into the statement.
	switch f.Op {
	case OpContains, OpStartsWith, OpEndsWith:
		s, ok := f.Value.(string)
		if !ok {
			return "", nil, fmt.Errorf("filter on %q with %q needs a text value", f.Column, f.Op)
		}
		escaped := escapeLikePattern(s)
		var pattern string
		switch f.Op {
		case OpContains:
			pattern = "%" + escaped + "%"
		case OpStartsWith:
			pattern = escaped + "%"
		default:
			pattern = "%" + escaped
		}
		// The ESCAPE clause makes a literal % or _ in the user's text match
		// itself instead of acting as a wildcard.
		return quoted + " LIKE ? ESCAPE '\\'", []any{pattern}, nil
	}

	if f.Value == nil {
		// "= NULL" never matches in SQL; the intent is almost always IS NULL.
		if f.Op == OpEq {
			return quoted + " IS NULL", nil, nil
		}
		if f.Op == OpNeq {
			return quoted + " IS NOT NULL", nil, nil
		}
	}
	bound, err := bindFor(f.Value, declared)
	if err != nil {
		return "", nil, fmt.Errorf("filter on %q: %w", f.Column, err)
	}
	return quoted + " " + sqlOp + " ?", []any{bound}, nil
}

// bindFor converts a filter operand for comparison against a column.
func bindFor(v any, declaredType string) (any, error) {
	bound, err := database.BindValue(v)
	if err != nil {
		return nil, err
	}
	// Comparing the text "10" against an INTEGER column would not match in
	// SQLite, so numeric columns get the same coercion as on write.
	coerced, err := database.CoerceToColumnType(bound, declaredType)
	if err != nil {
		// A non-numeric filter value against a numeric column simply matches
		// nothing; it is not a client error worth rejecting the request over.
		return bound, nil
	}
	return coerced, nil
}

// escapeLikePattern neutralises LIKE wildcards in user text.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// buildWhere combines filters into a WHERE clause.
func buildWhere(filters []Filter, columnTypes map[string]string) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	if len(filters) > 50 {
		return "", nil, fmt.Errorf("too many filters; the maximum is 50")
	}

	conditions := make([]string, 0, len(filters))
	args := []any{}
	for _, f := range filters {
		cond, condArgs, err := buildCondition(f, columnTypes)
		if err != nil {
			return "", nil, err
		}
		conditions = append(conditions, cond)
		args = append(args, condArgs...)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args, nil
}

// buildOrderBy renders an ORDER BY clause from validated columns.
func buildOrderBy(sorts []Sort, columnTypes map[string]string) (string, error) {
	if len(sorts) == 0 {
		return "", nil
	}
	if len(sorts) > 10 {
		return "", fmt.Errorf("too many sort columns; the maximum is 10")
	}

	terms := make([]string, 0, len(sorts))
	for _, s := range sorts {
		if _, ok := columnTypes[s.Column]; !ok {
			return "", fmt.Errorf("cannot sort by unknown column %q", s.Column)
		}
		quoted, err := database.QuoteIdent(s.Column)
		if err != nil {
			return "", err
		}
		// Direction is one of two fixed keywords, never caller text.
		direction := " ASC"
		if s.Desc {
			direction = " DESC"
		}
		terms = append(terms, quoted+direction)
	}
	return " ORDER BY " + strings.Join(terms, ", "), nil
}
