package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
)

type systemOneBody struct {
	State     json.RawMessage            `json:"state"`
	Model     string                     `json:"model"`
	Questions map[string]json.RawMessage `json:"questions"`
}

// SystemOne answers the named questions about state, which must encode to a
// JSON string, object, or array: text, a struct, a map, or a slice. See
// https://docs.typesafe.ai/concepts/system-one.
//
// Client-side misuse, such as empty questions, fails before any network I/O.
// Otherwise the error is an *[APIError], *[ResponseValidationError],
// *[ConnectionError], or *[TimeoutError] from the last attempt, or the caller's
// context error during a retry wait. Use [errors.Is] to detect cancellation.
// State and questions remain caller-owned; do not mutate referenced values
// while the call runs. The request is encoded once and reused across retries.
//
// For a classifier or guard, require the expected answer and its concrete
// type; a missing or skipped answer is not a safe default.
func (c *Client) SystemOne(ctx context.Context, state any, questions Questions, opts ...CallOption) (*SystemOneResponse, error) {
	return SystemOneAs[SystemOneResponse](ctx, c, state, questions, opts...)
}

// SystemOneAs sends the same request as [Client.SystemOne] and decodes the whole
// response body into a new T with [json.Unmarshal], once per attempt.
//
// A type mismatch yields a *[ResponseValidationError] whose FieldPath locates
// the value, such as "answers.spam.noul". Missing fields cannot be detected
// generically: pointer fields distinguish absent or null values from zero, and
// Validate() error on *T can reject them. Its error is wrapped in a
// *ResponseValidationError and remains accessible through [errors.Is] and
// [errors.As]. Custom types receive the standard encoding/json behavior, including
// unknown fields; only [SystemOneResponse] applies the SDK's answer validation.
//
// JSON decoding and Validate run synchronously and cannot be interrupted, so
// keep custom hooks fast and free of I/O. If the caller's context ends while
// decoding or validating, that error takes precedence after the hook returns;
// the per-attempt timeout does not bound this processing. Embed [ResponseMeta]
// by value in T to receive the request ID, headers, and raw body.
func SystemOneAs[T any](ctx context.Context, c *Client, state any, questions Questions, opts ...CallOption) (*T, error) {
	req, err := c.prepareSystemOne(state, questions, opts)
	if err != nil {
		return nil, err
	}
	var out *T
	err = c.send(ctx, req, func(resp *http.Response, raw []byte) error {
		t := new(T)
		if r, ok := any(t).(*SystemOneResponse); ok {
			decoded, err := c.decodeSystemOne(ctx, raw)
			if err != nil {
				return newValidationError(resp, raw, req.endpoint, err)
			}
			*r = *decoded
		} else if err := json.Unmarshal(raw, t); err != nil {
			return newValidationError(resp, raw, req.endpoint, err)
		}
		if m, ok := any(t).(metaSetter); ok {
			m.setMeta(newMeta(resp, raw))
		}
		if v, ok := any(t).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return newValidationError(resp, raw, req.endpoint, err)
			}
		}
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) decodeSystemOne(ctx context.Context, raw []byte) (*SystemOneResponse, error) {
	return decodeSystemOne(raw, func(name, typ string) { c.logUnknownAnswer(ctx, name, typ) })
}

func (c *Client) prepareSystemOne(state any, questions Questions, opts []CallOption) (*request, error) {
	o := resolveCallOptions(opts)
	stateJSON, err := encodeContent(state, "state")
	if err != nil {
		return nil, err
	}
	qs, err := normalizeQuestions(questions)
	if err != nil {
		return nil, err
	}
	model := c.cfg.model
	if o.model != nil {
		model = *o.model
	}
	var body any = systemOneBody{State: stateJSON, Model: model, Questions: qs}
	if o.extraBody != nil {
		merged := map[string]any{"state": stateJSON, "model": model, "questions": qs}
		maps.Copy(merged, o.extraBody)
		body = merged
	}
	b, err := marshalJSON(body)
	if err != nil {
		return nil, fmt.Errorf("typesafe: the request body could not be encoded as JSON: %w", err)
	}
	return c.prepare(http.MethodPost, systemOnePath, b, o)
}

// newValidationError describes an invalid 2xx body. cause is a *pathError from
// the SDK decoders, an error from json.Unmarshal or Validate, or a
// *ResponseValidationError from a standalone UnmarshalJSON.
func newValidationError(resp *http.Response, raw []byte, endpoint string, cause error) *ResponseValidationError {
	e := &ResponseValidationError{
		StatusCode: resp.StatusCode,
		Body:       decodeBody(raw),
		RawBody:    raw,
		Header:     resp.Header,
		Endpoint:   endpoint,
	}
	var (
		pe        *pathError
		inner     *ResponseValidationError
		typeErr   *json.UnmarshalTypeError
		syntaxErr *json.SyntaxError
	)
	switch {
	case errors.As(cause, &pe):
		e.FieldPath = pe.path
	case errors.As(cause, &inner):
		e.FieldPath, e.Err = inner.FieldPath, inner.Err
	case errors.As(cause, &typeErr):
		e.Err = cause
		if path, ok := pathAtOffset(raw, typeErr.Offset); ok {
			e.FieldPath = path
		} else {
			e.FieldPath = typeErr.Field
		}
	case errors.As(cause, &syntaxErr):
		e.Err = cause
	default:
		e.Err = cause
	}
	return e
}
