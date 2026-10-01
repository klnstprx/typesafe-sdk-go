package typesafe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestRetryPolicyValidation(t *testing.T) {
	bad := map[string]RetryPolicy{
		"negative retries":  {MaxRetries: -1},
		"negative initial":  {BackoffInitial: -1},
		"negative max":      {BackoffMax: -1},
		"negative timeout":  {Timeout: -1},
		"jitter NaN":        {BackoffJitter: math.NaN()},
		"jitter above one":  {BackoffJitter: 1.5},
		"jitter below zero": {BackoffJitter: -0.1},
	}
	for name, p := range bad {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			if _, err := New(WithAPIKey(testKey), WithRetryPolicy(p)); err == nil {
				t.Error("New accepted the policy")
			}
			f := newFake(ok(okBody))
			c := fakeClient(t, f)
			if _, err := c.SystemOne(bg, "x", spamQuestion, WithRetryPolicy(p)); err == nil || f.count() != 0 {
				t.Errorf("call err = %v after %d requests", err, f.count())
			}
		})
	}
	clearEnv(t)
	if _, err := New(WithAPIKey(testKey), WithRetryPolicy(RetryPolicy{Timeout: 0, BackoffJitter: 1})); err != nil {
		t.Errorf("valid policy rejected: %v", err)
	}
}

// attempts runs one SystemOne call in a synctest bubble and returns the
// request count, the virtual time the call took, and the error.
func attempts(t *testing.T, replies []reply, opts ...Option) (int, time.Duration, error) {
	t.Helper()
	var (
		n       int
		err     error
		elapsed time.Duration
	)
	synctest.Test(t, func(t *testing.T) {
		f := newFake(replies...)
		c := fakeClient(t, f, opts...)
		start := time.Now()
		_, err = c.SystemOne(bg, "x", spamQuestion)
		elapsed = time.Since(start)
		n = f.count()
	})
	return n, elapsed, err
}

func TestWhatRetries(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 599} {
		if n, _, _ := attempts(t, []reply{status(code)}); n != 3 {
			t.Errorf("status %d: %d attempts, want 3", code, n)
		}
	}
	for _, code := range []int{302, 400, 401, 403, 404, 409, 422} {
		if n, _, _ := attempts(t, []reply{status(code)}); n != 1 {
			t.Errorf("status %d: %d attempts, want 1", code, n)
		}
	}
	t.Run("eventual success", func(t *testing.T) {
		n, _, err := attempts(t, []reply{status(503), ok(okBody)})
		if n != 2 || err != nil {
			t.Errorf("n=%d err=%v", n, err)
		}
	})
	t.Run("HTTPStatuses replaces defaults", func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.HTTPStatuses = []int{400}
		if n, _, _ := attempts(t, []reply{status(400)}, WithRetryPolicy(p)); n != 3 {
			t.Errorf("400: %d attempts", n)
		}
		if n, _, _ := attempts(t, []reply{status(500)}, WithRetryPolicy(p)); n != 1 {
			t.Errorf("500: %d attempts", n)
		}
		p.HTTPStatuses = []int{}
		if n, _, _ := attempts(t, []reply{status(503)}, WithRetryPolicy(p)); n != 1 {
			t.Errorf("empty statuses: %d attempts", n)
		}
	})
	t.Run("Predicate adds", func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.Predicate = func(err error) bool {
			var apiErr *APIError
			return errors.As(err, &apiErr) && apiErr.StatusCode == 409
		}
		if n, _, _ := attempts(t, []reply{status(409)}, WithRetryPolicy(p)); n != 3 {
			t.Errorf("409: %d attempts", n)
		}
		if n, _, _ := attempts(t, []reply{status(503)}, WithRetryPolicy(p)); n != 3 {
			t.Errorf("503: %d attempts", n)
		}
	})
	t.Run("Predicate retries invalid 200 body", func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.Predicate = func(err error) bool {
			var ve *ResponseValidationError
			return errors.As(err, &ve)
		}
		n, _, err := attempts(t, []reply{ok(`{"model":1}`), ok(okBody)}, WithRetryPolicy(p))
		if n != 2 || err != nil {
			t.Errorf("n=%d err=%v", n, err)
		}
		if n, _, _ := attempts(t, []reply{ok(`{"model":1}`)}); n != 1 {
			t.Errorf("default policy retried a validation error: %d attempts", n)
		}
	})
	t.Run("HTTPStatuses with 200 retries validation errors", func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.HTTPStatuses = []int{200}
		if n, _, _ := attempts(t, []reply{ok(`{}`)}, WithRetryPolicy(p)); n != 3 {
			t.Errorf("%d attempts", n)
		}
	})
}

