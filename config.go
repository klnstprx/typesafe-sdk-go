package typesafe

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// config is the resolved, immutable client configuration.
type config struct {
	apiKey  string
	baseURL *url.URL // absolute http(s), no query or fragment
	model   string
	// timeout bounds each attempt; 0 means no per-attempt deadline, which only
	// happens when a supplied http.Client has Timeout 0 and WithTimeout is absent.
	timeout time.Duration
	headers http.Header
	retry   RetryPolicy
}

// String and GoString keep credentials out of formatted output.
func (config) String() string   { return "typesafe.config" }
func (config) GoString() string { return "typesafe.config{}" }

func resolveConfig(o *clientOptions) (*config, error) {
	key, err := resolveAPIKey(o.apiKey)
	if err != nil {
		return nil, err
	}
	base, err := resolveBaseURL(o.baseURL)
	if err != nil {
		return nil, err
	}
	timeout := DefaultTimeout
	switch {
	case o.timeout != nil:
		if *o.timeout <= 0 {
			return nil, errTimeout
		}
		timeout = *o.timeout
	case o.httpClient != nil:
		timeout = o.httpClient.Timeout
	}
	retry := DefaultRetryPolicy()
	if o.retry != nil {
		retry = *o.retry
	}
	if err := retry.validate(); err != nil {
		return nil, err
	}
	return &config{
		apiKey:  key,
		baseURL: base,
		model:   resolveString(o.model, DefaultModelEnv, DefaultModel),
		timeout: timeout,
		headers: o.headers.Clone(),
		retry:   retry,
	}, nil
}

var errTimeout = errors.New("typesafe: timeout must be a positive duration")

// resolveString prefers an explicit value, then a trimmed, non-empty
// environment value, then def.
func resolveString(explicit *string, env, def string) string {
	if explicit != nil {
		return *explicit
	}
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return def
}

func resolveAPIKey(explicit *string) (string, error) {
	key := strings.TrimSpace(resolveString(explicit, APIKeyEnv, ""))
	if key == "" {
		return "", errors.New("typesafe: no API key was provided; pass WithAPIKey or set the " + APIKeyEnv + " environment variable")
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c <= ' ' || c > '~' {
			return "", errors.New("typesafe: API key must contain only printable ASCII characters without whitespace")
		}
	}
	return key, nil
}

func resolveBaseURL(explicit *string) (*url.URL, error) {
	var raw string
	if explicit != nil {
		raw = *explicit
	} else {
		raw = os.Getenv(BaseURLEnv)
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = DefaultBaseURL
	}
	// The URL is never echoed in errors: it may carry userinfo credentials.
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("typesafe: base URL must be an absolute http or https URL")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("typesafe: base URL must not contain a query or fragment")
	}
	return u, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// buildHTTPClient returns the client the SDK sends with and, when the SDK owns
// it, the transport that Close releases. A supplied client is shallow-copied so
// that SDK per-attempt timeouts and the no-redirect policy apply without
// mutating it; its Transport (wrapped by redirectCapture) and Jar are shared.
func buildHTTPClient(supplied *http.Client) (*http.Client, *http.Transport) {
	if supplied != nil {
		c := *supplied
		c.Timeout = 0
		c.CheckRedirect = noRedirect
		base := supplied.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		c.Transport = redirectCapture{base}
		return &c, nil
	}
	var t *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	} else {
		t = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	return &http.Client{Transport: redirectCapture{t}, CheckRedirect: noRedirect}, t
}
