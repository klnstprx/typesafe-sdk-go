package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAPIKeyResolution(t *testing.T) {
	t.Run("env trimmed", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(APIKeyEnv, "  "+testKey+"\n")
		c, err := New()
		if err != nil || c.cfg.apiKey != testKey {
			t.Fatalf("New() = %v, key %q", err, c.cfg.apiKey)
		}
	})
	t.Run("explicit wins and is trimmed", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(APIKeyEnv, "sk-env")
		c, err := New(WithAPIKey(" sk-explicit "))
		if err != nil || c.cfg.apiKey != "sk-explicit" {
			t.Fatalf("New() = %v, key %q", err, c.cfg.apiKey)
		}
	})
	t.Run("missing names env var", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(APIKeyEnv, "   ")
		_, err := New()
		if err == nil || !strings.Contains(err.Error(), APIKeyEnv) {
			t.Fatalf("New() error = %v", err)
		}
	})
	t.Run("invalid explicit does not fall back", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(APIKeyEnv, "sk-env")
		if _, err := New(WithAPIKey("")); err == nil {
			t.Fatal("New(WithAPIKey(\"\")) succeeded")
		}
	})
	for name, key := range map[string]string{
		"control char": "sk-\x01abc",
		"inner space":  "sk-abc def",
		"tab":          "sk-abc\tdef",
		"non-ascii":    "sk-abcé",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			clearEnv(t)
			_, err := New(WithAPIKey(key))
			if err == nil {
				t.Fatal("New succeeded")
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "sk-") {
				t.Errorf("error echoes the key: %v", err)
			}
		})
	}
}

func TestBaseURLResolution(t *testing.T) {
	cases := []struct {
		name, env string
		opts      []Option
		want      string
	}{
		{"default", "", nil, DefaultBaseURL},
		{"whitespace env ignored", "   ", nil, DefaultBaseURL},
		{"env trimmed", "  https://env.test/  ", nil, "https://env.test"},
		{"explicit wins", "https://env.test", []Option{WithBaseURL(" https://x.test/api/// ")}, "https://x.test/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(BaseURLEnv, tc.env)
			c, err := New(append([]Option{WithAPIKey(testKey)}, tc.opts...)...)
			if err != nil || c.cfg.baseURL.String() != tc.want {
				t.Fatalf("New() = %v, baseURL %v; want %q", err, c.cfg.baseURL, tc.want)
			}
		})
	}
	t.Run("prefix kept in request path", func(t *testing.T) {
		f := newFake(ok(okBody))
		c := fakeClient(t, f, WithBaseURL("https://api.test/prefix/"))
		if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
			t.Fatal(err)
		}
		if got := f.all()[0].Path; got != "/prefix/v1/systemone" {
			t.Errorf("path = %q", got)
		}
	})
	for name, base := range map[string]string{
		"relative":       "api.test",
		"query":          "https://api.test/prefix?tenant=a",
		"empty query":    "https://api.test/prefix?",
		"fragment":       "https://api.test/prefix#frag",
		"query with key": "https://user:pw@api.test/?k=" + testKey,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			clearEnv(t)
			_, err := New(WithAPIKey(testKey), WithBaseURL(base))
			if err == nil {
				t.Fatal("New succeeded")
			}
			if strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), testKey) {
				t.Errorf("error echoes the URL: %v", err)
			}
		})
	}
}

