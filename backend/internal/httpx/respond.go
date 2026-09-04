package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/litebase/litebase/internal/logging"
)

type requestIDKey struct{}

// WithRequestID stores the request identifier on the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the identifier assigned to the current request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// JSON writes a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Responses may contain database contents; keep them out of shared caches.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if body == nil || status == http.StatusNoContent {
		return
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(body); err != nil {
		// The status line is already sent, so the only useful action is to log.
		logging.FromContext(context.Background()).Error("write json response", "error", err)
	}
}

// NoContent writes a bare 204.
func NoContent(w http.ResponseWriter) { JSON(w, http.StatusNoContent, nil) }

// errorEnvelope is the single shape every error response takes.
type errorEnvelope struct {
	Error     *Error `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// Fail writes err as a JSON error response and logs the internal cause.
//
// This is the only place errors reach a client, which is what makes the
// "never expose internals" rule enforceable rather than aspirational.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	e := AsError(err)
	if e == nil {
		return
	}

	log := logging.FromContext(r.Context())
	if e.Status >= 500 {
		cause := e.Cause()
		if cause == nil {
			cause = errors.New(e.Message)
		}
		log.Error("request failed",
			"status", e.Status, "code", e.Code, "error", cause.Error(),
			"method", r.Method, "path", r.URL.Path)
	} else {
		attrs := []any{"status", e.Status, "code", e.Code, "message", e.Message,
			"method", r.Method, "path", r.URL.Path}
		if cause := e.Cause(); cause != nil {
			attrs = append(attrs, "cause", cause.Error())
		}
		log.Debug("request rejected", attrs...)
	}

	if e.Status == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "60")
	}
	JSON(w, e.Status, errorEnvelope{Error: e, RequestID: RequestID(r.Context())})
}

// DecodeJSON reads a JSON request body into dst.
//
// Unknown fields are rejected so that a typo in a client payload surfaces as an
// error instead of being silently dropped, and the body is capped by the
// BodyLimit middleware before it reaches here.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !isJSONContentType(ct) {
		return BadRequest("Content-Type must be application/json")
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return TooLarge("request body is too large")
		}
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return BadRequest("malformed JSON at byte %d", syntaxErr.Offset)
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return BadRequest("field %q expects %s", typeErr.Field, typeErr.Type.String())
		}
		return BadRequest("invalid JSON body: %s", err.Error())
	}
	// A second value would mean the client sent a stream rather than one object.
	if dec.More() {
		return BadRequest("request body must contain a single JSON object")
	}
	return nil
}

func isJSONContentType(ct string) bool {
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			ct = ct[:i]
			break
		}
	}
	switch trimSpace(ct) {
	case "application/json", "text/json", "application/x-json":
		return true
	}
	return false
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
