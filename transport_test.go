package typesafe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRetriedBodiesAreByteIdentical(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(status(500), status(500), ok(okBody))
		var getBodies [][]byte
		var mu sync.Mutex
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.GetBody == nil {
				t.Error("GetBody is nil")
			} else {
				b1, _ := r.GetBody()
				b2, _ := r.GetBody()
				x, _ := io.ReadAll(b1)
				y, _ := io.ReadAll(b2)
				mu.Lock()
				getBodies = append(getBodies, x, y)
				mu.Unlock()
			}
			return f.RoundTrip(r)
		})
		c := fakeClient(t, rt)
		if _, err := c.SystemOne(bg, map[string]any{"msg": "hi", "n": []any{1, nil}}, spamQuestion, WithExtraBody(map[string]any{"z": 1})); err != nil {
			t.Fatal(err)
		}
		reqs := f.all()
		if len(reqs) != 3 {
			t.Fatalf("%d requests", len(reqs))
		}
		for i, r := range reqs {
			if !bytes.Equal(r.Body, reqs[0].Body) {
				t.Errorf("attempt %d body differs: %s vs %s", i, r.Body, reqs[0].Body)
			}
			h0, h := reqs[0].Header.Clone(), r.Header.Clone()
			h0.Del("X-Typesafe-Retry-Count")
			h.Del("X-Typesafe-Retry-Count")
			if mustJSON(t, h0) != mustJSON(t, h) {
				t.Errorf("attempt %d headers differ beyond the retry count", i)
			}
		}
		for _, b := range getBodies {
			if !bytes.Equal(b, reqs[0].Body) {
				t.Errorf("GetBody yielded %s", b)
			}
		}
	})
}

// trackedBody records Close and can fail or block reads.
type trackedBody struct {
	io.Reader
	closed atomic.Bool
	block  context.Context // when set, Read blocks until it is done
	fail   error
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if b.block != nil {
		<-b.block.Done()
		return 0, b.block.Err()
	}
	if b.fail != nil {
		return 0, b.fail
	}
	return b.Reader.Read(p)
}

func (b *trackedBody) Close() error { b.closed.Store(true); return nil }

func TestNetworkBodiesAreClosed(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		fail   error
	}{
		"success":          {200, okBody, nil},
		"api error":        {500, `{"error":"x"}`, nil},
		"validation error": {200, `{"model":1}`, nil},
		"read failure":     {200, okBody, errors.New("connection reset")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var bodies []*trackedBody
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				b := &trackedBody{Reader: strings.NewReader(tc.body), fail: tc.fail}
				bodies = append(bodies, b)
				resp := response(r, reply{status: tc.status})
				resp.Body = b
				return resp, nil
			})
			c := fakeClient(t, rt, noRetry())
			_, _ = c.SystemOne(bg, "x", spamQuestion)
			if len(bodies) != 1 || !bodies[0].closed.Load() {
				t.Error("network body not closed")
			}
		})
	}
}

func TestAttemptReleasedBeforeRetryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu     sync.Mutex
			ctxs   []context.Context
			bodies []*trackedBody
		)
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b := &trackedBody{Reader: strings.NewReader(`{}`)}
			mu.Lock()
			ctxs = append(ctxs, r.Context())
			bodies = append(bodies, b)
			mu.Unlock()
			resp := response(r, reply{status: 503})
			resp.Body = b
			return resp, nil
		})
		c := fakeClient(t, rt, WithTimeout(time.Minute), WithRetryPolicy(RetryPolicy{MaxRetries: 1, BackoffInitial: time.Second, BackoffMax: time.Second}))
		done := make(chan struct{})
		go func() {
			_, _ = c.SystemOne(bg, "x", spamQuestion)
			close(done)
		}()
		synctest.Wait() // durably blocked in the retry wait
		mu.Lock()
		if len(ctxs) != 1 || ctxs[0].Err() == nil {
			t.Error("attempt context still live during the retry wait")
		}
		if !bodies[0].closed.Load() {
			t.Error("body still open during the retry wait")
		}
		mu.Unlock()
		<-done
	})
}