func TestModelPrecedence(t *testing.T) {
	model := func(t *testing.T, env string, clientOpts []Option, callOpts ...CallOption) string {
		t.Helper()
		clearEnv(t)
		t.Setenv(DefaultModelEnv, env)
		f := newFake(ok(okBody))
		c, err := New(append([]Option{WithAPIKey(testKey), WithBaseURL("https://api.test"), WithHTTPClient(&http.Client{Transport: f})}, clientOpts...)...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.SystemOne(bg, "x", spamQuestion, callOpts...); err != nil {
			t.Fatal(err)
		}
		var body struct{ Model string }
		if err := json.Unmarshal(f.all()[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		return body.Model
	}
	cases := []struct {
		name     string
		env      string
		client   []Option
		call     []CallOption
		expected string
	}{
		{"default", "", nil, nil, DefaultModel},
		{"env", " env-model ", nil, nil, "env-model"},
		{"client", "env-model", []Option{WithModel("client-model")}, nil, "client-model"},
		{"call", "env-model", []Option{WithModel("client-model")}, []CallOption{WithModel("call-model")}, "call-model"},
		{"extra body", "env-model", []Option{WithModel("client-model")}, []CallOption{WithModel("call-model"), WithExtraBody(map[string]any{"model": "extra-model"})}, "extra-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := model(t, tc.env, tc.client, tc.call...); got != tc.expected {
				t.Errorf("model = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestTimeoutValidation(t *testing.T) {
	clearEnv(t)
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := New(WithAPIKey(testKey), WithTimeout(d)); err == nil {
			t.Errorf("New(WithTimeout(%v)) succeeded", d)
		}
		f := newFake(ok(okBody))
		c := fakeClient(t, f)
		if _, err := c.SystemOne(bg, "x", spamQuestion, WithTimeout(d)); err == nil {
			t.Errorf("SystemOne(WithTimeout(%v)) succeeded", d)
		}
		if f.count() != 0 {
			t.Errorf("request sent despite invalid timeout")
		}
	}
	c, _ := New(WithAPIKey(testKey))
	if c.cfg.timeout != DefaultTimeout || DefaultTimeout != 10*time.Second {
		t.Errorf("default timeout = %v", c.cfg.timeout)
	}
}

func TestPerCallTimeoutAppliesToThatCallOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(reply{status: 200, body: okBody, delay: 2 * time.Second})
		c := fakeClient(t, f, noRetry(), WithTimeout(5*time.Second))
		_, err := c.SystemOne(bg, "x", spamQuestion, WithTimeout(time.Second))
		var te *TimeoutError
		if !errors.As(err, &te) || te.Duration != time.Second {
			t.Fatalf("err = %v, want 1s timeout", err)
		}
		if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
			t.Fatalf("second call: %v", err)
		}
	})
}

func TestSuppliedHTTPClientTimeouts(t *testing.T) {
	slow := func() *fakeTransport { return newFake(reply{status: 200, body: okBody, delay: 2 * time.Second}) }
	newClient := func(t *testing.T, hc *http.Client, opts ...Option) *Client {
		clearEnv(t)
		c, err := New(append([]Option{WithAPIKey(testKey), WithBaseURL("https://api.test"), WithHTTPClient(hc), noRetry()}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Run("SDK timeout wins over client timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hc := &http.Client{Timeout: time.Second, Transport: slow()}
			c := newClient(t, hc, WithTimeout(10*time.Second))
			if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	})
	t.Run("client timeout inherited", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hc := &http.Client{Timeout: time.Second, Transport: slow()}
			c := newClient(t, hc)
			start := time.Now()
			_, err := c.SystemOne(bg, "x", spamQuestion)
			var te *TimeoutError
			if !errors.As(err, &te) || te.Duration != time.Second {
				t.Fatalf("err = %v, want *TimeoutError{1s}", err)
			}
			if elapsed := time.Since(start); elapsed != time.Second {
				t.Errorf("timed out after %v", elapsed)
			}
		})
	})
	t.Run("zero client timeout means no deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hc := &http.Client{Transport: newFake(reply{status: 200, body: okBody, delay: time.Hour})}
			c := newClient(t, hc)
			if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	})
	t.Run("supplied client not mutated", func(t *testing.T) {
		rt := slow()
		redirect := func(*http.Request, []*http.Request) error { return nil }
		hc := &http.Client{Timeout: 3 * time.Second, Transport: rt, CheckRedirect: redirect}
		c := newClient(t, hc, WithTimeout(time.Minute))
		_ = c.Close()
		if hc.Timeout != 3*time.Second || hc.Transport != rt || reflect.ValueOf(hc.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
			t.Errorf("supplied client changed: %+v", hc)
		}
	})
}

type closeRecorder struct {
	http.RoundTripper
	closes atomic.Int32
}

func (c *closeRecorder) CloseIdleConnections() { c.closes.Add(1) }

func TestCloseOwnership(t *testing.T) {
	t.Run("supplied transport untouched", func(t *testing.T) {
		rt := &closeRecorder{RoundTripper: newFake(ok(okBody))}
		c := fakeClient(t, rt)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if n := rt.closes.Load(); n != 0 {
			t.Errorf("CloseIdleConnections called %d times on a supplied transport", n)
		}
	})
	t.Run("owned transport closed, idempotent, calls fail", func(t *testing.T) {
		c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, okBody) })
		if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
			t.Fatal(err)
		}
		if c.owned == nil {
			t.Fatal("owned client has no owned transport")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := c.SystemOne(bg, "x", spamQuestion)
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Errorf("call after Close: %v", err)
		}
		_, err = c.Models.List(bg)
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Errorf("List after Close: %v", err)
		}
	})
}

func TestSafeFormatting(t *testing.T) {
	c := fakeClient(t, newFake(ok(okBody)), WithHeader("X-Api-Key", "header-secret"))
	for _, s := range []string{
		fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprint(c),
		fmt.Sprintf("%v", *c.cfg), fmt.Sprintf("%+v", *c.cfg), fmt.Sprintf("%#v", c.cfg),
		fmt.Sprintf("%+v", c.Models), fmt.Sprintf("%#v", c.Models),
	} {
		if strings.Contains(s, testKey) || strings.Contains(s, "header-secret") || strings.Contains(s, "Bearer") {
			t.Errorf("formatted client leaks a credential: %s", s)
		}
	}
	if c.String() != "typesafe.Client" {
		t.Errorf("String() = %q", c.String())
	}
}
