package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// APIError reports an HTTP response whose status is not 2xx, including 3xx
// redirects, which the SDK never follows. Use [errors.As] to inspect it.
// Server-provided messages, bodies, and headers are preserved without credential
// redaction; Error includes the server's message and request ID.
type APIError struct {
	// StatusCode is the HTTP response status code.
	StatusCode int
	// Body is the decoded JSON error body (as produced by [json.Unmarshal] into
	// any), the plain response text when the body is not JSON, or nil when the
	// body is empty.
	Body any
	// RawBody holds the response body bytes exactly as received.
	RawBody []byte
	// Header holds the HTTP response headers.
	Header http.Header
	// Endpoint is the request method and URL, without credentials, query, or
	// fragment, such as "POST https://api.typesafe.ai/v1/systemone".
	Endpoint string
	// Message is the server's error message, or a summary of the body when it
	// carries no recognizable message.
	Message string
}

// Error returns the endpoint, status, message, and request ID, such as
// "POST https://api.typesafe.ai/v1/systemone: 429 Rate limit exceeded (request_id=req-1)".
func (e *APIError) Error() string {
	return formatHTTPError(e.Endpoint, e.StatusCode, e.Message, e.RequestID())
}

// HTTPStatusCode returns StatusCode, for code that probes errors with
// interface{ HTTPStatusCode() int }.
func (e *APIError) HTTPStatusCode() int { return e.StatusCode }

// RequestID returns the x-typesafe-request-id response header, or "" if absent.
func (e *APIError) RequestID() string { return e.Header.Get(headerRequestID) }

// RetryAfter returns the delay requested by the retry-after-ms or Retry-After
// response header, if the server sent a usable one.
func (e *APIError) RetryAfter() (time.Duration, bool) { return parseRetryAfter(e.Header) }

// ResponseValidationError reports a 2xx response whose body is missing required
// data or has data of the wrong type. Body, RawBody, and Header preserve the
// server's content without redaction; Err preserves custom decoding or validation
// errors. Error may include that error's message when FieldPath is empty.
type ResponseValidationError struct {
	// StatusCode is the HTTP response status code, or 0 when the error comes
	// from decoding JSON outside a client call.
	StatusCode int
	// Body is the decoded response body, as for [APIError.Body].
	Body any
	// RawBody holds the response body bytes exactly as received.
	RawBody []byte
	// Header holds the HTTP response headers, or nil outside a client call.
	Header http.Header
	// Endpoint is the request method and sanitized URL, or "" outside a client call.
	Endpoint string
	// FieldPath locates the first invalid field, such as "answers.tone.confidence"
	// or "models[1].name"; "" refers to the whole body.
	FieldPath string
	// Err is the underlying decoding or validation error, if any.
	Err error
}

// Error returns the endpoint, status, offending field path, and request ID.
func (e *ResponseValidationError) Error() string {
	msg := "Invalid response data at '" + e.FieldPath + "'."
	if e.FieldPath == "" && e.Err != nil {
		msg = "Invalid response data: " + e.Err.Error()
	}
	return formatHTTPError(e.Endpoint, e.StatusCode, msg, e.RequestID())
}

// Unwrap returns Err.
func (e *ResponseValidationError) Unwrap() error { return e.Err }

// HTTPStatusCode returns StatusCode, for code that probes errors with
// interface{ HTTPStatusCode() int }.
func (e *ResponseValidationError) HTTPStatusCode() int { return e.StatusCode }

// RequestID returns the x-typesafe-request-id response header, or "" if absent.
func (e *ResponseValidationError) RequestID() string { return e.Header.Get(headerRequestID) }

// ConnectionError reports a request that failed without an HTTP response, or
// whose response body could not be read. When the caller's context ends during
// an attempt or synchronous validation, errors.Is matches the context's error.
// Cancellation during a retry wait instead returns the context error directly.
// Transport causes without credential leaks retain their original error chain;
// redacted causes preserve errors.Is and net.Error behavior but do not expose
// the original cause through errors.As or errors.Unwrap.
type ConnectionError struct {
	// Err is the cause, with credentials redacted.
	Err error
}

func (e *ConnectionError) Error() string { return "typesafe: connection error: " + e.Err.Error() }

// Unwrap returns Err.
func (e *ConnectionError) Unwrap() error { return e.Err }

// TimeoutError reports an attempt that exceeded its per-attempt timeout or hit a
// transport deadline. errors.Is(err, context.DeadlineExceeded) reports true for
// every TimeoutError, and it satisfies [net.Error] with Timeout() true.
type TimeoutError struct {
	// Duration is the per-attempt timeout in effect, or 0 when the attempt had
	// none and a transport deadline fired instead.
	Duration time.Duration
	// Err is the cause, with credentials redacted.
	Err error
}