func TestBodyReadDeadlines(t *testing.T) {
	blocking := func() http.RoundTripper {
		var n atomic.Int32
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := response(r, reply{status: 200})
			if n.Add(1) == 1 {
				resp.Body = &trackedBody{block: r.Context()}
			} else {
				resp.Body = io.NopCloser(strings.NewReader(okBody))
			}
			return resp, nil
		})
	}
	t.Run("attempt deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := fakeClient(t, blocking(), noRetry(), WithTimeout(2*time.Second))
			start := time.Now()
			_, err := c.SystemOne(bg, "x", spamQuestion)
			var te *TimeoutError
			if !errors.As(err, &te) || te.Duration != 2*time.Second || time.Since(start) != 2*time.Second {
				t.Fatalf("err=%v after %v", err, time.Since(start))
			}
			if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
				t.Errorf("client unusable after a body timeout: %v", err)
			}
		})
	})
	t.Run("attempt deadline is retried", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := fakeClient(t, blocking(), WithTimeout(2*time.Second))
			if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
				t.Errorf("err = %v", err)
			}
		})
	})
	t.Run("caller deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := fakeClient(t, blocking(), WithTimeout(time.Minute))
			ctx, cancel := context.WithTimeout(bg, time.Second)
			defer cancel()
			_, err := c.SystemOne(ctx, "x", spamQuestion)
			var ce *ConnectionError
			if !errors.As(err, &ce) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %T %v", err, err)
			}
		})
	})
}

func TestTimeoutIdentity(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		var te *TimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("err = %T %v", err, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error("not context.DeadlineExceeded")
		}
		var ne interface{ Timeout() bool }
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Error("not a net.Error with Timeout()")
		}
	}
	t.Run("socket deadline", func(t *testing.T) {
		c := failingClient(t, func(*http.Request) error { return errDeadline })
		_, err := c.SystemOne(bg, "x", spamQuestion)
		check(t, err)
		if !errors.Is(err, errDeadline) {
			t.Error("cause not reachable through the chain")
		}
	})
	t.Run("sdk deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := fakeClient(t, newFake(reply{status: 200, delay: time.Hour}), noRetry(), WithTimeout(time.Second))
			_, err := c.SystemOne(bg, "x", spamQuestion)
			check(t, err)
		})
	})
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			writeJSON(w, 200, okBody)
			return
		}
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusFound)
	}
	for name, opts := range map[string][]Option{
		"owned client":    nil,
		"supplied client": {WithHTTPClient(&http.Client{})},
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := serverClient(t, handler, opts...)
			_, err := c.SystemOne(bg, "x", spamQuestion)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != 302 || apiErr.Header.Get("Location") != "/elsewhere" {
				t.Fatalf("err = %v", err)
			}
			reqs := rec.all()
			if len(reqs) != 1 || reqs[0].Method != "POST" || len(reqs[0].Body) == 0 {
				t.Errorf("requests = %+v", reqs)
			}
		})
	}
}

func TestMalformedRedirectIsTerminalAPIError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(reply{status: 302, body: `moved`, header: hdr("Location", "%zz", "X-Typesafe-Request-Id", "req-r")})
		c := fakeClient(t, f) // default retries
		_, err := c.SystemOne(bg, "x", spamQuestion)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %T %v, want *APIError", err, err)
		}
		if apiErr.StatusCode != 302 || apiErr.Header.Get("Location") != "%zz" || string(apiErr.RawBody) != "moved" || apiErr.RequestID() != "req-r" {
			t.Errorf("apiErr = %+v", apiErr)
		}
		if f.count() != 1 {
			t.Errorf("%d requests, want 1", f.count())
		}
	})
}

