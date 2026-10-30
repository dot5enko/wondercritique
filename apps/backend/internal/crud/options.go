package crud

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	"gorm.io/gorm"
)

// Op is a CRUD operation.
type Op string

const (
	OpList   Op = "list"
	OpGet    Op = "get"
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
)

var allOps = []Op{OpList, OpGet, OpCreate, OpUpdate, OpDelete}

// Middleware is a standard net/http middleware.
type Middleware = func(http.Handler) http.Handler

// ScopeFunc narrows the base query of every operation (list, get, update, delete).
type ScopeFunc = func(ctx context.Context, q *gorm.DB) *gorm.DB

// Hooks run inside the operation's transaction; a non-nil error aborts the operation and rolls it back.
type Hooks[T any] struct {
	// BeforeCreate runs after the body is decoded and before validation, so it can set server-side fields.
	BeforeCreate func(ctx context.Context, v *T) error
	AfterCreate  func(ctx context.Context, v *T) error
	// BeforeUpdate receives the stored row (before the change) and the patch keyed by json field name with
	// decoded Go values. It may modify the patch, including setting server-side (untagged) fields; the
	// resulting patch is applied, validated and written.
	BeforeUpdate func(ctx context.Context, existing *T, patch map[string]any) error
	AfterUpdate  func(ctx context.Context, v *T) error
	BeforeDelete func(ctx context.Context, v *T) error
	AfterDelete  func(ctx context.Context, v *T) error
}

// AuthorizeFunc decides whether op may proceed on v. For OpList v is nil; for OpCreate it is the new entity
// (after BeforeCreate); for get/update/delete it is the stored row. Return an apperr (e.g. Forbidden) to deny.
type AuthorizeFunc[T any] = func(ctx context.Context, op Op, v *T) error

// Option configures a resource or service. Generic options (Scope, WithHooks, Authorize) carry their model
// type and panic at registration when used with a different model.
type Option interface{ apply(*settings) }

type optionFunc func(*settings)

func (f optionFunc) apply(s *settings) { f(s) }

type typedOption struct {
	name  string
	typ   reflect.Type
	value any
}

type settings struct {
	middleware   []Middleware
	opMiddleware map[Op][]Middleware
	only         map[Op]bool // nil = all ops
	typed        []typedOption
	maxLimit     int
	defaultSort  string
}

func newSettings(opts []Option) *settings {
	s := &settings{opMiddleware: map[Op][]Middleware{}, maxLimit: DefaultMaxLimit}
	for _, o := range opts {
		o.apply(s)
	}
	return s
}

func (s *settings) enabled(op Op) bool { return s.only == nil || s.only[op] }

// Use adds middleware to every route of the resource, including routes added with Resource.Route.
func Use(mw ...Middleware) Option {
	return optionFunc(func(s *settings) { s.middleware = append(s.middleware, mw...) })
}

// UseFor adds middleware to a single operation's route.
func UseFor(op Op, mw ...Middleware) Option {
	return optionFunc(func(s *settings) { s.opMiddleware[op] = append(s.opMiddleware[op], mw...) })
}

// Only restricts the routes that are mounted to ops. Repeated calls accumulate.
func Only(ops ...Op) Option {
	return optionFunc(func(s *settings) {
		if s.only == nil {
			s.only = map[Op]bool{}
		}
		for _, op := range ops {
			s.only[op] = true
		}
	})
}

// MaxLimit caps ?limit= (default DefaultMaxLimit). Larger requested limits are clamped.
func MaxLimit(n int) Option {
	return optionFunc(func(s *settings) {
		if n < 1 {
			panic("crud: MaxLimit must be >= 1")
		}
		s.maxLimit = n
	})
}

// DefaultSort sets the list order used when ?sort= is absent, in ?sort= syntax (e.g. "-created_at").
func DefaultSort(sort string) Option {
	return optionFunc(func(s *settings) { s.defaultSort = sort })
}

func typed[T any](name string, v any) Option {
	return optionFunc(func(s *settings) {
		s.typed = append(s.typed, typedOption{name: name, typ: reflect.TypeFor[T](), value: v})
	})
}

// Scope narrows every operation's query; multiple scopes are applied in order.
func Scope[T any](fn ScopeFunc) Option { return typed[T]("Scope", fn) }

// WithHooks registers lifecycle hooks; multiple sets run in registration order.
func WithHooks[T any](h Hooks[T]) Option { return typed[T]("WithHooks", h) }

// Authorize registers a per-operation authorization check; all registered checks must pass.
func Authorize[T any](fn AuthorizeFunc[T]) Option { return typed[T]("Authorize", fn) }

func checkType[T any](o typedOption) {
	if want := reflect.TypeFor[T](); o.typ != want {
		panic(fmt.Sprintf("crud: %s[%v] option used with a resource of %v", o.name, o.typ, want))
	}
}
