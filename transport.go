package typesafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	userAgent    = sdkName + "/" + Version
	runtimeValue = "go/" + strings.TrimPrefix(runtime.Version(), "go") + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
)

// request is an immutable, fully encoded API request. Every attempt builds a
// fresh *http.Request from it, so retries send byte-identical bodies.
type request struct {
	method   string
	url      string
	endpoint string // "METHOD url" without userinfo, query, or fragment
	logURL   string
	header   http.Header
	body     []byte // nil for requests without a body
	timeout  time.Duration
	retry    RetryPolicy
	apiKey   string
}

// decodeFunc decodes a 2xx response inside its attempt. It must build a fresh
// result on every call and publish it only on success.
type decodeFunc func(resp *http.Response, raw []byte) error

func resolveCallOptions(opts []CallOption) *callOptions {
	var o callOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyCall(&o)
		}
	}
	return &o
}

// prepare merges headers, resolves the timeout and retry policy, and validates
// them before any network I/O.
func (c *Client) prepare(method, path string, body []byte, o *callOptions) (*request, error) {
	if c.closed.Load() {
		return nil, errClosed
	}
	timeout := c.cfg.timeout
	if o.timeout != nil {
		if *o.timeout <= 0 {
			return nil, errTimeout
		}
		timeout = *o.timeout
	}
	retry := c.cfg.retry
	if o.retry != nil {
		retry = *o.retry
		if err := retry.validate(); err != nil {
			return nil, err
		}
	}

	header := mergeHeader(c.cfg.headers.Clone(), o.headers)
	header.Del(headerRetryCount)
	header.Set("Authorization", "Bearer "+c.cfg.apiKey)
	header.Set("Accept", jsonContentType)
	header.Set("User-Agent", userAgent)
	header.Set(headerSDK, userAgent)
	header.Set(headerRuntime, runtimeValue)
	header.Del("Content-Type")
	if body != nil {
		header.Set("Content-Type", jsonContentType)
	}

	u := c.cfg.baseURL.JoinPath(path)
	logURL := sanitizeURL(u)
	return &request{
		method:   method,
		url:      u.String(),
		endpoint: method + " " + logURL,
		logURL:   logURL,
		header:   header,
		body:     body,
		timeout:  timeout,
		retry:    retry,
		apiKey:   c.cfg.apiKey,
	}, nil
}

// send runs the attempt loop. ctx bounds the whole operation, including waits;
// req.timeout bounds each attempt; req.retry.Timeout is the scheduling budget,
// checked only between attempts.
func (c *Client) send(ctx context.Context, req *request, decode decodeFunc) error {
	p := req.retry
	start := time.Now()
	for attempt := 0; ; attempt++ {
		err := c.doAttempt(ctx, req, attempt, decode)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if attempt >= p.MaxRetries || !p.retryable(err) {
			return err
		}
		delay := p.wait(attempt+1, err)
		if p.Timeout > 0 {
			// Subtract rather than add, so huge delays cannot overflow.
			if remaining := p.Timeout - time.Since(start); remaining <= 0 || delay >= remaining {
				return err
			}
		}
		c.logRetry(ctx, attempt+1, delay, retryReason(err))
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// doAttempt sends one attempt and reads its whole body under the attempt's
// deadline. The network body is closed and the attempt context cancelled
// before it returns, whatever the outcome.
func (c *Client) doAttempt(ctx context.Context, req *request, attempt int, decode decodeFunc) error {
	if err := ctx.Err(); err != nil {
		return &ConnectionError{Err: err}
	}
	attemptCtx, cancel := ctx, context.CancelFunc(func() {})
	if req.timeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, req.timeout)
	}
	defer cancel()

	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body) // NewRequest derives GetBody from it.
	}
	redirect := &capturedRedirect{}
	httpReq, err := http.NewRequestWithContext(context.WithValue(attemptCtx, capturedRedirectKey{}, redirect), req.method, req.url, body)
	if err != nil {
		return &ConnectionError{Err: newRedactor(req.apiKey, req.header, nil).sanitize(err)}
	}
	httpReq.Header = req.header.Clone()
	if attempt > 0 {
		httpReq.Header.Set(headerRetryCount, strconv.Itoa(attempt))
	}

	start := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	var raw []byte
	switch {
	case err != nil && redirect.resp != nil:
		// net/http rejected the 3xx (e.g. an unparseable Location) before
		// consulting CheckRedirect; report the response itself.
		resp, raw = redirect.resp, redirect.raw
	case err != nil:
		classified := classify(ctx, attemptCtx, req, httpReq, err)
		c.logAttempt(ctx, req, attempt, 0, errorKind(classified), time.Since(start), "")
		return classified
	default:
		defer resp.Body.Close()
		if raw, err = io.ReadAll(resp.Body); err != nil {
			classified := classify(ctx, attemptCtx, req, httpReq, err)
			c.logAttempt(ctx, req, attempt, 0, errorKind(classified), time.Since(start), "")
			return classified
		}
	}
	c.logAttempt(ctx, req, attempt, resp.StatusCode, "", time.Since(start), resp.Header.Get(headerRequestID))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp, raw, req.endpoint)
	}
	decodeErr := decode(resp, raw)
	// Decoding or a Validate hook may outlast the caller's deadline. As on the
	// transport-error path, an ended operation reports its cancellation, never
	// a result or a validation error.
	if err := ctx.Err(); err != nil {
		return &ConnectionError{Err: err}
	}
	return decodeErr
}

