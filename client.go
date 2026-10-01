package typesafe

import (
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync/atomic"
	"time"
)

// Client calls the TypeSafe AI API. Create it with [New]; its zero value is not
// usable. It is safe for concurrent use and must not be copied after first use.
type Client struct {
	// Models lists the models available to the account.
	Models *ModelsService

	cfg        *config
	httpClient *http.Client
	// owned is the SDK-created transport that Close releases; nil when the
	// application supplied its own http.Client.
	owned  *http.Transport
	logger *slog.Logger
	closed atomic.Bool
}

// Option configures a [Client] in [New]. Options come from the With functions
// and may be reused. Nil options are ignored; later options take precedence.
type Option interface{ applyClient(*clientOptions) }

// CallOption configures a single API call. Options may be reused. Nil options
// are ignored; later options take precedence.
type CallOption interface{ applyCall(*callOptions) }

// SharedOption is accepted both by [New], where it sets the client default, and
// by individual calls, where it overrides that default for the call only.
type SharedOption interface {
	Option
	CallOption
}

type clientOptions struct {
	apiKey     *string
	baseURL    *string
	httpClient *http.Client
	logger     *slog.Logger
	model      *string
	timeout    *time.Duration
	retry      *RetryPolicy
	headers    http.Header
}

type callOptions struct {
	model     *string
	timeout   *time.Duration
	retry     *RetryPolicy
	headers   http.Header
	extraBody map[string]any
}

type clientOption func(*clientOptions)

func (f clientOption) applyClient(o *clientOptions) { f(o) }

type callOption func(*callOptions)

func (f callOption) applyCall(o *callOptions) { f(o) }

type sharedOption struct {
	client func(*clientOptions)
	call   func(*callOptions)
}

func (s sharedOption) applyClient(o *clientOptions) { s.client(o) }
func (s sharedOption) applyCall(o *callOptions)     { s.call(o) }

// WithAPIKey sets the API key, taking precedence over [APIKeyEnv]. Surrounding
// whitespace is trimmed. An invalid explicit key is an error; it does not fall
// back to the environment.
func WithAPIKey(key string) Option {
	return clientOption(func(o *clientOptions) { o.apiKey = &key })
}

// WithBaseURL sets the API root, taking precedence over [BaseURLEnv].
// Surrounding whitespace and trailing slashes are removed; a path prefix is
// kept. It must be an absolute http or https URL without a query or fragment.
func WithBaseURL(baseURL string) Option {
	return clientOption(func(o *clientOptions) { o.baseURL = &baseURL })
}

// WithHTTPClient sends requests through a copy of c, sharing its Transport and
// Jar, so applications can wrap the transport for tracing or metrics. The SDK
// never mutates c, never closes its transport, and never follows redirects. When
// [WithTimeout] is absent, c.Timeout becomes the per-attempt timeout (0 means
// none). A nil c uses the SDK's default client. Client fields are captured at
// [New]; the shared transport and jar remain caller-owned and must support
// concurrent use.
func WithHTTPClient(c *http.Client) Option {
	return clientOption(func(o *clientOptions) { o.httpClient = c })
}

// WithLogger sends the SDK's log records to l. Without it, or with a nil l, the
// SDK logs nothing. Records are emitted with the call's context: routine
// attempts and retries at DEBUG, ignored unknown answer types at WARN. Bodies,
// headers, credentials, and returned errors are never logged. The SDK never
// uses the process default logger or writes directly to stdout or stderr;
// l's handler controls filtering and output.
func WithLogger(l *slog.Logger) Option {
	return clientOption(func(o *clientOptions) { o.logger = l })
}

// WithModel sets the model, taking precedence over [DefaultModelEnv] on the
// client and over the client default on a call. A "model" key passed to
// [WithExtraBody] takes precedence over both.
func WithModel(model string) SharedOption {
	return sharedOption{
		client: func(o *clientOptions) { o.model = &model },
		call:   func(o *callOptions) { o.model = &model },
	}
}

