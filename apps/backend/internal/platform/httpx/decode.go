package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
)

// MaxBodyBytes is the request body limit applied by Decode.
const MaxBodyBytes = 1 << 20

var validate = sync.OnceValue(func() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			return ""
		case "":
			return f.Name
		}
		return name
	})
	return v
})

// NewValidator returns the process-wide validator used by Decode and Validate.
// Field names are reported by json tag; custom validations registered on it apply everywhere.
func NewValidator() *validator.Validate { return validate() }

// Decode reads a JSON body (max 1MB, unknown fields rejected, single value) into T and validates it.
// Failures are *apperr.Error (invalid, or 413 payload_too_large).
func Decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, decodeError(err)
	}
	if dec.More() {
		return v, apperr.Invalid("request body must contain a single JSON value", nil)
	}
	return v, Validate(v)
}

func decodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.Is(err, io.EOF):
		return apperr.Invalid("request body is empty", nil).Wrap(err)
	case errors.As(err, &maxErr):
		return apperr.New(http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes)).Wrap(err)
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return apperr.Invalid("malformed JSON", nil).Wrap(err)
	case errors.As(err, &typeErr):
		if typeErr.Field == "" {
			return apperr.Invalid("request body has the wrong type", nil).Wrap(err)
		}
		return apperr.Invalid("invalid request body",
			map[string]string{typeErr.Field: "must be " + jsonKind(typeErr.Type)}).Wrap(err)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return apperr.Invalid("invalid request body", map[string]string{field: "unknown field"}).Wrap(err)
	}
	return apperr.Invalid("invalid request body", nil).Wrap(err)
}

func jsonKind(t reflect.Type) string {
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
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return t.String()
}

// Validate runs the shared validator on v (structs or pointers to structs; other kinds pass) and maps
// failures to apperr.Invalid with messages keyed by json field path (e.g. "title", "address.city").
func Validate(v any) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	err := validate().Struct(v)
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
		fields[key] = FieldMessage(fe)
	}
	return apperr.Invalid("validation failed", fields).Wrap(err)
}

// FieldMessage renders a human-readable message for a validation failure.
func FieldMessage(fe validator.FieldError) string {
	p := fe.Param()
	switch fe.Tag() {
	case "required", "required_if", "required_unless", "required_with", "required_without":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "url", "http_url":
		return "must be a valid URL"
	case "uuid", "uuid4", "uuid7":
		return "must be a valid UUID"
	case "oneof":
		return "must be one of: " + strings.Join(strings.Fields(p), ", ")
	case "len":
		return lenMessage(fe, "exactly", p)
	case "min":
		return lenMessage(fe, "at least", p)
	case "max":
		return lenMessage(fe, "at most", p)
	case "gt":
		return "must be greater than " + p
	case "gte":
		return "must be greater than or equal to " + p
	case "lt":
		return "must be less than " + p
	case "lte":
		return "must be less than or equal to " + p
	case "alphanum":
		return "must contain only letters and digits"
	case "numeric", "number":
		return "must be numeric"
	}
	if p != "" {
		return fmt.Sprintf("failed %q validation (%s)", fe.Tag(), p)
	}
	return fmt.Sprintf("failed %q validation", fe.Tag())
}

func lenMessage(fe validator.FieldError, qual, p string) string {
	switch fe.Kind() {
	case reflect.String:
		return fmt.Sprintf("must be %s %s characters long", qual, p)
	case reflect.Slice, reflect.Array, reflect.Map:
		return fmt.Sprintf("must contain %s %s items", qual, p)
	}
	return fmt.Sprintf("must be %s %s", qual, p)
}
