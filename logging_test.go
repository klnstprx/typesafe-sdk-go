package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
	ctx   context.Context
}

type recordingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler            { return h }
func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, capturedRecord{r.Level, r.Message, attrs, ctx})
	return nil
}

func (h *recordingHandler) all() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedRecord(nil), h.records...)
}

const unknownAnswerBody = `{"model":"m","usage":{},"answers":{"rank":{"type":"ranking"}}}`

var logScenarios = []struct {
	name    string
	replies []reply
}{
	{"success", []reply{ok(okBody)}},
	{"retry", []reply{{status: 429, body: `{}`, header: hdr("Retry-After-Ms", "0")}, ok(okBody)}},
	{"bad request", []reply{status(400)}},
	{"unknown answer type", []reply{ok(unknownAnswerBody)}},
}

// replyServer serves replies in order (repeating the last) from a real
// httptest server and returns a client for it.
func replyServer(t *testing.T, replies []reply, opts ...Option) *Client {
	t.Helper()
	var n atomic.Int32
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		rep := replies[min(int(n.Add(1))-1, len(replies)-1)]
		for k, vs := range rep.header {
			w.Header()[k] = vs
		}
		writeJSON(w, rep.status, rep.body)
	}, opts...)
	return c
}

func TestLoggingSilentByDefault(t *testing.T) {
	t.Setenv("TYPESAFE_LOG_LEVEL", "debug")
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, logger := range map[string][]Option{"omitted": nil, "nil": {WithLogger(nil)}} {
		for _, sc := range logScenarios {
			c := fakeClient(t, newFake(sc.replies...), logger...)
			_, _ = c.SystemOne(bg, "x", spamQuestion)
		}
	}
	if n := len(h.all()); n != 0 {
		t.Errorf("default logger received %d records", n)
	}
}

func TestNoConsoleOutput(t *testing.T) {
	if os.Getenv("TYPESAFE_CONSOLE_HELPER") == "1" {
		t.Skip("running as helper")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNoConsoleOutputHelper$")
	cmd.Env = append(os.Environ(), "TYPESAFE_CONSOLE_HELPER=1", "TYPESAFE_LOG_LEVEL=debug")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("SDK wrote to the console:\nstdout: %q\nstderr: %q", stdout.String(), stderr.String())
	}
}

func TestNoConsoleOutputHelper(t *testing.T) {
	if os.Getenv("TYPESAFE_CONSOLE_HELPER") != "1" {
		t.Skip("helper for TestNoConsoleOutput")
	}
	for _, logger := range [][]Option{nil, {WithLogger(nil)}} {
		for _, sc := range logScenarios {
			c := replyServer(t, sc.replies, logger...)
			_, _ = c.SystemOne(bg, "x", spamQuestion)
			_, _ = c.Models.List(bg)
		}
	}
	os.Exit(0) // skip the test framework's own output
}

type ctxKey struct{}

func TestInjectedLogger(t *testing.T) {
	run := func(t *testing.T, replies []reply) []capturedRecord {
		t.Helper()
		h := &recordingHandler{}
		c := fakeClient(t, newFake(replies...), WithLogger(slog.New(h)))
		ctx := context.WithValue(bg, ctxKey{}, "tid-42")
		_, _ = c.SystemOne(ctx, "x", spamQuestion)
		records := h.all()
		for _, r := range records {
			if r.ctx.Value(ctxKey{}) != "tid-42" {
				t.Errorf("record %q lacks the call's context", r.msg)
			}
			for k := range r.attrs {
				if k == "body" || k == "headers" || strings.EqualFold(k, "authorization") {
					t.Errorf("record %q has attr %q", r.msg, k)
				}
			}
			if s := mustJSON(t, r.attrs); strings.Contains(s, testKey) {
				t.Errorf("record leaks the key: %s", s)
			}
		}
		return records
	}
	t.Run("success", func(t *testing.T) {
		recs := run(t, []reply{{status: 200, body: okBody, header: hdr("X-Typesafe-Request-Id", "req-1")}})
		if len(recs) != 1 {
			t.Fatalf("%d records", len(recs))
		}
		r := recs[0]
		if r.level != slog.LevelDebug || r.msg != "typesafe.attempt" || r.attrs["method"] != "POST" ||
			r.attrs["url"] != "https://api.test/v1/systemone" || r.attrs["status"] != int64(200) || r.attrs["request_id"] != "req-1" {
			t.Errorf("record = %+v", r)
		}
	})
	t.Run("retry", func(t *testing.T) {
		recs := run(t, logScenarios[1].replies)
		var msgs []string
		for _, r := range recs {
			msgs = append(msgs, r.msg)
		}
		if strings.Join(msgs, ",") != "typesafe.attempt,typesafe.retry,typesafe.attempt" {
			t.Fatalf("records = %v", msgs)
		}
		r := recs[1]
		if r.attrs["attempt"] != int64(1) || r.attrs["reason"] != "status=429" || r.attrs["delay_ms"] != int64(0) {
			t.Errorf("retry record = %+v", r.attrs)
		}
	})
	t.Run("error is not logged", func(t *testing.T) {
		recs := run(t, []reply{status(400)})
		if len(recs) != 1 || recs[0].msg != "typesafe.attempt" {
			t.Fatalf("records = %+v", recs)
		}
		for _, r := range recs {
			if r.level >= slog.LevelWarn {
				t.Errorf("returned error logged at %v: %q", r.level, r.msg)
			}
		}
	})
	t.Run("unknown answer type", func(t *testing.T) {
		var warns []capturedRecord
		for _, r := range run(t, []reply{ok(unknownAnswerBody)}) {
			if r.level == slog.LevelWarn {
				warns = append(warns, r)
			}
		}
		if len(warns) != 1 || warns[0].msg != "typesafe.unknown_answer_type" || warns[0].attrs["question"] != "rank" || warns[0].attrs["type"] != "ranking" {
			t.Errorf("warnings = %+v", warns)
		}
	})
	t.Run("transport error kind", func(t *testing.T) {
		h := &recordingHandler{}
		f := newFake(ok(okBody))
		f.err = func(*http.Request) error { return errDeadline }
		c := fakeClient(t, f, WithLogger(slog.New(h)), noRetry())
		_, _ = c.SystemOne(bg, "x", spamQuestion)
		if recs := h.all(); len(recs) != 1 || recs[0].attrs["error_kind"] != "timeout" {
			t.Errorf("records = %+v", recs)
		}
	})
}

func TestStandaloneUnmarshalIsSilent(t *testing.T) {
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	var r SystemOneResponse
	if err := json.Unmarshal([]byte(unknownAnswerBody), &r); err != nil {
		t.Fatal(err)
	}
	if n := len(h.all()); n != 0 {
		t.Errorf("%d records", n)
	}
}
