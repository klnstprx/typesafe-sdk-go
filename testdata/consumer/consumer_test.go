package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klnstprx/typesafe-sdk-go"
)

type ctxKey struct{}

// recorder is an application-side slog.Handler that keeps correlation IDs.
type recorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recorder) WithGroup(string) slog.Handler            { return r }
func (r *recorder) Handle(ctx context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, fmt.Sprintf("%s tid=%v", rec.Message, ctx.Value(ctxKey{})))
	return nil
}

// tracing is an application-side transport wrapper, as used for metrics.
type tracing struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (t *tracing) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.base.RoundTrip(r)
}

func server(t *testing.T, status int, body string, delay time.Duration) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-release: // the test is over
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-TypeSafe-Request-Id", "req-c")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait out delay
	return srv.URL
}

func checker(t *testing.T, url string) (*Checker, *recorder, *tracing) {
	t.Helper()
	rec := &recorder{}
	tr := &tracing{base: http.DefaultTransport}
	c, err := NewChecker(url, &http.Client{Transport: tr}, slog.New(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec, tr
}

func TestGenericDecodingMetadataAndLogging(t *testing.T) {
	c, rec, tr := checker(t, server(t, 200, `{"model":"jev-1","usage":{},"answers":{"spam":{"type":"noul","noul":0}}}`, 0))
	ctx := context.WithValue(context.Background(), ctxKey{}, "tid-7")
	v, err := c.Check(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if v.Spam || v.Score != 0 || v.RequestID != "req-c" {
		t.Errorf("verdict = %+v; a zero score must stay valid", v)
	}
	if tr.calls.Load() != 1 {
		t.Errorf("tracing transport saw %d calls", tr.calls.Load())
	}
	if len(rec.msgs) != 1 || rec.msgs[0] != "typesafe.attempt tid=tid-7" {
		t.Errorf("log records = %q", rec.msgs)
	}
}

func TestMetadataAccessorsAndSerialization(t *testing.T) {
	body := `{"model":"jev-1","usage":{"input_tokens":2},"answers":{"spam":{"type":"noul","noul":0.75},"x":{"type":"future"}}}`
	c, err := typesafe.New(typesafe.WithAPIKey("sk-consumer"), typesafe.WithBaseURL(server(t, 200, body, 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := c.SystemOne(context.Background(), map[string]any{"msg": "hi"}, typesafe.Questions{"spam": typesafe.Noul{}})
	if err != nil {
		t.Fatal(err)
	}
	spam, ok := resp.Answers["spam"].(typesafe.NoulAnswer)
	if !ok || spam.Noul != 0.75 || len(resp.Answers) != 1 {
		t.Fatalf("answers = %#v", resp.Answers)
	}
	if string(resp.RawJSON()) != body || resp.Header().Get("X-TypeSafe-Request-Id") != "req-c" {
		t.Error("metadata accessors")
	}
	raw, _ := io.ReadAll(resp.RawHTTPResponse().Body)
	if string(raw) != body {
		t.Errorf("RawHTTPResponse body = %s", raw)
	}
	out, err := json.Marshal(resp)
	if err != nil || !strings.Contains(string(out), `"spam":{"type":"noul","noul":0.75}`) {
		t.Errorf("json.Marshal = %s, %v", out, err)
	}
	if fmt.Sprint(c) != "typesafe.Client" || strings.Contains(fmt.Sprintf("%#v", c), "sk-consumer") {
		t.Error("client formatting")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing answer": `{"model":"m","answers":{}}`,
		"null noul":      `{"model":"m","answers":{"spam":{"type":"noul","noul":null}}}`,
		"wrong type":     `{"model":"m","answers":{"spam":{"type":"choice","noul":1}}}`,
		"mistyped value": `{"model":"m","answers":{"spam":{"type":"noul","noul":"high"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, _ := checker(t, server(t, 200, body, 0))
			_, err := c.Check(context.Background(), "hello")
			var ve *typesafe.ResponseValidationError
			if !errors.As(err, &ve) || ve.StatusCode != 200 || ve.RequestID() != "req-c" {
				t.Fatalf("err = %v", err)
			}
			if Status(fmt.Errorf("wrapped: %w", err)) != 200 {
				t.Errorf("Status = %d", Status(err))
			}
		})
	}
	t.Run("field path", func(t *testing.T) {
		c, _, _ := checker(t, server(t, 200, cases["mistyped value"], 0))
		_, err := c.Check(context.Background(), "hello")
		var ve *typesafe.ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "answers.spam.noul" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAPIErrorsTimeoutsAndMisuse(t *testing.T) {
	c, _, tr := checker(t, server(t, 429, `{"error":"Rate limit exceeded"}`, 0))
	_, err := c.Check(context.Background(), "hello")
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "Rate limit exceeded" || Status(err) != 429 {
		t.Fatalf("err = %v", err)
	}
	if tr.calls.Load() != 1 {
		t.Errorf("SDK retried despite MaxRetries 0: %d calls", tr.calls.Load())
	}

	slow, _, _ := checker(t, server(t, 200, `{}`, time.Minute))
	_, err = slow.Check(context.Background(), "hello")
	var te *typesafe.TimeoutError
	if !errors.As(err, &te) || te.Duration != 2*time.Second || Status(err) != 504 {
		t.Fatalf("err = %v", err)
	}

	if _, err := typesafe.SystemOneAs[spamResponse](context.Background(), c.client, nil, typesafe.Questions{"q": typesafe.Noul{}}); err == nil || !strings.HasPrefix(err.Error(), "typesafe: ") {
		t.Errorf("misuse err = %v", err)
	}
}
