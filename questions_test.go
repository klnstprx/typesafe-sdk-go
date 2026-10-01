package typesafe

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestQuestionWireJSON(t *testing.T) {
	type rubric struct {
		Level string `json:"level"`
	}
	cases := []struct {
		name string
		q    Question
		want string
	}{
		{"empty noul", Noul{}, `{"type":"noul"}`},
		{"noul empty string kept", Noul{Instructions: ""}, `{"type":"noul","instructions":""}`},
		{"noul criteria", Noul{Instructions: "Spam?", Criteria: &NoulCriteria{True: "ads"}}, `{"type":"noul","instructions":"Spam?","criteria":{"true":"ads"}}`},
		{"noul empty criteria", Noul{Criteria: &NoulCriteria{}}, `{"type":"noul","criteria":{}}`},
		{"noul structured", Noul{Instructions: map[string]any{"task": "x", "n": nil}}, `{"type":"noul","instructions":{"n":null,"task":"x"}}`},
		{"choice", Choice{Instructions: "Tone?", Criteria: map[string]any{"calm": nil, "angry": "upset"}}, `{"type":"choice","instructions":"Tone?","criteria":{"angry":"upset","calm":null}}`},
		{"choice empty map kept", Choice{Criteria: map[string]any{}}, `{"type":"choice","criteria":{}}`},
		{"score", Score{Criteria: []any{"low", rubric{"high"}, []any{1, nil}}}, `{"type":"score","criteria":["low",{"level":"high"},[1,null]]}`},
		{"raw verbatim", RawQuestion{"type": "future", "extra": []any{nil}}, `{"extra":[null],"type":"future"}`},
		{"pointer", &Noul{Instructions: "x"}, `{"type":"noul","instructions":"x"}`},
		{"raw json", Noul{Instructions: json.RawMessage(` {"a": 1}`)}, `{"type":"noul","instructions":{"a":1}}`},
		{"no html escaping", Noul{Instructions: "a<b>&c"}, `{"type":"noul","instructions":"a<b>&c"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeQuestion("q", tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
			if _, isRaw := tc.q.(RawQuestion); !isRaw {
				// json.Marshal on the typed value yields the same wire form.
				if b, err := json.Marshal(tc.q); err != nil || !jsonEqual(t, b, got) {
					t.Errorf("json.Marshal = %s, %v", b, err)
				}
			}
		})
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return mustJSON(t, x) == mustJSON(t, y)
}

func TestMarshalRejectsWhatSendingRejects(t *testing.T) {
	for name, q := range map[string]Question{
		"score without criteria":  Score{},
		"choice without criteria": Choice{Instructions: "?"},
		"number instructions":     Noul{Instructions: 5},
	} {
		t.Run(name, func(t *testing.T) {
			if b, err := json.Marshal(q); err == nil {
				t.Errorf("json.Marshal = %s, want an error", b)
			}
		})
	}
}

func TestQuestionValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		qs   Questions
		want string
	}{
		{"empty", Questions{}, "at least one question is required"},
		{"nil", nil, "at least one question is required"},
		{"nil question", Questions{"x": nil}, `question "x" must be`},
		{"score empty", Questions{"urgency": Score{}}, `score question "urgency" has no criteria; at least one score is required`},
		{"choice nil criteria", Questions{"x": Choice{}}, `question "x" requires "criteria"`},
		{"raw no type", Questions{"x": RawQuestion{"instructions": "?"}}, `question "x" must be`},
		{"raw empty type", Questions{"x": RawQuestion{"type": ""}}, `question "x" must be`},
		{"raw non-string type", Questions{"x": RawQuestion{"type": 1}}, `question "x" must be`},
		{"raw choice no criteria", Questions{"x": RawQuestion{"type": "choice"}}, `question "x" requires "criteria"`},
		{"raw score no criteria", Questions{"x": RawQuestion{"type": "score"}}, `question "x" requires "criteria"`},
		{"raw score empty criteria", Questions{"urgency": RawQuestion{"type": "score", "criteria": []any{}}}, `score question "urgency" has no criteria`},
		{"raw score false criteria", Questions{"u": RawQuestion{"type": "score", "criteria": false}}, `score question "u" has no criteria`},
		{"raw score zero criteria", Questions{"u": RawQuestion{"type": "score", "criteria": 0}}, `score question "u" has no criteria`},
		{"raw score float zero criteria", Questions{"u": RawQuestion{"type": "score", "criteria": 0.0}}, `score question "u" has no criteria`},
		{"raw score negative zero criteria", Questions{"u": RawQuestion{"type": "score", "criteria": math.Copysign(0, -1)}}, `score question "u" has no criteria`},
		{"raw score empty string criteria", Questions{"u": RawQuestion{"type": "score", "criteria": ""}}, `score question "u" has no criteria`},
		{"number instructions", Questions{"x": Noul{Instructions: 5}}, `question "x" instructions must encode to a JSON string, object, or array`},
		{"bool criteria", Questions{"x": Noul{Criteria: &NoulCriteria{False: true}}}, `must encode to a JSON string, object, or array`},
		{"nil score item", Questions{"x": Score{Criteria: []any{"a", nil}}}, `question "x" criteria[1] must encode`},
		{"number choice label", Questions{"x": Choice{Criteria: map[string]any{"a": 1.5}}}, `criteria "a" must encode`},
		{"unencodable", Questions{"x": Noul{Instructions: map[string]any{"f": math.Inf(1)}}}, "could not be encoded as JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(ok(okBody))
			c := fakeClient(t, f)
			_, err := c.SystemOne(bg, "x", tc.qs)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "typesafe: ") {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if f.count() != 0 {
				t.Error("request sent despite invalid questions")
			}
		})
	}
}

func TestRawScoreTruthyCriteriaPassThrough(t *testing.T) {
	// Python only rejects falsy criteria; other values reach the server, which validates them.
	for _, criteria := range []any{true, 1, 0.5, "x", []any{"a"}} {
		f := newFake(ok(okBody))
		c := fakeClient(t, f)
		if _, err := c.SystemOne(bg, "x", Questions{"u": RawQuestion{"type": "score", "criteria": criteria}}); err != nil || f.count() != 1 {
			t.Errorf("criteria %#v: err = %v, sent %d", criteria, err, f.count())
		}
	}
}

func TestRequestBody(t *testing.T) {
	send := func(t *testing.T, state any, opts ...CallOption) string {
		t.Helper()
		f := newFake(ok(okBody))
		c := fakeClient(t, f, WithModel("m"))
		if _, err := c.SystemOne(bg, state, Questions{"q": Noul{}}, opts...); err != nil {
			t.Fatal(err)
		}
		return string(f.all()[0].Body)
	}
	t.Run("order state model questions", func(t *testing.T) {
		got := send(t, "hi")
		if want := `{"state":"hi","model":"m","questions":{"q":{"type":"noul"}}}`; got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
	t.Run("structured state keeps nested nulls and arrays", func(t *testing.T) {
		type msg struct {
			Subject string `json:"subject"`
			Tags    []any  `json:"tags"`
			Extra   any    `json:"extra"`
		}
		got := send(t, msg{"dup", []any{nil, 1}, nil})
		if want := `{"state":{"subject":"dup","tags":[null,1],"extra":null},"model":"m","questions":{"q":{"type":"noul"}}}`; got != want {
			t.Errorf("body = %s", got)
		}
	})
	t.Run("extra body shallow merge keeps nulls", func(t *testing.T) {
		got := send(t, "hi", WithExtraBody(map[string]any{"state": map[string]any{"a": 1}, "trace": nil}), WithExtraBody(map[string]any{"debug": true}))
		if want := `{"debug":true,"model":"m","questions":{"q":{"type":"noul"}},"state":{"a":1},"trace":null}`; got != want {
			t.Errorf("body = %s", got)
		}
	})
	for name, state := range map[string]any{"nil": nil, "number": 5, "bool": true} {
		t.Run("rejects "+name+" state", func(t *testing.T) {
			f := newFake(ok(okBody))
			c := fakeClient(t, f)
			_, err := c.SystemOne(bg, state, spamQuestion)
			if err == nil || !strings.Contains(err.Error(), "state must encode") || f.count() != 0 {
				t.Fatalf("err = %v, sent %d", err, f.count())
			}
		})
	}
	t.Run("unencodable extra body fails before I/O", func(t *testing.T) {
		f := newFake(ok(okBody))
		c := fakeClient(t, f)
		_, err := c.SystemOne(bg, "x", spamQuestion, WithExtraBody(map[string]any{"ch": make(chan int)}))
		if err == nil || !strings.Contains(err.Error(), "typesafe: the request body could not be encoded as JSON") || f.count() != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("extra body does not mutate caller map", func(t *testing.T) {
		extra := map[string]any{"a": 1}
		send(t, "x", WithExtraBody(extra))
		if len(extra) != 1 {
			t.Errorf("extra mutated: %v", extra)
		}
	})
}
