package crud

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
)

const (
	DefaultLimit    = 50
	DefaultMaxLimit = 200
)

// ListQuery is a transport-neutral list request. Field names are json names.
type ListQuery struct {
	Limit   int    // 0 = DefaultLimit; clamped to the service's MaxLimit
	Offset  int    // offset pagination; mutually exclusive with Cursor
	Cursor  string // opaque NextCursor of a previous page
	Sort    string // e.g. "-created_at,role"; empty = the service's default sort
	Filters []Filter
}

// Filter is one condition: Field Op Value. Ops: eq, ne, gt, gte, lt, lte, in (comma-separated list),
// like (SQL LIKE pattern, client supplies % and _).
type Filter struct {
	Field string
	Op    string
	Value string
}

// Page is a list response. NextCursor is set only in cursor mode when more items exist.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

var filterKey = regexp.MustCompile(`^filter\[([^\[\]]+)\](?:\[([^\[\]]+)\])?$`)

// ParseListQuery reads limit, offset, cursor, sort and filter[field] / filter[field][op] from query values.
// Other parameters are ignored.
func ParseListQuery(q url.Values) (ListQuery, error) {
	var lq ListQuery
	fields := map[string]string{}
	parseInt := func(key string, min int) int {
		s := q.Get(key)
		if s == "" {
			return 0
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < min {
			fields[key] = fmt.Sprintf("must be an integer >= %d", min)
		}
		return n
	}
	lq.Limit = parseInt("limit", 1)
	lq.Offset = parseInt("offset", 0)
	lq.Cursor = q.Get("cursor")
	lq.Sort = q.Get("sort")
	for key, vals := range q {
		m := filterKey.FindStringSubmatch(key)
		if m == nil {
			if strings.HasPrefix(key, "filter") {
				fields[key] = "malformed filter parameter"
			}
			continue
		}
		op := m[2]
		if op == "" {
			op = "eq"
		}
		for _, v := range vals {
			lq.Filters = append(lq.Filters, Filter{Field: m[1], Op: op, Value: v})
		}
	}
	if len(fields) > 0 {
		return lq, apperr.Invalid("invalid query parameters", fields)
	}
	return lq, nil
}

type sortKey struct {
	f    *field
	desc bool
}

// parseSort resolves "-a,b" to sortable fields and appends the primary key as tie-break (same direction as
// the last key) unless already present.
func (s *Service[T]) parseSort(spec string) ([]sortKey, error) {
	var keys []sortKey
	seen := map[*field]bool{}
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := strings.HasPrefix(part, "-")
		name := strings.TrimPrefix(strings.TrimPrefix(part, "-"), "+")
		f, ok := s.meta.byJSON[name]
		if !ok || !f.sort {
			return nil, apperr.Invalid("invalid sort", map[string]string{"sort": fmt.Sprintf("cannot sort by %q", name)})
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		keys = append(keys, sortKey{f: f, desc: desc})
	}
	if len(keys) == 0 {
		return nil, apperr.Invalid("invalid sort", map[string]string{"sort": "is empty"})
	}
	if !seen[s.meta.pk] {
		keys = append(keys, sortKey{f: s.meta.pk, desc: keys[len(keys)-1].desc})
	}
	return keys, nil
}

// cursorable reports whether keys support keyset pagination: [pk] or [created_at, pk] in one direction.
func (s *Service[T]) cursorable(keys []sortKey) bool {
	switch len(keys) {
	case 1:
		return keys[0].f == s.meta.pk
	case 2:
		return keys[0].f == s.meta.createdAt && keys[1].f == s.meta.pk && keys[0].desc == keys[1].desc
	}
	return false
}

// List returns one page. Cursor (keyset) pagination is available when the sort is by id or created_at
// (either direction, with the id tie-break); any other sort uses offset pagination and never returns
// NextCursor. Cursor and Offset are mutually exclusive.
func (s *Service[T]) List(ctx context.Context, lq ListQuery) (*Page[T], error) {
	if err := s.authorized(ctx, OpList, nil); err != nil {
		return nil, err
	}
	limit := lq.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, s.maxLimit)
	if lq.Offset < 0 {
		return nil, apperr.Invalid("invalid query parameters", map[string]string{"offset": "must be >= 0"})
	}

	keys := s.defaultSort
	if lq.Sort != "" {
		var err error
		if keys, err = s.parseSort(lq.Sort); err != nil {
			return nil, err
		}
	}
	cursorMode := s.cursorable(keys) && lq.Offset == 0
	if lq.Cursor != "" && !cursorMode {
		msg := "cannot be combined with offset"
		if lq.Offset == 0 {
			msg = "is only supported when sorting by id or created_at"
		}
		return nil, apperr.Invalid("invalid query parameters", map[string]string{"cursor": msg})
	}

	q := s.Query(ctx)
	conds, err := s.filterExprs(lq.Filters)
	if err != nil {
		return nil, err
	}
	if len(conds) > 0 {
		q = q.Where(clause.And(conds...))
	}
	if lq.Cursor != "" {
		if q, err = s.applyCursor(ctx, q, keys, lq.Cursor); err != nil {
			return nil, err
		}
	}
	for _, k := range keys {
		q = q.Order(clause.OrderByColumn{Column: col(k.f), Desc: k.desc})
	}
	if lq.Offset > 0 {
		q = q.Offset(lq.Offset)
	}

	items := make([]T, 0, limit+1)
	fetch := limit
	if cursorMode {
		fetch++
	}
	if err := q.Limit(fetch).Find(&items).Error; err != nil {
		return nil, err
	}
	page := &Page[T]{Items: items}
	if cursorMode && len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(s.pkOf(&page.Items[limit-1])))
	}
	return page, nil
}