func (e *TimeoutError) Error() string {
	msg := "typesafe: request timed out"
	if e.Duration > 0 {
		msg += " (timeout=" + e.Duration.String() + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns Err.
func (e *TimeoutError) Unwrap() error { return e.Err }

// Is reports whether target is [context.DeadlineExceeded].
func (e *TimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// Timeout reports true; it is part of [net.Error].
func (e *TimeoutError) Timeout() bool { return true }

// Temporary reports true; it is part of [net.Error].
func (e *TimeoutError) Temporary() bool { return true }

func formatHTTPError(endpoint string, status int, msg, requestID string) string {
	s := msg
	if status != 0 {
		s = strconv.Itoa(status)
		if msg != "" {
			s += " " + msg
		}
	}
	if endpoint != "" {
		s = endpoint + ": " + s
	}
	if requestID != "" {
		s += " (request_id=" + requestID + ")"
	}
	return s
}

func newAPIError(resp *http.Response, raw []byte, endpoint string) *APIError {
	body := decodeBody(raw)
	return &APIError{
		StatusCode: resp.StatusCode,
		Body:       body,
		RawBody:    raw,
		Header:     resp.Header,
		Endpoint:   endpoint,
		Message:    errorMessage(body, raw),
	}
}

// decodeBody decodes JSON into any; non-JSON text is returned as a string with
// invalid UTF-8 replaced by U+FFFD, and an empty body as nil.
func decodeBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	return string(bytes.Runes(raw)) // one U+FFFD per invalid byte
}

func errorMessage(body any, raw []byte) string {
	if msg := extractMessage(body); msg != "" {
		return msg
	}
	if body == nil {
		return noBodyMessage
	}
	s, ok := body.(string)
	if !ok {
		var buf bytes.Buffer
		if json.Compact(&buf, raw) == nil {
			s = buf.String()
		} else {
			b, _ := json.Marshal(body)
			s = string(b)
		}
	}
	if utf8.RuneCountInString(s) > maxErrorBodyLen {
		s = string([]rune(s)[:maxErrorBodyLen]) + "…"
	}
	return s
}

// extractMessage finds the server's message in an error body, following the
// precedence error > error.message > message > detail > detail.message > the
// detail validation list. It returns "" when there is none.
func extractMessage(body any) string {
	if s, ok := body.(string); ok {
		return s
	}
	obj, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	errVal, msgVal, detail := obj["error"], obj["message"], obj["detail"]
	if s, ok := errVal.(string); ok {
		return s
	}
	if m, ok := errVal.(map[string]any); ok {
		if s, ok := m["message"].(string); ok {
			return s
		}
	}
	if s, ok := msgVal.(string); ok {
		return s
	}
	switch d := detail.(type) {
	case string:
		return d
	case map[string]any:
		if s, ok := d["message"].(string); ok {
			return s
		}
	case []any:
		var parts []string
		for _, entry := range d {
			e, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			msg, ok := e["msg"].(string)
			if !ok {
				continue
			}
			var path []string
			if loc, ok := e["loc"].([]any); ok {
				for _, item := range loc {
					if item == "body" {
						continue
					}
					path = append(path, locString(item))
				}
			}
			if len(path) > 0 {
				msg = strings.Join(path, ".") + ": " + msg
			}
			parts = append(parts, msg)
		}
		return strings.Join(parts, "; ")
	}
	return ""
}

func locString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// sanitizeURL renders u without userinfo, query, or fragment.
func sanitizeURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.ForceQuery = false
	c.Fragment = ""
	c.RawFragment = ""
	return c.String()
}

// parseRetryAfter reads retry-after-ms (milliseconds) and then Retry-After
// (seconds or an HTTP date). Invalid, negative, or non-finite retry-after-ms
// values fall through to Retry-After; a negative Retry-After means none.
// Values at or beyond the time.Duration range mean none.
func parseRetryAfter(h http.Header) (time.Duration, bool) {
	for _, name := range [...]string{headerRetryAfterMS, headerRetryAfter} {
		values := h.Values(name)
		if len(values) == 0 {
			continue
		}
		raw := strings.TrimSpace(values[0])
		if raw == "" {
			raw = "0"
		}
		value, err := strconv.ParseFloat(raw, 64)
		if strings.ContainsAny(raw, "xX") {
			// strconv accepts hexadecimal floats such as 0x1p4; Python's
			// float() and the Retry-After grammar do not.
			err = strconv.ErrSyntax
		}
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			if name == headerRetryAfter {
				if t, err := http.ParseTime(raw); err == nil {
					return max(0, time.Until(t)), true
				}
			}
			continue
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		if value < 0 {
			if name == headerRetryAfter {
				return 0, false
			}
			continue
		}
		scale := float64(time.Millisecond)
		if name == headerRetryAfter {
			scale = float64(time.Second)
		}
		if d, ok := durationFromFloat(math.Round(value * scale)); ok {
			return d, true
		}
	}
	return 0, false
}

// durationFromFloat converts nanoseconds to a Duration, reporting false for NaN,
// negative values, and values at or beyond 2^63.
func durationFromFloat(f float64) (time.Duration, bool) {
	if math.IsNaN(f) || f < 0 || f >= 1<<63 {
		return 0, false
	}
	return time.Duration(f), true
}
