package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// maxBodyBytes caps request bodies. Without a cap, a client can stream gigabytes into
// json.Decoder and exhaust memory. 1 MiB is generous for this API's tiny JSON payloads.
const maxBodyBytes = 1 << 20

// errorEnvelope is the one shape every error response takes, so clients write one error parser:
//
//	{"error": {"code": "not_found", "message": "...", "fields": {...}, "request_id": "..."}}
//
// Code is a stable machine-readable string; Message is for humans and may change.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// apiError is the HTTP-level rendering of an error.
type apiError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

// Sentinels for errors that originate in this layer (not in the domain).
var (
	errRateLimited = errors.New("rate limit exceeded")
	errBadGateway  = errors.New("upstream identity provider failed")
)

// badRequestError marks malformed client input detected while decoding (bad JSON, wrong types,
// unknown fields). Distinct from domain.ValidationError, which means "well-formed but
// semantically invalid": 400 versus 422.
type badRequestError struct{ msg string }

func (e *badRequestError) Error() string { return e.msg }

// mapError is the SINGLE place where errors become HTTP statuses. Lower layers return domain
// errors wrapped with context; this function inspects the chain with errors.Is/As.
//
// Two rules keep it safe:
//   - Messages for client-visible errors are fixed strings (or validation details we wrote), never
//     err.Error(), because a wrapped error's text can contain SQL, file paths or other internals.
//   - Anything unrecognised becomes a generic 500; the real error is logged server-side only.
func mapError(err error) apiError {
	if ve, ok := errors.AsType[*domain.ValidationError](err); ok {
		return apiError{http.StatusUnprocessableEntity, "validation_failed", "one or more fields are invalid", ve.Fields}
	}
	if bre, ok := errors.AsType[*badRequestError](err); ok {
		return apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: bre.msg}
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return apiError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
			Message: fmt.Sprintf("request body must be at most %d bytes", maxBodyBytes)}
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return apiError{Status: http.StatusNotFound, Code: "not_found", Message: "resource not found"}
	case errors.Is(err, domain.ErrConflict):
		return apiError{Status: http.StatusConflict, Code: "conflict", Message: "resource already exists"}
	case errors.Is(err, domain.ErrUnauthorized):
		return apiError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "invalid or missing credentials"}
	case errors.Is(err, domain.ErrForbidden):
		return apiError{Status: http.StatusForbidden, Code: "forbidden", Message: "you are not allowed to do that"}
	case errors.Is(err, domain.ErrInvalidToken):
		return apiError{Status: http.StatusBadRequest, Code: "invalid_token", Message: "the link or token is invalid or has expired"}
	case errors.Is(err, errBadGateway):
		return apiError{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "the identity provider could not complete the request"}
	case errors.Is(err, errRateLimited):
		return apiError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests, slow down"}
	case errors.Is(err, context.Canceled):
		// The client hung up. 499 is nginx's convention; nobody reads the response, but metrics
		// and logs stay honest, and it is not logged as a server fault.
		return apiError{Status: 499, Code: "client_closed_request", Message: "client closed request"}
	}
	return apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"}
}

// writeError renders err as the standard envelope and logs it appropriately: server faults
// (5xx) at Error level with the real cause, client mistakes not at all (the access log already
// records the status).
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	ae := mapError(err)
	if ae.Status >= 500 {
		log.ErrorContext(r.Context(), "request failed", "error", err, "request_id", requestIDFrom(r.Context()))
	}
	if ae.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="taskapi"`)
	}
	if ae.Status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, ae.Status, errorEnvelope{errorBody{
		Code: ae.Code, Message: ae.Message, Fields: ae.Fields, RequestID: requestIDFrom(r.Context()),
	}})
}

// writeJSON encodes v into a buffer FIRST and only then writes. If encoding fails we can still
// send a clean 500; encoding straight into the ResponseWriter would have already committed a
// 200 status and half a body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		http.Error(w, `{"error":{"code":"internal","message":"internal server error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // a write error means the client left; nothing useful to do
}

// decodeJSON reads exactly one JSON value from the request body into dst.
//
// Hardening, in order: cap the size (MaxBytesReader also tells the server to close the
// connection instead of draining a huge body), reject unknown fields (catches client typos like
// "titel" instead of silently ignoring them), and reject trailing garbage after the value.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return translateDecodeError(err)
	}
	// A second Decode must hit EOF; anything else is extra data after the first value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return err
		}
		return &badRequestError{"request body must contain a single JSON object"}
	}
	return nil
}

func translateDecodeError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return err // mapError turns it into 413
	}
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return &badRequestError{fmt.Sprintf("malformed JSON at byte %d", se.Offset)}
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
		return &badRequestError{fmt.Sprintf("field %q has the wrong type (expected %s)", te.Field, te.Type)}
	}
	switch {
	case errors.Is(err, io.EOF):
		return &badRequestError{"request body must not be empty"}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &badRequestError{"malformed JSON: unexpected end of input"}
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		// encoding/json has no typed error for this; the message is stable and safe to echo
		// (it only contains the field name the client sent).
		return &badRequestError{strings.TrimPrefix(err.Error(), "json: ")}
	}
	return &badRequestError{"invalid JSON body"}
}