func TestTransportErrorRetries(t *testing.T) {
	run := func(t *testing.T, fail error, p RetryPolicy) (int, error) {
		var n int
		var err error
		synctest.Test(t, func(t *testing.T) {
			f := newFake(ok(okBody))
			f.err = func(*http.Request) error { return fail }
			c := fakeClient(t, f, WithRetryPolicy(p), WithTimeout(5*time.Second))
			_, err = c.SystemOne(bg, "x", spamQuestion)
			n = f.count()
		})
		return n, err
	}
	connFail := errors.New("connection reset")
	timeoutFail := fmt.Errorf("read: %w", errDeadline)

	n, err := run(t, connFail, DefaultRetryPolicy())
	var ce *ConnectionError
	if n != 3 || !errors.As(err, &ce) {
		t.Errorf("connection: n=%d err=%T %v", n, err, err)
	}
	n, err = run(t, timeoutFail, DefaultRetryPolicy())
	var te *TimeoutError
	if n != 3 || !errors.As(err, &te) || te.Duration != 5*time.Second {
		t.Errorf("timeout: n=%d err=%T %v", n, err, err)
	}
	p := DefaultRetryPolicy()
	p.DisableConnectionErrorRetry = true
	if n, _ := run(t, connFail, p); n != 1 {
		t.Errorf("DisableConnectionErrorRetry: %d attempts", n)
	}
	p = DefaultRetryPolicy()
	p.DisableTimeoutRetry = true
	if n, _ := run(t, timeoutFail, p); n != 1 {
		t.Errorf("DisableTimeoutRetry: %d attempts", n)
	}
}

// errDeadline is a socket-style deadline error, as a dialer or conn deadline
// in a user transport reports it.
var errDeadline = deadlineErr{}

type deadlineErr struct{}

func (deadlineErr) Error() string   { return "i/o timeout" }
func (deadlineErr) Timeout() bool   { return true }
func (deadlineErr) Temporary() bool { return true }

func TestMaxRetriesCounts(t *testing.T) {
	for retries, want := range map[int]int{0: 1, 1: 2, 4: 5} {
		p := DefaultRetryPolicy()
		p.MaxRetries = retries
		if n, _, _ := attempts(t, []reply{status(500)}, WithRetryPolicy(p)); n != want {
			t.Errorf("MaxRetries=%d: %d attempts, want %d", retries, n, want)
		}
	}
	p := RetryPolicy{MaxRetries: 2}
	n, elapsed, _ := attempts(t, []reply{status(500)}, WithRetryPolicy(p))
	if n != 3 || elapsed != 0 {
		t.Errorf("zero backoff: n=%d elapsed=%v", n, elapsed)
	}
}

func TestBackoff(t *testing.T) {
	p := DefaultRetryPolicy()
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, w := range want {
		if got := p.backoff(i+1, 0); got != w {
			t.Errorf("backoff(%d, 0) = %v, want %v", i+1, got, w)
		}
	}
	if got := p.backoff(1, 1); got != 375*time.Millisecond {
		t.Errorf("backoff(1, 1) = %v, want 375ms", got)
	}
	for range 100 {
		if got := p.wait(1, errors.New("x")); got < 375*time.Millisecond || got > 500*time.Millisecond {
			t.Fatalf("wait(1) = %v, outside [375ms, 500ms]", got)
		}
	}
	cases := []struct {
		name          string
		initial, maxD time.Duration
		jitter        float64
		n             int
		want          time.Duration
	}{
		{"cap on first attempt", 500 * time.Millisecond, 600 * time.Microsecond, 0, 1, 600 * time.Microsecond},
		{"zero initial", 0, time.Second, 0, 1, 0},
		{"zero max", time.Second, 0, 0, 1, 0},
		{"huge exponent", time.Nanosecond, time.Hour, 0.25, 2000, time.Hour},
		{"max int64", math.MaxInt64, math.MaxInt64, 0, 1, math.MaxInt64},
		{"max int64 late", math.MaxInt64, math.MaxInt64, 0, 64, math.MaxInt64},
	}
	for _, tc := range cases {
		p := RetryPolicy{BackoffInitial: tc.initial, BackoffMax: tc.maxD, BackoffJitter: tc.jitter}
		if got := p.backoff(tc.n, 0); got != tc.want {
			t.Errorf("%s: backoff = %v, want %v", tc.name, got, tc.want)
		}
		if got := p.backoff(tc.n, 0.999); got < 0 {
			t.Errorf("%s: negative backoff %v", tc.name, got)
		}
	}
}