// capturedRedirect receives a buffered 3xx response from redirectCapture.
type capturedRedirect struct {
	resp *http.Response
	raw  []byte
}

type capturedRedirectKey struct{}

// redirectCapture wraps the transport the SDK sends through. It buffers 3xx
// responses and records them for the attempt, because net/http parses the
// Location header before CheckRedirect runs and turns a malformed one into a
// transport error, discarding the response.
type redirectCapture struct{ base http.RoundTripper }

func (t redirectCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	// Leave contract violations such as a nil response to http.Client, which
	// reports them as errors.
	if err != nil || resp == nil || resp.StatusCode < 300 || resp.StatusCode > 399 {
		return resp, err
	}
	slot, _ := req.Context().Value(capturedRedirectKey{}).(*capturedRedirect)
	if slot == nil {
		return resp, nil
	}
	if resp.Body == nil {
		// Mirror http.Client: a nil Body means empty unless ContentLength
		// promises content, which it reports as an invalid response.
		if resp.ContentLength > 0 && req.Method != http.MethodHead {
			return resp, nil
		}
		resp.Body = http.NoBody
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	slot.resp, slot.raw = resp, raw
	return resp, nil
}

// classify turns a transport or body-read failure into a *ConnectionError or
// *TimeoutError carrying the sanitized cause. Secrets are collected from the
// request as sent, after net/http has added any cookie-jar cookies.
func classify(parent, attemptCtx context.Context, req *request, sent *http.Request, err error) error {
	safe := newRedactor(req.apiKey, sent.Header, sent.URL).sanitize(err)
	if perr := parent.Err(); perr != nil {
		// The caller ended the operation; keep its error in the chain.
		if !errors.Is(safe, perr) {
			safe = fmt.Errorf("%w: %w", perr, safe)
		}
		return &ConnectionError{Err: safe}
	}
	if attemptCtx.Err() != nil {
		return &TimeoutError{Duration: req.timeout, Err: safe}
	}
	if chainHas(err, func(e error) bool { t, ok := e.(interface{ Timeout() bool }); return ok && t.Timeout() }) {
		return &TimeoutError{Duration: req.timeout, Err: safe}
	}
	return &ConnectionError{Err: safe}
}

// chainHas reports whether pred holds for any error in err's tree. Unlike
// errors.As, it keeps looking past a match whose method returns false, which
// matters because *url.Error.Timeout inspects only its direct cause.
func chainHas(err error, pred func(error) bool) bool {
	if err == nil {
		return false
	}
	if pred(err) {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return chainHas(u.Unwrap(), pred)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if chainHas(e, pred) {
				return true
			}
		}
	}
	return false
}

func errorKind(err error) string {
	var te *TimeoutError
	if errors.As(err, &te) {
		return "timeout"
	}
	return "connection"
}
