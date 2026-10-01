package typesafe

// Fuzz targets with seeds from earlier bugs and edge cases. CI runs each for a
// bounded time; `go test` runs the seeds and any saved failures in
// testdata/fuzz as ordinary tests.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var responseSeeds = []string{
	fullBody,
	okBody,
	unknownAnswerBody,
	`{"model":"m","usage":{}}`,
	`{"model":"m","usage":{"input_tokens":null}}`,
	`{"model":"m","usage":{"input_tokens":100000000000000000000}}`,
	`{"model":"m","usage":{},"answers":null}`,
	`{"model":"m","usage":{},"answers":{"n":{"type":"noul","noul":NaN}}}`,
	`{"model":"m","usage":{},"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{"0":null,"01":"x","-0":"y"},"probabilities":{"1":1,"01":2}}}}`,
	`{"model":"m","usage":{},"answers":{"c":{"type":"choice","choice":"a","confidence":-0,"probabilities":{"z":null,"a":null}}}}`,
	`{"model":"m","usage":{},"answers":{"a":{"type":"noul","noul":1},"a":{"type":"noul","noul":2}}}`,
	`{"model":"\ud800","usage":{},"answers":{"\xff":{"type":"noul","noul":1e308}}}`,
	`{"models":[{"name":"a","description":"d","release_date":"r"},{"name":5}]}`,
	`{"answers":{"spam":{"noul":"high"}}}`,
	`[1]`, `null`, ``, `oops`, `{"model":`,
}

// FuzzResponseDecoding checks that the SDK decoders never panic, that every
// rejection is a *ResponseValidationError with the client's field path, and
// that every accepted body survives serialization.
func FuzzResponseDecoding(f *testing.F) {
	for _, s := range responseSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		resp, err := decodeSystemOne(data, func(string, string) {})
		var standalone SystemOneResponse
		uerr := json.Unmarshal(data, &standalone)
		if err != nil {
			var pe *pathError
			if !errors.As(err, &pe) {
				t.Fatalf("decoder error %T is not a path error", err)
			}
			var ve *ResponseValidationError
			var se *json.SyntaxError
			if !errors.As(uerr, &se) && (!errors.As(uerr, &ve) || ve.FieldPath != pe.path) {
				t.Fatalf("json.Unmarshal = %v, decoder path %q", uerr, pe.path)
			}
		} else {
			if uerr != nil {
				t.Fatalf("decoder accepted what json.Unmarshal rejects: %v", uerr)
			}
			roundTrip(t, *resp)
		}

		models, err := decodeModels(data)
		if err == nil {
			out, err := json.Marshal(models)
			if err != nil {
				t.Fatal(err)
			}
			again, err := decodeModels(out)
			if err != nil || !reflect.DeepEqual(again, models) {
				t.Fatalf("models round trip: %v\n%s", err, out)
			}
		}
	})
}

func roundTrip(t *testing.T, resp SystemOneResponse) {
	t.Helper()
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal accepted response: %v", err)
	}
	again, err := decodeSystemOne(out, nil)
	if err != nil {
		t.Fatalf("serialized response no longer decodes: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(*again, resp) {
		t.Fatalf("round trip changed the response:\n%#v\n%#v", *again, resp)
	}
	if out2, _ := json.Marshal(*again); string(out2) != string(out) {
		t.Fatalf("serialization is not stable:\n%s\n%s", out, out2)
	}
}

type fuzzShape struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type          string             `json:"type"`
		Noul          *float64           `json:"noul"`
		Probabilities map[string]float64 `json:"probabilities"`
		Legend        map[int]any        `json:"legend"`
	} `json:"answers"`
	Models []struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	} `json:"models"`
	Usage *Usage `json:"usage"`
}

var bracketIndex = regexp.MustCompile(`\[(\d+)\]`)

