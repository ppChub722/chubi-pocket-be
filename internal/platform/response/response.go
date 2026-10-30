package response

import (
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

type GeneralResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
}

// APIError is the envelope's `error`. Code is a registered apierr.Code; Message is safe, human
// English (never parse it); Details depends on the code (VALIDATION_ERROR: []apierr.FieldError).
type APIError struct {
	Code    apierr.Code `json:"code,omitempty"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// The gin validator names fields by their JSON tag, so FieldError paths are request-body paths.
func init() {
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			switch name {
			case "-":
				return ""
			case "":
				return f.Name
			}
			return name
		})
	}
}

func Success(c *gin.Context, status int, message string, data interface{}) {
	c.JSON(status, GeneralResponse{
		Success: true,
		Message: message,
		Data:    data,
	})
}

func Created(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusCreated, message, data)
}

func OK(c *gin.Context, message string, data interface{}) {
	Success(c, http.StatusOK, message, data)
}

// Fail writes the error envelope. errCode must be a registered apierr constant (registry_test).
// A message that looks like raw Go / DB error text is replaced by a generic one and the original is
// attached to the request (logged by the request logger), so internals never reach the client.
func Fail(c *gin.Context, status int, errCode apierr.Code, errMsg string, details interface{}) {
	if errCode == apierr.ValidationError && details == nil {
		details = []apierr.FieldError{}
	}
	if Leaky(errMsg) {
		_ = c.Error(&leakedText{errMsg})
		errMsg = safeMessage(status)
	}
	c.JSON(status, GeneralResponse{
		Success: false,
		Error: &APIError{
			Code:    errCode,
			Message: errMsg,
			Details: details,
		},
	})
}

func BadRequest(c *gin.Context, errCode apierr.Code, errMsg string, details interface{}) {
	Fail(c, http.StatusBadRequest, errCode, errMsg, details)
}

func NotFound(c *gin.Context, errCode apierr.Code, errMsg string) {
	Fail(c, http.StatusNotFound, errCode, errMsg, nil)
}

// Validation: 400 VALIDATION_ERROR; details = the field errors (always a list, maybe empty).
func Validation(c *gin.Context, errMsg string, fields ...apierr.FieldError) {
	if fields == nil {
		fields = []apierr.FieldError{}
	}
	Fail(c, http.StatusBadRequest, apierr.ValidationError, errMsg, fields)
}

// ValidationErr: 400 VALIDATION_ERROR from a service validation error. The message is the error's
// own (curated) text; the fields come from an apierr.Fields in its chain, then any extra given.
func ValidationErr(c *gin.Context, err error, extra ...apierr.FieldError) {
	fields := append(apierr.FieldsOf(err), extra...)
	Validation(c, err.Error(), fields...)
}

// BindError: 400 VALIDATION_ERROR for a request body that failed to bind (bad JSON, a wrong type,
// a `binding:` rule). The raw decoder / validator text is never sent; details lists the fields.
func BindError(c *gin.Context, err error) {
	_ = c.Error(err)
	Validation(c, "Invalid request body", apierr.FromBindError(err)...)
}

// InternalError: 500 INTERNAL_SERVER_ERROR with a safe message. err (may be nil) is attached to the
// request and logged by the request logger; it is never sent.
func InternalError(c *gin.Context, errMsg string, err error) {
	if err != nil {
		_ = c.Error(err)
	}
	Fail(c, http.StatusInternalServerError, apierr.InternalServerError, errMsg, nil)
}

type leakedText struct{ s string }

func (e *leakedText) Error() string { return "unsafe error message replaced: " + e.s }

// leakRe matches text that comes from Go / pgx / Postgres internals, never from our own messages.
var leakRe = regexp.MustCompile(`(?i)(^json: |^unexpected EOF$|^EOF$|Key: '|Error:Field validation|SQLSTATE|^ERROR: |\bpgx\b|\bpgconn\b|no rows in result set|duplicate key value|violates (foreign key|unique|check|not-null) constraint|invalid input syntax|strconv\.|invalid UUID|runtime error|context (deadline exceeded|canceled)|dial tcp|connection refused|^http: |broken pipe|cannot unmarshal|invalid character .* looking for)`)

// Leaky reports whether a message looks like raw internal error text.
func Leaky(msg string) bool { return leakRe.MatchString(msg) }

func safeMessage(status int) string {
	switch {
	case status >= 500:
		return "Something went wrong"
	case status == http.StatusNotFound:
		return "Not found"
	default:
		return "Invalid request"
	}
}

// NoRoute answers a path no route matches (r.NoRoute): the envelope with NOT_FOUND, never gin's
// plain-text "404 page not found".
func NoRoute(c *gin.Context) {
	NotFound(c, apierr.NotFound, "Route not found")
}
