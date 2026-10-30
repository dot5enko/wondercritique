package crud

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// field is the CRUD view of one model column.
type field struct {
	json   string // client-facing name (body, filter and sort keys)
	column string // DB column
	typ    reflect.Type
	sf     *schema.Field

	create, update, filter, sort bool
}

// meta is the cached CRUD metadata of a model type.
type meta struct {
	name      string // lower-cased type name, used in messages
	table     string
	pk        *field
	createdAt *field // nil when the model has no created_at column
	byJSON    map[string]*field
	byColumn  map[string]*field
}

var metaCache sync.Map // reflect.Type -> *meta

// readonlyColumns are never writable from request bodies; the exposed ones are always filterable and sortable.
var readonlyColumns = map[string]bool{"id": true, "created_at": true, "updated_at": true, "deleted_at": true}

// metaFor parses (once per type) the GORM schema of T and its `crud` tags. It panics on misconfiguration.
func metaFor[T any](db *gorm.DB) *meta {
	t := reflect.TypeFor[T]()
	if m, ok := metaCache.Load(t); ok {
		return m.(*meta)
	}
	m, err := buildMeta(db, t)
	if err != nil {
		panic(fmt.Sprintf("crud: %v: %v", t, err))
	}
	actual, _ := metaCache.LoadOrStore(t, m)
	return actual.(*meta)
}

func buildMeta(db *gorm.DB, t reflect.Type) (*meta, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("model must be a struct")
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(reflect.New(t).Interface()); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	s := stmt.Schema
	if len(s.PrimaryFields) != 1 {
		return nil, fmt.Errorf("model must have exactly one primary key, has %d", len(s.PrimaryFields))
	}
	m := &meta{
		name:     strings.ToLower(t.Name()),
		table:    s.Table,
		byJSON:   map[string]*field{},
		byColumn: map[string]*field{},
	}
	for _, sf := range s.Fields {
		if sf.DBName == "" {
			continue // relations and ignored fields
		}
		name, _, _ := strings.Cut(sf.StructField.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = sf.StructField.Name
		}
		f := &field{json: name, column: sf.DBName, typ: sf.FieldType, sf: sf}
		if tag, ok := sf.StructField.Tag.Lookup("crud"); ok {
			for opt := range strings.SplitSeq(tag, ",") {
				switch strings.TrimSpace(opt) {
				case "create":
					f.create = true
				case "update":
					f.update = true
				case "filter":
					f.filter = true
				case "sort":
					f.sort = true
				case "":
				default:
					return nil, fmt.Errorf("field %s: unknown crud tag option %q", sf.Name, opt)
				}
			}
		}
		if sf.PrimaryKey || readonlyColumns[sf.DBName] {
			f.create, f.update = false, false
			f.filter, f.sort = true, true
		}
		if prev, dup := m.byJSON[name]; dup {
			return nil, fmt.Errorf("json name %q used by both %s and %s", name, prev.sf.Name, sf.Name)
		}
		m.byJSON[name] = f
		m.byColumn[sf.DBName] = f
		if sf.PrimaryKey {
			m.pk = f
		}
		if sf.DBName == "created_at" {
			m.createdAt = f
		}
	}
	if m.pk == nil {
		return nil, fmt.Errorf("primary key must be exposed through a json name")
	}
	return m, nil
}
