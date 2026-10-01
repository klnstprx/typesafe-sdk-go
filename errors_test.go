package typesafe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAPIErrorFromResponse(t *testing.T) {
	c := fakeClient(t, newFake(reply{
		status: 429,
		body:   `{"error":"Rate limit exceeded"}`,
		header: hdr("X-Typesafe-Request-Id", "req-1", "Retry-After", "7"),
	}), noRetry())
	_, err := c.SystemOne(bg, "text", spamQuestion)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want *APIError", err, err)
	}
	want := "POST https://api.test/v1/systemone: 429 Rate limit exceeded (request_id=req-1)"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if apiErr.StatusCode != 429 || apiErr.Message != "Rate limit exceeded" || apiErr.RequestID() != "req-1" {
		t.Errorf("got status=%d message=%q request_id=%q", apiErr.StatusCode, apiErr.Message, apiErr.RequestID())
	}
	if body, ok := apiErr.Body.(map[string]any); !ok || body["error"] != "Rate limit exceeded" {
		t.Errorf("Body = %#v", apiErr.Body)
	}
	if string(apiErr.RawBody) != `{"error":"Rate limit exceeded"}` {
		t.Errorf("RawBody = %q", apiErr.RawBody)
	}
	if d, ok := apiErr.RetryAfter(); !ok || d != 7*time.Second {
		t.Errorf("RetryAfter() = %v, %v", d, ok)
	}
}

func TestHTTPStatusCodeInterface(t *testing.T) {
	type statusCoder interface{ HTTPStatusCode() int }
	cases := []struct {
		name  string
		reply reply
		want  int
	}{
		{"api error", status(429), 429},
		{"validation error", ok(`{"model":1}`), 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, newFake(tc.reply), noRetry())
			_, err := c.SystemOne(bg, "text", spamQuestion)
			wrapped := fmt.Errorf("feature failed: %w", err)
			var sc statusCoder
			if !errors.As(wrapped, &sc) || sc.HTTPStatusCode() != tc.want {
				t.Fatalf("errors.As(%v) status = %v, want %d", wrapped, sc, tc.want)
			}
		})
	}
}

func TestErrorMessageExtraction(t *testing.T) {
	long := strings.Repeat("x", 250)
	cases := []struct {
		name, body, want string
	}{
		{"error string", `{"error":"bad","message":"m"}`, "bad"},
		{"error.message", `{"error":{"message":"nested"},"message":"m"}`, "nested"},
		{"message", `{"message":"msg","detail":"d"}`, "msg"},
		{"detail string", `{"detail":"d"}`, "d"},
		{"detail.message", `{"detail":{"message":"dm"}}`, "dm"},
		{"detail list", `{"detail":[{"loc":["body","questions","q","score","criteria",0],"msg":"Invalid"},{"loc":["body"],"msg":"Missing"},"junk",{"loc":"x"}]}`, "questions.q.score.criteria.0: Invalid; Missing"},
		{"plain text not truncated", long, long},
		{"json fallback truncated", `{"data":"` + long + `"}`, (`{"data":"` + long)[:200] + "…"},
		{"empty body", ``, "status code (no body)"},
		{"empty array", `[]`, "[]"},
		{"number", `42`, "42"},
		{"empty error string dumps json", `{"error":""}`, `{"error":""}`},
		{"compact json dump", `{ "code" : 7 }`, `{"code":7}`},
		{"invalid utf-8", "bad \xff byte", "bad � byte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, newFake(reply{status: 400, body: tc.body}), noRetry())
			_, err := c.SystemOne(bg, "text", spamQuestion)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v", err)
			}
			if apiErr.Message != tc.want {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.want)
			}
		})
	}
}

func TestJSONNullBodyIsNoBody(t *testing.T) {
	// A JSON null decodes to a nil body, which reads as "no body".
	c := fakeClient(t, newFake(reply{status: 500, body: `null`}), noRetry())
	_, err := c.SystemOne(bg, "text", spamQuestion)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "status code (no body)" || apiErr.Body != nil {
		t.Fatalf("err = %#v", err)
	}
}

func TestEndpointStripsCredentialsQueryAndFragment(t *testing.T) {
	c := fakeClient(t, newFake(status(404)), noRetry(), WithBaseURL("https://user:pw@api.test/prefix"))
	_, err := c.SystemOne(bg, "text", spamQuestion)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if apiErr.Endpoint != "POST https://api.test/prefix/v1/systemone" {
		t.Errorf("Endpoint = %q", apiErr.Endpoint)
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("error leaks userinfo: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	cases := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{"absent", hdr(), 0, false},
		{"seconds", hdr("Retry-After", "2"), 2 * time.Second, true},
		{"fractional seconds", hdr("Retry-After", "0.25"), 250 * time.Millisecond, true},
		{"bad", hdr("Retry-After", "bad"), 0, false},
		{"negative", hdr("Retry-After", "-1"), 0, false},
		{"empty", hdr("Retry-After", ""), 0, true},
		{"huge", hdr("Retry-After", "1e308"), 0, false},
		{"beyond duration range", hdr("Retry-After", "9223372036.9"), 0, false},
		{"ms", hdr("Retry-After-Ms", "150"), 150 * time.Millisecond, true},
		{"ms wins", hdr("Retry-After-Ms", "150", "Retry-After", "3"), 150 * time.Millisecond, true},
		{"ms inf falls through", hdr("Retry-After-Ms", "inf", "Retry-After", "3"), 3 * time.Second, true},
		{"ms nan falls through", hdr("Retry-After-Ms", "NaN", "Retry-After", "3"), 3 * time.Second, true},
		{"ms negative falls through", hdr("Retry-After-Ms", "-1", "Retry-After", "3"), 3 * time.Second, true},
		{"ms bad falls through", hdr("Retry-After-Ms", "bad", "Retry-After", "3"), 3 * time.Second, true},
		{"hex float rejected", hdr("Retry-After", "0x1p4"), 0, false},
		{"past date", hdr("Retry-After", past), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.header)
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseRetryAfter = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	t.Run("future date", func(t *testing.T) {
		got, ok := parseRetryAfter(hdr("Retry-After", future))
		if !ok || got <= 59*time.Minute || got > time.Hour {
			t.Errorf("parseRetryAfter = %v, %v; want about 1h", got, ok)
		}
	})
}

func TestTimeoutErrorIdentity(t *testing.T) {
	err := &TimeoutError{Duration: time.Second, Err: errors.New("socket deadline")}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("TimeoutError is not context.DeadlineExceeded")
	}
	if !err.Timeout() || !err.Temporary() {
		t.Error("TimeoutError does not report Timeout/Temporary")
	}
	if got := err.Error(); got != "typesafe: request timed out (timeout=1s): socket deadline" {
		t.Errorf("Error() = %q", got)
	}
}