// WithTimeout bounds each attempt, including reading its response body. It must
// be positive; zero does not disable the timeout. Without [WithHTTPClient], the
// default is [DefaultTimeout]; otherwise the supplied client's timeout applies
// unless overridden. The caller's context bounds network I/O and retry waits
// across the whole call. Synchronous JSON decoding and Validate hooks cannot be
// interrupted; see [SystemOneAs].
func WithTimeout(d time.Duration) SharedOption {
	return sharedOption{
		client: func(o *clientOptions) { o.timeout = &d },
		call:   func(o *callOptions) { o.timeout = &d },
	}
}

// WithRetryPolicy replaces the whole retry policy. Start from
// [DefaultRetryPolicy] to keep the other defaults. HTTPStatuses is copied when
// this option is created. Predicate is shared and must support concurrent calls.
func WithRetryPolicy(p RetryPolicy) SharedOption {
	p = p.clone()
	return sharedOption{
		client: func(o *clientOptions) { o.retry = &p },
		call:   func(o *callOptions) { o.retry = &p },
	}
}

// WithHeader sets a request header. Call headers override client headers with
// the same name, compared case-insensitively. Authorization, Accept,
// Content-Type, User-Agent, X-TypeSafe-SDK, X-TypeSafe-Runtime, and
// X-TypeSafe-Retry-Count are controlled by the SDK and cannot be overridden.
func WithHeader(key, value string) SharedOption {
	return WithHeaders(http.Header{key: {value}})
}

// WithHeaders sets several request headers, as [WithHeader] does. The map and
// its value slices are copied when this option is created.
func WithHeaders(h http.Header) SharedOption {
	h = canonicalHeader(h)
	return sharedOption{
		client: func(o *clientOptions) { o.headers = mergeHeader(o.headers, h) },
		call:   func(o *callOptions) { o.headers = mergeHeader(o.headers, h) },
	}
}

// WithExtraBody adds top-level request-body fields to a SystemOne call. They
// are shallow-merged over state, model, and questions, last write wins, and nil
// values are sent as JSON null. Object keys are then sorted. Models.List
// rejects it, even when fields is nil or empty.
//
// The top-level map is copied when this option is created. Nested maps, slices,
// pointers, and other referenced values remain caller-owned; do not mutate them
// while a call using the option runs. Values are encoded once per call, so all
// retries send the same body.
func WithExtraBody(fields map[string]any) CallOption {
	fields = maps.Clone(fields)
	return callOption(func(o *callOptions) {
		if o.extraBody == nil {
			o.extraBody = make(map[string]any, len(fields))
		}
		maps.Copy(o.extraBody, fields)
	})
}

func canonicalHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		out[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	return out
}

// mergeHeader returns dst with each key of src replacing dst's values.
func mergeHeader(dst, src http.Header) http.Header {
	if dst == nil {
		dst = make(http.Header, len(src))
	}
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}

// New creates a Client. Explicit options take precedence over environment
// variables, which take precedence over defaults. It fails when the API key is
// missing or malformed, the base URL is not an absolute http(s) URL, the
// timeout is not positive, or the retry policy is invalid.
// The environment is read once at construction; later changes do not affect c.
func New(opts ...Option) (*Client, error) {
	var o clientOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyClient(&o)
		}
	}
	cfg, err := resolveConfig(&o)
	if err != nil {
		return nil, err
	}
	httpClient, owned := buildHTTPClient(o.httpClient)
	c := &Client{cfg: cfg, httpClient: httpClient, owned: owned, logger: o.logger}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	c.Models = &ModelsService{client: c}
	return c, nil
}

// Close releases idle connections of the SDK-owned transport. A transport
// supplied through [WithHTTPClient] is left untouched. Calls made after Close
// fail. Calls already in progress, including their retries, are not cancelled;
// cancel their contexts to stop them. Close is safe for concurrent use,
// idempotent, and always returns nil.
func (c *Client) Close() error {
	if !c.closed.Swap(true) && c.owned != nil {
		c.owned.CloseIdleConnections()
	}
	return nil
}

var errClosed = errors.New("typesafe: client is closed")

// String returns a fixed description that contains no configuration or credentials.
func (c *Client) String() string { return "typesafe.Client" }

// GoString returns a fixed description for %#v that contains no configuration or credentials.
func (c *Client) GoString() string { return "typesafe.Client{}" }