func TestRedirectCaptureKeepsClientContractHandling(t *testing.T) {
	nilBody := func(status int, contentLength int64) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := response(r, reply{status: status, header: hdr("Location", "%zz")})
			resp.Body, resp.ContentLength = nil, contentLength
			return resp, nil
		})
	}
	t.Run("nil response and nil error", func(t *testing.T) {
		rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
		_, err := fakeClient(t, rt, noRetry()).SystemOne(bg, "x", spamQuestion)
		var ce *ConnectionError
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), "nil *Response") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty redirect with nil body", func(t *testing.T) {
		_, err := fakeClient(t, nilBody(302, 0), noRetry()).SystemOne(bg, "x", spamQuestion)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 302 || apiErr.Header.Get("Location") != "%zz" || len(apiErr.RawBody) != 0 {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("nil body with content length", func(t *testing.T) {
		_, err := fakeClient(t, nilBody(302, 5), noRetry()).SystemOne(bg, "x", spamQuestion)
		var ce *ConnectionError
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), "content length 5 but a nil Body") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRequestHeaders(t *testing.T) {
	clientHeaders := http.Header{"X-Team": {"a"}, "X-Shared": {"client"}}
	lowerShared := strings.ToLower("X-Shared") // deliberately non-canonical
	callHeaders := http.Header{lowerShared: {"call"}}
	f := newFake(ok(okBody), ok(`{"models":[]}`))
	c := fakeClient(t, f,
		WithHeaders(clientHeaders),
		WithHeader("Authorization", "Bearer stolen"),
		WithHeader("User-Agent", "evil"),
		WithHeader("Accept", "text/html"),
	)
	if _, err := c.SystemOne(bg, "x", spamQuestion, WithHeaders(callHeaders), WithHeader("Content-Type", "text/plain"), WithHeader("X-Typesafe-Sdk", "fake")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Models.List(bg, WithHeader("Content-Type", "text/plain")); err != nil {
		t.Fatal(err)
	}
	post, get := f.all()[0].Header, f.all()[1].Header
	want := map[string]string{
		"Authorization":      "Bearer " + testKey,
		"Accept":             "application/json",
		"Content-Type":       "application/json",
		"User-Agent":         "typesafe-sdk-go/" + Version,
		"X-Typesafe-Sdk":     "typesafe-sdk-go/" + Version,
		"X-Team":             "a",
		"X-Shared":           "call",
		"X-Typesafe-Runtime": runtimeValue,
	}
	for k, v := range want {
		if got := post.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("POST %s = %q, want %q", k, got, v)
		}
	}
	if !strings.HasPrefix(runtimeValue, "go/1.") || !strings.Contains(runtimeValue, "; ") {
		t.Errorf("runtime header = %q", runtimeValue)
	}
	if get.Get("Content-Type") != "" {
		t.Errorf("GET Content-Type = %q", get.Get("Content-Type"))
	}
	if get.Get("X-Shared") != "client" {
		t.Errorf("per-call header persisted: X-Shared = %q", get.Get("X-Shared"))
	}
	if clientHeaders.Get("Authorization") != "" || len(callHeaders) != 1 || callHeaders[lowerShared][0] != "call" {
		t.Error("caller header maps mutated")
	}
	clientHeaders.Set("X-Team", "changed")
	if _, err := c.Models.List(bg); err != nil {
		t.Fatal(err)
	}
	if got := f.all()[2].Header.Get("X-Team"); got != "a" {
		t.Errorf("client headers alias the caller's map: X-Team = %q", got)
	}
}

func TestModelsList(t *testing.T) {
	f := newFake(reply{status: 200, body: `{"models":[{"name":"jev-latest","description":"General.","release_date":"2026-09-15","extra":1}]}`, header: hdr("X-Typesafe-Request-Id", "req-m")})
	c := fakeClient(t, f)
	resp, err := c.Models.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelMetadata{{"jev-latest", "General.", "2026-09-15"}}
	if mustJSON(t, resp.Models) != mustJSON(t, want) || resp.RequestID() != "req-m" {
		t.Errorf("resp = %+v", resp)
	}
	r := f.all()[0]
	if r.Method != "GET" || r.Path != "/v1/models" || len(r.Body) != 0 {
		t.Errorf("request = %+v", r)
	}
	if _, err := c.Models.List(bg, WithExtraBody(map[string]any{"a": 1})); err == nil {
		t.Error("List accepted WithExtraBody")
	}
}