func col(f *field) clause.Column {
	return clause.Column{Table: clause.CurrentTable, Name: f.column}
}

func invalidCursor() error {
	return apperr.Invalid("invalid query parameters", map[string]string{"cursor": "is invalid"})
}

// applyCursor adds the keyset condition "after the row with the cursor id" in sort order. The sort value of
// the cursor row is read through a subquery so it is compared in its stored representation. The cursor row
// must be within scope but may have been soft-deleted since the previous page.
func (s *Service[T]) applyCursor(ctx context.Context, q *gorm.DB, keys []sortKey, cursor string) (*gorm.DB, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return nil, invalidCursor()
	}
	id := string(raw)
	base := func() *gorm.DB {
		return s.Query(ctx).Unscoped().Where(s.pkExpr(id))
	}
	var n int64
	if err := base().Count(&n).Error; err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, invalidCursor()
	}
	cmp := ">"
	if keys[0].desc {
		cmp = "<"
	}
	pk := col(s.meta.pk)
	if len(keys) == 1 {
		return q.Where(clause.Expr{SQL: "? " + cmp + " ?", Vars: []any{pk, id}}), nil
	}
	c := col(keys[0].f)
	sub := base().Select(keys[0].f.column)
	return q.Where(clause.Expr{
		SQL:  "(? " + cmp + " (?) OR (? = (?) AND ? " + cmp + " ?))",
		Vars: []any{c, sub, c, sub, pk, id},
	}), nil
}

func (s *Service[T]) filterExprs(filters []Filter) ([]clause.Expression, error) {
	fields := map[string]string{}
	var exprs []clause.Expression
	for _, fl := range filters {
		key := "filter[" + fl.Field + "]"
		f, ok := s.meta.byJSON[fl.Field]
		if !ok || !f.filter {
			fields[key] = "cannot filter by this field"
			continue
		}
		c := col(f)
		var expr clause.Expression
		switch fl.Op {
		case "in":
			parts := strings.Split(fl.Value, ",")
			vals := make([]any, 0, len(parts))
			for _, p := range parts {
				v, err := parseValue(f.typ, p)
				if err != nil {
					fields[key] = err.Error()
					break
				}
				vals = append(vals, v)
			}
			expr = clause.IN{Column: c, Values: vals}
		case "like":
			if indirect(f.typ).Kind() != reflect.String {
				fields[key] = "like is only supported on text fields"
				continue
			}
			expr = clause.Like{Column: c, Value: fl.Value}
		case "eq", "ne", "gt", "gte", "lt", "lte":
			v, err := parseValue(f.typ, fl.Value)
			if err != nil {
				fields[key] = err.Error()
				continue
			}
			switch fl.Op {
			case "eq":
				expr = clause.Eq{Column: c, Value: v}
			case "ne":
				expr = clause.Neq{Column: c, Value: v}
			case "gt":
				expr = clause.Gt{Column: c, Value: v}
			case "gte":
				expr = clause.Gte{Column: c, Value: v}
			case "lt":
				expr = clause.Lt{Column: c, Value: v}
			case "lte":
				expr = clause.Lte{Column: c, Value: v}
			}
		default:
			fields[key] = fmt.Sprintf("unknown operator %q", fl.Op)
			continue
		}
		exprs = append(exprs, expr)
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid filter", fields)
	}
	return exprs, nil
}

func indirect(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

var timeType = reflect.TypeFor[time.Time]()

// parseValue converts a query string to a value comparable with a column of Go type t.
func parseValue(t reflect.Type, s string) (any, error) {
	t = indirect(t)
	if t == timeType || (t.Kind() == reflect.Struct && t.ConvertibleTo(timeType)) {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, fmt.Errorf("must be an RFC 3339 timestamp")
		}
		return ts.Local(), nil // GORM stores timestamps in local time
	}
	switch t.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("must be a boolean")
		}
		return b, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return n, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a non-negative integer")
		}
		return n, nil
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return n, nil
	}
	return s, nil
}
