package apierr

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Field error codes: the `code` of one FieldError in a VALIDATION_ERROR's `details`.
const (
	FieldRequired    = "required"     // missing or empty
	FieldTooLong     = "too_long"     // params.max (characters)
	FieldTooShort    = "too_short"    // params.min (characters)
	FieldTooMany     = "too_many"     // params.max (items)
	FieldTooFew      = "too_few"      // params.min (items)
	FieldOutOfRange  = "out_of_range" // params: any of min, max, gt, gte, lt, lte
	FieldInvalid     = "invalid"      // wrong value / format; params may hold allowed, format or len
	FieldInvalidType = "invalid_type" // a JSON value of the wrong type; params.expected
	FieldInvalidJSON = "invalid_json" // the body is not valid JSON (field "")
	FieldTooLarge    = "too_large"    // an uploaded file is too big; params.max_bytes
)

// FieldError is one entry of a VALIDATION_ERROR's `details`. Field is the JSON path in the request
// body (`name`, `splits[0].amount`), or the query / path parameter name; "" = the whole body.
// Params is always an object ({} when empty).
type FieldError struct {
	Field  string         `json:"field"`
	Code   string         `json:"code"`
	Params map[string]any `json:"params"`
}

// Field builds a FieldError; kv are param key / value pairs (`"max", 100`).
func Field(field, code string, kv ...any) FieldError {
	p := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			p[k] = kv[i+1]
		}
	}
	return FieldError{Field: field, Code: code, Params: p}
}

// Fields is an error carrying field errors; a service can return it (or wrap it) so the handler's
// VALIDATION_ERROR lists the fields.
type Fields struct {
	Msg  string
	List []FieldError
	base error
}

func (f *Fields) Error() string { return f.Msg }

// Invalid returns a *Fields error with one field.
func Invalid(msg, field, code string, kv ...any) error {
	return &Fields{Msg: msg, List: []FieldError{Field(field, code, kv...)}}
}

// FieldsOf returns the field errors carried by err (a *Fields anywhere in its chain), else nil.
func FieldsOf(err error) []FieldError {
	var f *Fields
	if errors.As(err, &f) {
		return f.List
	}
	return nil
}

// FromBindError turns a request-binding error (gin validator, encoding/json) into field errors.
// Unknown error kinds give one {field: "", code: invalid} entry; the raw text is never used.
func FromBindError(err error) []FieldError {
	var ves validator.ValidationErrors
	if errors.As(err, &ves) {
		out := make([]FieldError, 0, len(ves))
		for _, fe := range ves {
			out = append(out, fromValidator(fe))
		}
		return out
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return []FieldError{Field(jsonFieldPath(te.Field), FieldInvalidType, "expected", jsonKind(te.Type))}
	}
	var se *json.SyntaxError
	if errors.As(err, &se) || errors.Is(err, io.ErrUnexpectedEOF) {
		return []FieldError{Field("", FieldInvalidJSON)}
	}
	if errors.Is(err, io.EOF) {
		return []FieldError{Field("", FieldRequired)}
	}
	return []FieldError{Field("", FieldInvalid)}
}

// fromValidator maps one go-playground FieldError (namespaced by JSON names, see response init).
func fromValidator(fe validator.FieldError) FieldError {
	field := validatorPath(fe.Namespace())
	kind := fe.Kind()
	if kind == reflect.Pointer {
		kind = fe.Type().Elem().Kind()
	}
	param := paramValue(fe.Param())
	switch fe.Tag() {
	case "required", "required_if", "required_unless", "required_with", "required_without":
		return Field(field, FieldRequired)
	case "max", "lte", "lt":
		switch kind {
		case reflect.String:
			return Field(field, FieldTooLong, "max", param)
		case reflect.Slice, reflect.Array, reflect.Map:
			return Field(field, FieldTooMany, "max", param)
		}
		return Field(field, FieldOutOfRange, fe.Tag(), param)
	case "min", "gte", "gt":
		switch kind {
		case reflect.String:
			if n, ok := param.(float64); ok && fe.Tag() == "gt" {
				param = n + 1
			}
			return Field(field, FieldTooShort, "min", param)
		case reflect.Slice, reflect.Array, reflect.Map:
			return Field(field, FieldTooFew, "min", param)
		}
		return Field(field, FieldOutOfRange, fe.Tag(), param)
	case "oneof":
		return Field(field, FieldInvalid, "allowed", strings.Fields(fe.Param()))
	case "len":
		return Field(field, FieldInvalid, "len", param)
	case "datetime":
		return Field(field, FieldInvalid, "format", fe.Param())
	default: // email, url, uuid, alpha, ...
		return Field(field, FieldInvalid, "format", fe.Tag())
	}
}

// validatorPath drops the top struct name: "CreateRequest.splits[0].amount" -> "splits[0].amount".
func validatorPath(ns string) string {
	if i := strings.IndexByte(ns, '.'); i >= 0 {
		return ns[i+1:]
	}
	return ns
}

// jsonFieldPath: encoding/json reports nested fields dotted ("splits.amount"); kept as is.
func jsonFieldPath(f string) string { return f }

func paramValue(p string) any {
	if n, err := strconv.ParseFloat(p, 64); err == nil {
		return n
	}
	return p
}

func jsonKind(t reflect.Type) string {
	if t == nil {
		return "value"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		if t.String() == "uuid.UUID" {
			return "string"
		}
		return "object"
	}
	return "value"
}

// Wrap returns an error that matches base (errors.Is), reads "base: detail" and carries fields, so
// a service keeps its sentinel and the handler's VALIDATION_ERROR lists the fields.
func Wrap(base error, detail string, fields ...FieldError) error {
	return &Fields{Msg: base.Error() + ": " + detail, List: fields, base: base}
}

func (f *Fields) Unwrap() error { return f.base }