func TestRetryAfterHonored(t *testing.T) {
	limited := reply{status: 429, body: `{}`, header: hdr("Retry-After", "3")}
	p := DefaultRetryPolicy()
	p.MaxRetries = 1
	n, elapsed, _ := attempts(t, []reply{limited}, WithRetryPolicy(p))
	if n != 2 || elapsed != 3*time.Second {
		t.Errorf("Retry-After: n=%d elapsed=%v, want 2 attempts after exactly 3s", n, elapsed)
	}
	ms := reply{status: 503, body: `{}`, header: hdr("Retry-After-Ms", "1234")}
	if _, elapsed, _ := attempts(t, []reply{ms}, WithRetryPolicy(p)); elapsed != 1234*time.Millisecond {
		t.Errorf("retry-after-ms: elapsed=%v", elapsed)
	}
	long := reply{status: 503, body: `{}`, header: hdr("Retry-After", "60")}
	unbudgeted := p
	unbudgeted.Timeout = 0
	if _, elapsed, _ := attempts(t, []reply{long}, WithRetryPolicy(unbudgeted)); elapsed != time.Minute {
		t.Errorf("long Retry-After capped: elapsed=%v", elapsed)
	}

	p.BackoffJitter = 0
	p.DisableRetryAfter = true
	if _, elapsed, _ := attempts(t, []reply{limited}, WithRetryPolicy(p)); elapsed != 500*time.Millisecond {
		t.Errorf("DisableRetryAfter: elapsed=%v, want 500ms backoff", elapsed)
	}
	p.DisableRetryAfter = false
	bad := reply{status: 503, body: `{}`, header: hdr("Retry-After", "soon")}
	if _, elapsed, _ := attempts(t, []reply{bad}, WithRetryPolicy(p)); elapsed != 500*time.Millisecond {
		t.Errorf("unparseable Retry-After: elapsed=%v, want 500ms backoff", elapsed)
	}
	overflow := reply{status: 503, body: `{}`, header: hdr("Retry-After", "9223372036.9")}
	if _, elapsed, _ := attempts(t, []reply{overflow}, WithRetryPolicy(p)); elapsed != 500*time.Millisecond {
		t.Errorf("out-of-range Retry-After: elapsed=%v, want 500ms backoff", elapsed)
	}
}

