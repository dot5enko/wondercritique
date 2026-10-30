package crud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

// Service implements the CRUD operations for model T without any HTTP concerns.
// All queries go through dbx.Conn; mutations run in dbx.Tx so hooks share the transaction.
type Service[T any] struct {
	db          *gorm.DB
	validate    *validator.Validate
	meta        *meta
	scopes      []ScopeFunc
	hooks       []Hooks[T]
	authorize   []AuthorizeFunc[T]
	maxLimit    int
	defaultSort []sortKey
}

// NewService builds a service for T. HTTP-only options (Use, UseFor, Only) are ignored. A nil validator
// uses the shared httpx validator. It panics on misconfiguration (bad tags, mismatched generic options,
// unknown DefaultSort keys).
func NewService[T any](db *gorm.DB, v *validator.Validate, opts ...Option) *Service[T] {
	return newService[T](db, v, newSettings(opts))
}

func newService[T any](db *gorm.DB, v *validator.Validate, st *settings) *Service[T] {
	if v == nil {
		v = httpx.NewValidator()
	}
	s := &Service[T]{db: db, validate: v, meta: metaFor[T](db), maxLimit: st.maxLimit}
	for _, o := range st.typed {
		checkType[T](o)
		switch val := o.value.(type) {
		case ScopeFunc:
			s.scopes = append(s.scopes, val)
		case Hooks[T]:
			s.hooks = append(s.hooks, val)
		case AuthorizeFunc[T]:
			s.authorize = append(s.authorize, val)
		default:
			panic(fmt.Sprintf("crud: unexpected %s option value %T", o.name, o.value))
		}
	}
	def := st.defaultSort
	if def == "" {
		def = "-" + s.meta.pk.json
		if s.meta.createdAt != nil {
			def = "-" + s.meta.createdAt.json
		}
	}
	keys, err := s.parseSort(def)
	if err != nil {
		panic(fmt.Sprintf("crud: DefaultSort(%q): %v", def, err))
	}
	s.defaultSort = keys
	return s
}

// Query returns the scoped base query for T (scopes applied, soft-deleted rows excluded),
// bound to ctx and to the transaction in ctx if any.
func (s *Service[T]) Query(ctx context.Context) *gorm.DB {
	q := dbx.Conn(ctx, s.db).Model(new(T))
	for _, sc := range s.scopes {
		q = sc(ctx, q)
	}
	return q
}

func (s *Service[T]) pkExpr(id string) clause.Eq {
	return clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: s.meta.pk.column}, Value: id}
}

func (s *Service[T]) notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound(s.meta.name + " not found").Wrap(err)
	}
	return dbError(err)
}

// dbError maps constraint violations to client errors; anything else stays internal.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return apperr.Conflict("already exists").Wrap(err)
	}
	return err
}

func (s *Service[T]) authorized(ctx context.Context, op Op, v *T) error {
	for _, fn := range s.authorize {
		if err := fn(ctx, op, v); err != nil {
			return err
		}
	}
	return nil
}

// load fetches id within scope.
func (s *Service[T]) load(ctx context.Context, id string) (*T, error) {
	var v T
	if err := s.Query(ctx).Where(s.pkExpr(id)).Take(&v).Error; err != nil {
		return nil, s.notFound(err)
	}
	return &v, nil
}

// reload fetches id by primary key only (no scope), e.g. after a write.
func (s *Service[T]) reload(ctx context.Context, id string) (*T, error) {
	var v T
	if err := dbx.Conn(ctx, s.db).Model(new(T)).Where(s.pkExpr(id)).Take(&v).Error; err != nil {
		return nil, s.notFound(err)
	}
	return &v, nil
}

func (s *Service[T]) pkOf(v *T) string {
	return fmt.Sprint(s.meta.pk.sf.ReflectValueOf(context.Background(), reflect.ValueOf(v).Elem()).Interface())
}