// FuzzValidationPath checks that SystemOneAs-style path recovery locates every
// type mismatch where encoding/json itself says it is.
func FuzzValidationPath(f *testing.F) {
	for _, s := range responseSeeds {
		f.Add([]byte(s))
	}
	f.Add([]byte(`{"models":[{},{"tags":["a",1]}]}`))
	f.Add([]byte(`{"answers":{"a.b":{"NOUL":"x"}}}`))
	f.Add([]byte(`{"answers":{"a":{"probabilities":{"k":"x"}}}}`))
	f.Add([]byte(`{"answers":{"a":{"legend":{"x":1}}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var v fuzzShape
		err := json.Unmarshal(data, &v)
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return
		}
		got, ok := pathAtOffset(data, te.Offset)
		if !ok {
			t.Fatalf("no value ends at offset %d (field %q)", te.Offset, te.Field)
		}
		if strings.ContainsAny(te.Field, "[]") {
			return // a key with brackets makes the two notations ambiguous
		}
		// Go 1.26 and later name every segment in Field, so the paths must
		// match; Go 1.25 names struct fields only, which must then appear in
		// order. Struct fields match JSON keys case-insensitively.
		ours := strings.Split(strings.TrimPrefix(bracketIndex.ReplaceAllString(got, ".$1"), "."), ".")
		theirs := strings.Split(te.Field, ".")
		if te.Field == "" {
			theirs = nil
		}
		if len(theirs) == len(ours) {
			if !strings.EqualFold(strings.Join(ours, "."), te.Field) {
				t.Fatalf("path %q, encoding/json reports %q", got, te.Field)
			}
		} else if !isSubsequenceFold(theirs, ours) {
			t.Fatalf("path %q does not contain encoding/json's fields %q", got, te.Field)
		}
		// The SDK wraps the same error with the recovered path.
		resp := &http.Response{StatusCode: 200, Header: http.Header{}}
		if ve := newValidationError(resp, data, "POST x", err); ve.FieldPath != got {
			t.Fatalf("FieldPath %q, want %q", ve.FieldPath, got)
		}
	})
}

// isSubsequenceFold reports whether sub appears in order within seq, ignoring case.
func isSubsequenceFold(sub, seq []string) bool {
	i := 0
	for _, s := range seq {
		if i < len(sub) && strings.EqualFold(sub[i], s) {
			i++
		}
	}
	return i == len(sub)
}

// FuzzRedaction checks that no form of a credential survives sanitizing, for
// errors that embed it raw, Go-quoted, JSON-escaped, in a URL, or in a field
// other than the wrapped error.
func FuzzRedaction(f *testing.F) {
	f.Add("sk-test-0123456789", "tok-secret", "pw-secret", uint8(0))
	f.Add(`sk-"quoted"-key`, `"a=b"`, "p@ss/word", uint8(1))
	f.Add(`sk-<html>&\`, "", "", uint8(2))
	f.Add("k", "x", "y", uint8(3))
	f.Add("sk-key", "tok", "pw é", uint8(4))
	f.Add("sk-key", `bad value,"x"`, "", uint8(5))
	f.Fuzz(func(t *testing.T, key, cookie, password string, shape uint8) {
		key, err := resolveAPIKey(&key)
		if err != nil {
			return // only keys the client accepts
		}
		header := http.Header{"Authorization": {"Bearer " + key}}
		u := &url.URL{Scheme: "https", Host: "api.test", Path: "/v1/systemone"}
		creds := []string{key}
		// Header values cannot carry surrounding whitespace, so a cookie value
		// is never whitespace-padded on the wire.
		if cookie != "" && cookie == strings.TrimSpace(cookie) && !strings.ContainsAny(cookie, ";\r\n") {
			header.Set("Cookie", "session="+cookie)
			creds = append(creds, cookie)
		}
		if password != "" {
			u.User = url.UserPassword("alice", password)
			creds = append(creds, password)
		}
		for _, c := range creds {
			if strings.Contains(c, "*") {
				return // the "***" mask could rebuild such a credential
			}
		}

		var cause error
		switch shape % 6 {
		case 0:
			cause = fmt.Errorf("dial failed: %s", header.Get("Authorization"))
		case 1:
			cause = fmt.Errorf("dial failed: %q / %s / %q", key, mustMarshal(key), cookie)
		case 2:
			cause = &url.Error{Op: "Post", URL: u.String(), Err: fmt.Errorf("cookie %s", header.Get("Cookie"))}
		case 3:
			cause = &net.OpError{Op: key, Net: "tcp", Err: io.EOF}
		case 4:
			cause = errors.Join(io.EOF, fmt.Errorf("pw=%s %q url=%s", password, password, u))
		case 5:
			cause = fmt.Errorf("outer: %w", fmt.Errorf("inner %s %s", key, mustMarshal(cookie)))
		}
		safe := newRedactor(key, header, u).sanitize(cause)
		messages := append(chainMessages(safe), fmt.Sprintf("%v", safe), fmt.Sprintf("%+v", safe))
		if re, ok := safe.(*redactedError); ok {
			// %#v renders only the (already checked) message, quoted, inside
			// a fixed type name.
			gs := fmt.Sprintf("%#v", re)
			if want := "&typesafe.redactedError{" + strconv.Quote(re.msg) + "}"; gs != want {
				t.Fatalf("%%#v = %s, want %s", gs, want)
			}
		}
		for _, msg := range messages {
			for _, c := range creds {
				for _, form := range credentialForms(c) {
					if strings.Contains(msg, form) {
						t.Fatalf("sanitized error leaks %q (credential %q): %q", form, c, msg)
					}
				}
			}
		}
		if errors.Is(cause, io.EOF) && !errors.Is(safe, io.EOF) {
			t.Fatal("sanitizing lost errors.Is(io.EOF)")
		}
	})
}

// credentialForms lists how a credential can appear in an error message.
func credentialForms(c string) []string {
	unquote := func(q string) string { return q[1 : len(q)-1] } // drop the outer quotes only
	forms := []string{c, unquote(strconv.Quote(c)), unquote(mustMarshal(c))}
	if escaped := strings.TrimPrefix(url.UserPassword("", c).String(), ":"); escaped != c {
		forms = append(forms, escaped)
	}
	return forms
}

func mustMarshal(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// FuzzRetryDelays checks that header parsing and backoff never overflow or
// produce a negative delay, and that backoff stays within BackoffMax.
func FuzzRetryDelays(f *testing.F) {
	f.Add("150", "3", int64(500*time.Millisecond), int64(5*time.Second), 0.25, uint16(1), 0.5)
	f.Add("", "9223372036.9", int64(1), int64(time.Hour), 0.0, uint16(2000), 0.0)
	f.Add("inf", "1e308", int64(math.MaxInt64), int64(math.MaxInt64), 0.0, uint16(64), 0.999)
	f.Add("NaN", "-1", int64(500*time.Millisecond), int64(600*time.Microsecond), 1.0, uint16(1), 1.0)
	f.Add("-1", "0x1p4", int64(0), int64(0), 0.5, uint16(0), 0.0)
	f.Add("1e-400", "1_000", int64(3), int64(7), 0.1, uint16(65535), 0.3)
	f.Add("bad", "Wed, 21 Oct 2015 07:28:00 GMT", int64(1), int64(1), 0.0, uint16(3), 0.0)
	f.Add("9223372036854.775807", "+3", int64(math.MaxInt64/2), int64(math.MaxInt64), 0.25, uint16(2), 0.0)
	f.Fuzz(func(t *testing.T, ms, seconds string, initial, maxDelay int64, jitter float64, n uint16, r float64) {
		h := http.Header{}
		if ms != "" {
			h.Set(headerRetryAfterMS, ms)
		}
		if seconds != "" {
			h.Set(headerRetryAfter, seconds)
		}
		if d, ok := parseRetryAfter(h); ok && d < 0 {
			t.Fatalf("parseRetryAfter(%q, %q) = %v", ms, seconds, d)
		}

		p := RetryPolicy{BackoffInitial: time.Duration(initial), BackoffMax: time.Duration(maxDelay), BackoffJitter: jitter}
		if p.validate() != nil || math.IsNaN(r) || r < 0 || r >= 1 {
			return
		}
		d := p.backoff(int(n), r)
		if d < 0 || d > p.BackoffMax {
			t.Fatalf("backoff(%d, %v) = %v with %+v", n, r, d, p)
		}
		if next := p.backoff(int(n)+1, 0); next < p.backoff(int(n), 0) {
			t.Fatalf("backoff decreased from attempt %d to %d", n, n+1)
		}
		if w := p.wait(int(n), &APIError{Header: h}); w < 0 {
			t.Fatalf("wait = %v", w)
		}
	})
}
