package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	testKey = "sk-test-0123456789"
	okBody  = `{"model":"jev-1","usage":{"input_tokens":3,"output_tokens":1},"answers":{"spam":{"type":"noul","noul":0.9}}}`
)

var spamQuestion = Questions{"spam": Noul{Instructions: "Is this spam?"}}

// recorded is a request observed by a test server or fake transport.
type recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (r *recorder) add(req *http.Request) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, recorded{req.Method, req.URL.Path, req.Header.Clone(), body})
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

func (r *recorder) count() int { return len(r.all()) }

// reply describes one canned response.
type reply struct {
	status int
	body   string
	header http.Header
	delay  time.Duration // time spent before responding; honors cancellation
}

// fakeTransport replays replies in order (repeating the last) and records
// requests. It never touches the network, so it works inside synctest.
type fakeTransport struct {
	recorder
	mu      sync.Mutex
	replies []reply
	n       int
	err     func(*http.Request) error // when set, returned instead of a reply
}

func newFake(replies ...reply) *fakeTransport { return &fakeTransport{replies: replies} }

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.add(req)
	f.mu.Lock()
	rep := f.replies[min(f.n, len(f.replies)-1)]
	f.n++
	f.mu.Unlock()
	if rep.delay > 0 {
		select {
		case <-time.After(rep.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	if f.err != nil {
		if err := f.err(req); err != nil {
			return nil, err
		}
	}
	return response(req, rep), nil
}

func response(req *http.Request, rep reply) *http.Response {
	h := rep.header.Clone()
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{
		StatusCode: rep.status,
		Status:     http.StatusText(rep.status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader([]byte(rep.body))),
		Request:    req,
	}
}

func ok(body string) reply { return reply{status: 200, body: body} }

func status(code int) reply { return reply{status: code, body: `{"error":"boom"}`} }

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

// clearEnv isolates a test from TYPESAFE_* variables in the environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{APIKeyEnv, BaseURLEnv, DefaultModelEnv} {
		t.Setenv(name, "")
	}
}

// fakeClient builds a client over a fake transport.
func fakeClient(t *testing.T, rt http.RoundTripper, opts ...Option) *Client {
	t.Helper()
	clearEnv(t)
	base := []Option{WithAPIKey(testKey), WithBaseURL("https://api.test"), WithHTTPClient(&http.Client{Transport: rt})}
	c, err := New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// serverClient builds a client against a real httptest server.
func serverClient(t *testing.T, h http.HandlerFunc, opts ...Option) (*Client, *recorder) {
	t.Helper()
	clearEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec.add(r)
		r.Body = io.NopCloser(bytes.NewReader(body))
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	base := []Option{WithAPIKey(testKey), WithBaseURL(srv.URL)}
	c, err := New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func noRetry() SharedOption { return WithRetryPolicy(RetryPolicy{}) }

var bg = context.Background()