// Get returns the entity with id, or not_found if it is missing or outside the scope.
func (s *Service[T]) Get(ctx context.Context, id string) (*T, error) {
	v, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorized(ctx, OpGet, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Create builds a T from the create-tagged keys of input, runs BeforeCreate, validates and inserts it.
// Unknown or non-creatable keys are rejected with invalid.
func (s *Service[T]) Create(ctx context.Context, input map[string]json.RawMessage) (*T, error) {
	var v T
	rv := reflect.ValueOf(&v).Elem()
	fields := map[string]string{}
	for key, raw := range input {
		f, ok := s.meta.byJSON[key]
		switch {
		case !ok:
			fields[key] = "unknown field"
		case !f.create:
			fields[key] = "is read-only"
		default:
			if err := json.Unmarshal(raw, f.sf.ReflectValueOf(ctx, rv).Addr().Interface()); err != nil {
				fields[key] = "must be " + jsonKind(f.typ)
			}
		}
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid request body", fields)
	}

	var id string
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		for _, h := range s.hooks {
			if h.BeforeCreate != nil {
				if err := h.BeforeCreate(ctx, &v); err != nil {
					return err
				}
			}
		}
		if err := s.validateStruct(&v); err != nil {
			return err
		}
		if err := s.authorized(ctx, OpCreate, &v); err != nil {
			return err
		}
		if err := dbx.Conn(ctx, s.db).Create(&v).Error; err != nil {
			return dbError(err)
		}
		for _, h := range s.hooks {
			if h.AfterCreate != nil {
				if err := h.AfterCreate(ctx, &v); err != nil {
					return err
				}
			}
		}
		id = s.pkOf(&v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.reload(ctx, id)
}

// Update applies the update-tagged keys of input to the entity with id (partial update: only present keys
// are written, zero values included). Unknown or non-updatable keys are rejected with invalid.
func (s *Service[T]) Update(ctx context.Context, id string, input map[string]json.RawMessage) (*T, error) {
	patch := make(map[string]any, len(input))
	fields := map[string]string{}
	for key, raw := range input {
		f, ok := s.meta.byJSON[key]
		switch {
		case !ok:
			fields[key] = "unknown field"
		case !f.update:
			fields[key] = "is read-only"
		default:
			ptr := reflect.New(f.typ)
			if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
				fields[key] = "must be " + jsonKind(f.typ)
				continue
			}
			patch[key] = ptr.Elem().Interface()
		}
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid request body", fields)
	}

	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		existing, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		if err := s.authorized(ctx, OpUpdate, existing); err != nil {
			return err
		}
		for _, h := range s.hooks {
			if h.BeforeUpdate != nil {
				if err := h.BeforeUpdate(ctx, existing, patch); err != nil {
					return err
				}
			}
		}
		next := *existing
		rv := reflect.ValueOf(&next).Elem()
		cols := make([]string, 0, len(patch))
		for _, key := range slices.Sorted(maps.Keys(patch)) {
			f, ok := s.meta.byJSON[key]
			if !ok || f == s.meta.pk {
				return fmt.Errorf("crud: BeforeUpdate patch has unknown or immutable key %q", key)
			}
			if err := assign(f.sf.ReflectValueOf(ctx, rv), patch[key]); err != nil {
				return fmt.Errorf("crud: patch key %q: %w", key, err)
			}
			cols = append(cols, f.column)
		}
		if err := s.validateStruct(&next); err != nil {
			return err
		}
		if len(cols) > 0 {
			if err := dbx.Conn(ctx, s.db).Model(&next).Select(cols).Updates(&next).Error; err != nil {
				return dbError(err)
			}
		}
		for _, h := range s.hooks {
			if h.AfterUpdate != nil {
				if err := h.AfterUpdate(ctx, &next); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.reload(ctx, id)
}

// Delete removes the entity with id (soft delete when T has gorm.DeletedAt).
func (s *Service[T]) Delete(ctx context.Context, id string) error {
	return dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		v, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		if err := s.authorized(ctx, OpDelete, v); err != nil {
			return err
		}
		for _, h := range s.hooks {
			if h.BeforeDelete != nil {
				if err := h.BeforeDelete(ctx, v); err != nil {
					return err
				}
			}
		}
		if err := dbx.Conn(ctx, s.db).Delete(v).Error; err != nil {
			return dbError(err)
		}
		for _, h := range s.hooks {
			if h.AfterDelete != nil {
				if err := h.AfterDelete(ctx, v); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// assign sets dst to v, converting when the types are compatible; nil sets the zero value.
func assign(dst reflect.Value, v any) error {
	if v == nil {
		dst.SetZero()
		return nil
	}
	rv := reflect.ValueOf(v)
	switch {
	case rv.Type().AssignableTo(dst.Type()):
		dst.Set(rv)
	case rv.Type().ConvertibleTo(dst.Type()) && rv.Kind() == dst.Kind():
		dst.Set(rv.Convert(dst.Type()))
	default:
		return fmt.Errorf("cannot assign %T to %v", v, dst.Type())
	}
	return nil
}

// validateStruct validates v with the service's validator, mapping failures to apperr.Invalid keyed by
// field path (json names when the validator uses the shared httpx tag-name func).
func (s *Service[T]) validateStruct(v *T) error {
	err := s.validate.Struct(v)
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return err
	}
	fields := make(map[string]string, len(verrs))
	for _, fe := range verrs {
		key := fe.Namespace()
		if _, rest, ok := strings.Cut(key, "."); ok {
			key = rest
		}
		fields[key] = httpx.FieldMessage(fe)
	}
	return apperr.Invalid("validation failed", fields).Wrap(err)
}

func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map:
		return "an object"
	}
	return "a valid " + t.String()
}
