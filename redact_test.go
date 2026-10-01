package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

// chainMessages returns the message of err and of every error reachable
// through Unwrap.
func chainMessages(err error) []string {
	if err == nil {
		return nil
	}
	msgs := []string{err.Error()}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		msgs = append(msgs, chainMessages(u.Unwrap())...)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			msgs = append(msgs, chainMessages(e)...)
		}
	}
	return msgs
}

func failingClient(t *testing.T, fail func(*http.Request) error, opts ...Option) *Client {
	t.Helper()
	f := newFake(ok(okBody))
	f.err = fail
	return fakeClient(t, f, append([]Option{noRetry()}, opts...)...)
}

func TestTransportErrorsAreRedacted(t *testing.T) {
	quotedJSON, _ := json.Marshal(testKey + "\"")
	cases := map[string]func(*http.Request) error{
		"raw header":   func(r *http.Request) error { return fmt.Errorf("dial failed: %s", r.Header.Get("Authorization")) },
		"go quoted":    func(r *http.Request) error { return fmt.Errorf("dial failed: %q", r.Header.Get("Authorization")) },
		"json escaped": func(*http.Request) error { return fmt.Errorf("dial failed: %s", quotedJSON) },
		"wrapped":      func(*http.Request) error { return fmt.Errorf("outer: %w", fmt.Errorf("inner %s", testKey)) },
		"custom header": func(r *http.Request) error {
			return fmt.Errorf("proxy said %s", r.Header.Get("X-Session-Token"))
		},
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			c := failingClient(t, fail, WithHeader("X-Session-Token", "tok-secret-value"))
			_, err := c.SystemOne(bg, "text", spamQuestion)
			var connErr *ConnectionError
			if !errors.As(err, &connErr) {
				t.Fatalf("err = %T %v", err, err)
			}
			for _, msg := range chainMessages(err) {
				if strings.Contains(msg, testKey) || strings.Contains(msg, "tok-secret-value") {
					t.Errorf("chain message leaks a secret: %q", msg)
				}
			}
			if !strings.Contains(err.Error(), "***") {
				t.Errorf("Error() = %q, want a *** mask", err.Error())
			}
			if s := fmt.Sprintf("%v %+v %#v", err, err, err); strings.Contains(s, testKey) {
				t.Errorf("formatted error leaks the key: %s", s)
			}
		})
	}
}

func TestQuotedSecretIsRedacted(t *testing.T) {
	// A key containing a quote character is escaped differently by %q and JSON.
	clearEnv(t)
	key := `sk-"quoted"-key`
	f := newFake(ok(okBody))
	f.err = func(r *http.Request) error {
		j, _ := json.Marshal(key)
		return fmt.Errorf("a=%q b=%s", key, j)
	}
	c, err := New(WithAPIKey(key), WithBaseURL("https://api.test"), WithHTTPClient(&http.Client{Transport: f}), noRetry())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SystemOne(bg, "text", spamQuestion)
	inner := strings.Trim(strconv.Quote(key), `"`)
	if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), inner) {
		t.Errorf("Error() leaks the key: %q", err.Error())
	}
}

func TestSecretInNonInnerFieldReplacesWholeError(t *testing.T) {
	c := failingClient(t, func(*http.Request) error { return &net.OpError{Op: testKey, Net: "tcp", Err: io.EOF} })
	_, err := c.SystemOne(bg, "text", spamQuestion)
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		t.Errorf("leaking *net.OpError is still reachable: %+v", opErr)
	}
	if !errors.Is(err, io.EOF) {
		t.Error("errors.Is(err, io.EOF) = false after redaction")
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), testKey) {
		t.Error("formatted error leaks the key")
	}
}

func TestCleanErrorChainIsPreserved(t *testing.T) {
	c := failingClient(t, func(*http.Request) error { return &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF} })
	_, err := c.SystemOne(bg, "text", spamQuestion)
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		t.Errorf("errors.As(*net.OpError) failed for a clean error: %v", err)
	}
}

func TestRedactedTimeoutKeepsIdentity(t *testing.T) {
	c := failingClient(t, func(r *http.Request) error {
		return fmt.Errorf("deadline for %s: %w", r.Header.Get("Authorization"), os.ErrDeadlineExceeded)
	})
	_, err := c.SystemOne(bg, "text", spamQuestion)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %T %v, want *TimeoutError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("errors.Is deadline checks failed: %v", err)
	}
	var netErr net.Error
	if !errors.As(timeoutErr.Err, &netErr) || !netErr.Timeout() {
		t.Error("redacted cause does not satisfy net.Error with Timeout()")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("Error() leaks the key: %q", err.Error())
	}
}

func TestClientSideErrorsNeverContainKey(t *testing.T) {
	clearEnv(t)
	bad := testKey + " with space"
	if _, err := New(WithAPIKey(bad)); err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("New error = %v", err)
	}
	c := fakeClient(t, newFake(ok(okBody)))
	errs := []error{}
	_, err := c.SystemOne(bg, nil, spamQuestion)
	errs = append(errs, err)
	_, err = c.SystemOne(bg, "x", nil)
	errs = append(errs, err)
	_, err = c.SystemOne(bg, "x", spamQuestion, WithTimeout(-1))
	errs = append(errs, err)
	for _, err := range errs {
		if err == nil || strings.Contains(err.Error(), testKey) {
			t.Errorf("client-side error = %v", err)
		}
	}
}

func TestCookieJarAndURLCredentialsAreRedacted(t *testing.T) {
	const session = "sess-secret-token-123"
	jar, _ := cookiejar.New(nil)
	jarURL, _ := url.Parse("https://api.test/")
	jar.SetCookies(jarURL, []*http.Cookie{{Name: "session", Value: session}})

	cases := map[string]struct {
		base   string
		fail   func(*http.Request) error
		secret []string
	}{
		"cookie header": {"https://api.test", func(r *http.Request) error {
			return fmt.Errorf("proxy rejected cookie %s", r.Header.Get("Cookie"))
		}, []string{session}},
		"cookie value only": {"https://api.test", func(r *http.Request) error {
			c, _ := r.Cookie("session")
			return fmt.Errorf("bad session %q", c.Value)
		}, []string{session}},
		"url password": {"https://alice:pw-secret-42@api.test", func(r *http.Request) error {
			return fmt.Errorf("dial %s failed", r.URL)
		}, []string{"pw-secret-42"}},
		"url password escaped": {"https://alice:p%40ss-secret@api.test", func(r *http.Request) error {
			pw, _ := r.URL.User.Password()
			return fmt.Errorf("dial %s (password %s) failed", r.URL, pw)
		}, []string{"p@ss-secret", "p%40ss-secret"}},
		"url token username": {"https://tok-user-secret@api.test", func(r *http.Request) error {
			return fmt.Errorf("dial %s failed", r.URL)
		}, []string{"tok-user-secret"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			f := newFake(ok(okBody))
			f.err = tc.fail
			c, err := New(WithAPIKey(testKey), WithBaseURL(tc.base), noRetry(),
				WithHTTPClient(&http.Client{Transport: f, Jar: jar}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.SystemOne(bg, "x", spamQuestion)
			if err == nil {
				t.Fatal("call succeeded")
			}
			for _, msg := range chainMessages(err) {
				for _, s := range tc.secret {
					if strings.Contains(msg, s) {
						t.Errorf("chain message leaks %q: %q", s, msg)
					}
				}
			}
			if !strings.Contains(err.Error(), "***") {
				t.Errorf("Error() = %q, want a *** mask", err.Error())
			}
		})
	}
}
