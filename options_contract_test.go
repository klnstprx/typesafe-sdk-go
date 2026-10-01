package typesafe

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestWithExtraBodySnapshotsTopLevelMap(t *testing.T) {
	nested := map[string]any{"value": "before"}
	fields := map[string]any{"model": "snapshot", "nested": nested, "nullable": nil}
	opt := WithExtraBody(fields)
	fields["model"] = "changed"
	delete(fields, "nullable")
	fields["added"] = true
	// The contract is a shallow snapshot: sequential changes to nested values
	// are observed when the next call encodes its body.
	nested["value"] = "after"

	f := newFake(ok(okBody))
	c := fakeClient(t, f)
	for range 2 {
		if _, err := c.SystemOne(bg, "x", spamQuestion, WithModel("call-model"), opt); err != nil {
			t.Fatal(err)
		}
	}
	for _, req := range f.all() {
		var body map[string]any
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "snapshot" {
			t.Errorf("model = %v; caller's top-level mutation affected the option", body["model"])
		}
		if value, present := body["nullable"]; !present || value != nil {
			t.Errorf("nullable = %v, present = %v; want JSON null", value, present)
		}
		if _, present := body["added"]; present {
			t.Error("field added after option creation appeared in the request")
		}
		if got := body["nested"].(map[string]any)["value"]; got != "after" {
			t.Errorf("nested value = %v; want the caller-owned value at encoding time", got)
		}
	}
	if fields["model"] != "changed" || fields["nullable"] != nil || len(fields) != 3 {
		t.Errorf("caller map was changed by the SDK: %v", fields)
	}
}

func TestWithExtraBodyMergeDoesNotChangeReusableOption(t *testing.T) {
	f := newFake(ok(okBody))
	c := fakeClient(t, f)
	first := WithExtraBody(map[string]any{"model": "first", "trace": "keep"})
	second := WithExtraBody(map[string]any{"model": "second", "trace": nil})
	for _, opts := range [][]CallOption{{first, second}, {first}} {
		if _, err := c.SystemOne(bg, "x", spamQuestion, opts...); err != nil {
			t.Fatal(err)
		}
	}
	for i, req := range f.all() {
		var body map[string]any
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		wantModel, wantTrace := "second", any(nil)
		if i == 1 {
			wantModel, wantTrace = "first", "keep"
		}
		if body["model"] != wantModel || body["trace"] != wantTrace {
			t.Errorf("request %d: model = %v, trace = %v; want %s, %v", i, body["model"], body["trace"], wantModel, wantTrace)
		}
	}
}

func TestModelsRejectsEmptyExtraBody(t *testing.T) {
	for _, fields := range []map[string]any{nil, {}} {
		f := newFake(ok(`{"models":[]}`))
		c := fakeClient(t, f)
		if _, err := c.Models.List(bg, WithExtraBody(fields)); err == nil {
			t.Errorf("Models.List accepted WithExtraBody(%v)", fields)
		}
		if f.count() != 0 {
			t.Error("Models.List sent a request despite the unsupported option")
		}
	}
}

func TestWithHeadersSnapshotsMapAndSlicesAtCreation(t *testing.T) {
	h := http.Header{"X-Snapshot": {"before"}}
	opt := WithHeaders(h)
	h["X-Snapshot"][0] = "changed"
	h.Set("X-Added", "changed")
	f := newFake(ok(okBody))
	c := fakeClient(t, f, opt)
	if _, err := c.SystemOne(bg, "x", spamQuestion); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SystemOne(bg, "x", spamQuestion, WithHeader("X-Snapshot", "override"), opt); err != nil {
		t.Fatal(err)
	}
	for _, req := range f.all() {
		if got := req.Header.Get("X-Snapshot"); got != "before" {
			t.Errorf("X-Snapshot = %q; want the value at option creation", got)
		}
		if got := req.Header.Get("X-Added"); got != "" {
			t.Errorf("X-Added = %q; header added after option creation was sent", got)
		}
	}
}

func TestWithRetryPolicySnapshotsStatusesAtCreation(t *testing.T) {
	statuses := []int{409}
	opt := WithRetryPolicy(RetryPolicy{MaxRetries: 1, HTTPStatuses: statuses})
	statuses[0] = 503
	for _, perCall := range []bool{false, true} {
		f := newFake(status(409), ok(okBody))
		var clientOpts []Option
		var callOpts []CallOption
		if perCall {
			clientOpts = []Option{noRetry()}
			callOpts = []CallOption{opt}
		} else {
			clientOpts = []Option{opt}
		}
		c := fakeClient(t, f, clientOpts...)
		if _, err := c.SystemOne(bg, "x", spamQuestion, callOpts...); err != nil {
			t.Fatalf("perCall=%v: %v; want the original 409 retry decision", perCall, err)
		}
		if f.count() != 2 {
			t.Errorf("perCall=%v: %d requests; want a retry", perCall, f.count())
		}
	}
}