func TestRetryBudget(t *testing.T) {
	cases := []struct {
		budget, attempt, delay time.Duration
		want                   int
	}{
		{0, time.Second, 500 * time.Millisecond, 3},
		{30 * time.Second, 10 * time.Second, 5 * time.Second, 2},
		{2500 * time.Millisecond, 750 * time.Millisecond, 500 * time.Millisecond, 2},
		{2 * time.Second, time.Second, 0, 2},
		{time.Second, 0, time.Second, 1},
		{time.Second, 0, time.Minute, 1},
		{time.Second, 2 * time.Second, 0, 1}, // elapsed already beyond budget
	}
	for _, tc := range cases {
		name := fmt.Sprintf("budget=%v attempt=%v delay=%v", tc.budget, tc.attempt, tc.delay)
		t.Run(name, func(t *testing.T) {
			rep := reply{
				status: 503, body: `{}`, delay: tc.attempt,
				header: hdr("Retry-After-Ms", fmt.Sprint(tc.delay.Milliseconds())),
			}
			p := DefaultRetryPolicy()
			p.Timeout = tc.budget
			n, _, err := attempts(t, []reply{rep}, WithRetryPolicy(p), WithTimeout(time.Hour))
			if n != tc.want {
				t.Errorf("%d attempts, want %d (err %v)", n, tc.want, err)
			}
		})
	}
	t.Run("huge server delay stops without sleeping", func(t *testing.T) {
		rep := reply{status: 429, body: `{}`, header: hdr("Retry-After", "9223372035")}
		p := DefaultRetryPolicy()
		p.Timeout = time.Second
		n, elapsed, err := attempts(t, []reply{rep}, WithRetryPolicy(p))
		var apiErr *APIError
		if n != 1 || elapsed != 0 || !errors.As(err, &apiErr) {
			t.Errorf("n=%d elapsed=%v err=%v", n, elapsed, err)
		}
	})
	t.Run("default budget is 30s and resets per call", func(t *testing.T) {
		if DefaultRetryPolicy().Timeout != 30*time.Second {
			t.Fatal("default budget changed")
		}
		synctest.Test(t, func(t *testing.T) {
			rep := reply{status: 503, body: `{}`, delay: 10 * time.Second, header: hdr("Retry-After", "5")}
			f := newFake(rep)
			c := fakeClient(t, f, WithTimeout(time.Hour))
			for call := 1; call <= 2; call++ {
				before := f.count()
				_, _ = c.SystemOne(bg, "x", spamQuestion)
				if n := f.count() - before; n != 2 {
					t.Errorf("call %d: %d attempts, want 2", call, n)
				}
			}
		})
	})
	t.Run("budget does not interrupt an attempt", func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.Timeout = time.Second
		n, elapsed, err := attempts(t, []reply{{status: 200, body: okBody, delay: 5 * time.Second}}, WithRetryPolicy(p))
		if n != 1 || err != nil || elapsed != 5*time.Second {
			t.Errorf("n=%d err=%v elapsed=%v", n, err, elapsed)
		}
	})
}

func TestCallerCancellation(t *testing.T) {
	t.Run("mid-sleep", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFake(reply{status: 429, body: `{}`, header: hdr("Retry-After", "20")})
			c := fakeClient(t, f)
			ctx, cancel := context.WithCancel(bg)
			done := make(chan error)
			go func() {
				_, err := c.SystemOne(ctx, "x", spamQuestion)
				done <- err
			}()
			synctest.Wait() // the call is now sleeping before its retry
			start := time.Now()
			cancel()
			err := <-done
			if !errors.Is(err, context.Canceled) || f.count() != 1 || time.Since(start) != 0 {
				t.Errorf("err=%v attempts=%d", err, f.count())
			}
		})
	})
	t.Run("during attempt", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFake(reply{status: 200, body: okBody, delay: time.Minute})
			c := fakeClient(t, f)
			ctx, cancel := context.WithTimeout(bg, time.Second)
			defer cancel()
			_, err := c.SystemOne(ctx, "x", spamQuestion)
			var ce *ConnectionError
			if !errors.As(err, &ce) || !errors.Is(err, context.DeadlineExceeded) || f.count() != 1 {
				t.Errorf("err=%T %v attempts=%d", err, err, f.count())
			}
		})
	})
	t.Run("already cancelled", func(t *testing.T) {
		f := newFake(ok(okBody))
		c := fakeClient(t, f)
		ctx, cancel := context.WithCancel(bg)
		cancel()
		_, err := c.SystemOne(ctx, "x", spamQuestion)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestOperationDeadline(t *testing.T) {
	t.Run("caller deadline bounds attempts and waits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFake(reply{status: 200, body: okBody, delay: time.Hour})
			c := fakeClient(t, f, WithTimeout(2*time.Second))
			ctx, cancel := context.WithTimeout(bg, 3*time.Second)
			defer cancel()
			start := time.Now()
			_, err := c.SystemOne(ctx, "x", spamQuestion)
			if elapsed := time.Since(start); elapsed != 3*time.Second {
				t.Errorf("elapsed = %v, want 3s", elapsed)
			}
			if !errors.Is(err, context.DeadlineExceeded) || f.count() != 2 {
				t.Errorf("err=%v attempts=%d, want 2 (attempt timeout, then cut by caller)", err, f.count())
			}
		})
	})
	t.Run("without per-attempt timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clearEnv(t)
			f := newFake(reply{status: 200, body: okBody, delay: time.Hour})
			c, _ := New(WithAPIKey(testKey), WithBaseURL("https://api.test"), WithHTTPClient(&http.Client{Transport: f}))
			ctx, cancel := context.WithTimeout(bg, time.Second)
			defer cancel()
			start := time.Now()
			_, err := c.SystemOne(ctx, "x", spamQuestion)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Second || f.count() != 1 {
				t.Errorf("err=%v elapsed=%v attempts=%d", err, time.Since(start), f.count())
			}
		})
	})
}

func TestRetryCountHeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(status(500))
		c := fakeClient(t, f, WithHeader("X-TypeSafe-Retry-Count", "99"))
		_, _ = c.SystemOne(bg, "x", spamQuestion, WithHeader("x-typesafe-retry-count", "7"))
		var got []string
		for _, r := range f.all() {
			got = append(got, strings.Join(r.Header.Values("X-TypeSafe-Retry-Count"), ","))
		}
		if strings.Join(got, "|") != "|1|2" {
			t.Errorf("retry headers = %q, want none, 1, 2", got)
		}
	})
}

func TestConcurrentCallsAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		perCall := map[string][]string{}
		f := newFake(status(503))
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			id := r.Header.Get("X-Call")
			perCall[id] = append(perCall[id], r.Header.Get("X-TypeSafe-Retry-Count"))
			mu.Unlock()
			return f.RoundTrip(r)
		})
		c := fakeClient(t, rt)
		var wg sync.WaitGroup
		for i := range 5 {
			wg.Go(func() {
				p := DefaultRetryPolicy()
				p.MaxRetries = i
				_, _ = c.SystemOne(bg, "x", spamQuestion, WithHeader("X-Call", fmt.Sprint(i)), WithRetryPolicy(p))
			})
		}
		wg.Wait()
		for i := range 5 {
			got := perCall[fmt.Sprint(i)]
			if len(got) != i+1 {
				t.Errorf("call %d: %d attempts, want %d (policy bled?)", i, len(got), i+1)
				continue
			}
			for n, v := range got {
				if want := map[bool]string{true: "", false: fmt.Sprint(n)}[n == 0]; v != want {
					t.Errorf("call %d attempt %d: retry header %q", i, n, v)
				}
			}
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPerCallPolicyOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(status(500))
		c := fakeClient(t, f, WithRetryPolicy(RetryPolicy{MaxRetries: 3}))
		_, _ = c.SystemOne(bg, "x", spamQuestion, WithRetryPolicy(RetryPolicy{}))
		if f.count() != 1 {
			t.Errorf("override: %d attempts, want 1", f.count())
		}
		_, _ = c.SystemOne(bg, "x", spamQuestion)
		if f.count() != 5 {
			t.Errorf("client policy after override: %d total attempts, want 5", f.count())
		}
	})
}

func TestExhaustionReturnsLastError(t *testing.T) {
	replies := []reply{
		{status: 500, body: `{"error":"first"}`, header: hdr("X-Typesafe-Request-Id", "req-1")},
		{status: 503, body: `{"error":"second"}`, header: hdr("X-Typesafe-Request-Id", "req-2")},
		{status: 502, body: `{"error":"third"}`, header: hdr("X-Typesafe-Request-Id", "req-3")},
	}
	n, _, err := attempts(t, replies)
	var apiErr *APIError
	if n != 3 || !errors.As(err, &apiErr) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if apiErr.StatusCode != 502 || apiErr.Message != "third" || apiErr.RequestID() != "req-3" || apiErr.Endpoint != "POST https://api.test/v1/systemone" {
		t.Errorf("last error = %v", apiErr)
	}
}

func TestRetryOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(status(503))
		c := fakeClient(t, f, WithRetryPolicy(RetryPolicy{MaxRetries: 0}))
		for range 3 { // the application's own retry loop
			_, _ = c.SystemOne(bg, "x", spamQuestion)
		}
		if f.count() != 3 {
			t.Errorf("%d requests for 3 application attempts", f.count())
		}
	})
}
