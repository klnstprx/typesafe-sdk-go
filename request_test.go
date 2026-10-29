package typesafe

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSystemOneRequestRoundTrip(t *testing.T) {
	wire := `{"state":{"id":9007199254740993},"model":"request-model","questions":{
		"n":{"type":"noul","instructions":{"id":9007199254740993},"criteria":{"true":["yes"],"false":"no"}},
		"c":{"type":"choice","criteria":{"a":null,"b":{"id":9007199254740993}}},
		"s":{"type":"score","instructions":["rate"],"criteria":["low",{"id":9007199254740993}]},
		"b":{"type":"box","instructions":{"id":9007199254740993}},
		"future":{"type":"future","value":9007199254740993}
	}}`
	var req SystemOneRequest
	if err := json.Unmarshal([]byte(wire), &req); err != nil {
		t.Fatal(err)
	}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Question{"n": Noul{}, "c": Choice{}, "s": Score{}, "b": Box{}, "future": RawQuestion{}} {
		if reflect.TypeOf(req.Questions[name]) != reflect.TypeOf(want) {
			t.Fatalf("%s decoded as %T", name, req.Questions[name])
		}
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(b []byte) any {
		t.Helper()
		var v any
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !reflect.DeepEqual(decode([]byte(wire)), decode(encoded)) {
		t.Fatalf("round trip changed request: %s", encoded)
	}
}

func TestRequestValidationBeforeDispatch(t *testing.T) {
	for _, wire := range []string{
		`{}`, `{"state":null,"questions":{"q":{"type":"noul","instructions":"?"}}}`,
		`{"state":42,"questions":{"q":{"type":"noul","instructions":"?"}}}`,
		`{"state":"x","questions":{"q":{"type":"noul"}}}`,
		`{"state":"x","questions":[]}`, `{"state":"x","questions":{}}`,
		`{"state":"x","questions":{"q":null}}`,
		`{"state":"x","questions":{"q":{"type":42}}}`,
		`{"state":"x","questions":{"q":{"type":"noul","instructions":42}}}`,
		`{"state":"x","questions":{"q":{"type":"noul","criteria":{"true":false}}}}`,
		`{"state":"x","questions":{"q":{"type":"choice"}}}`,
		`{"state":"x","questions":{"q":{"type":"choice","criteria":[]}}}`,
		`{"state":"x","questions":{"q":{"type":"choice","criteria":{}}}}`,
		`{"state":"x","questions":{"q":{"type":"score","criteria":[]}}}`,
		`{"state":"x","questions":{"q":{"type":"score","criteria":[null]}}}`,
		`{"state":"x","questions":{"q":{"type":"score","criteria":[1]}}}`,
		`{"state":"x","questions":`,
	} {
		t.Run(wire, func(t *testing.T) {
			var req SystemOneRequest
			if err := json.Unmarshal([]byte(wire), &req); err != nil {
				return
			}
			if err := req.Validate(); err == nil {
				t.Fatal("accepted invalid request")
			}
			f := newFake(ok(okBody))
			c := fakeClient(t, f)
			if _, err := c.CreateSystemOne(bg, req); err == nil {
				t.Fatal("call accepted invalid request")
			}
			if f.count() != 0 {
				t.Fatal("invalid request reached transport")
			}
		})
	}
}

func TestRequestUnknownFields(t *testing.T) {
	const wire = `{"state":"x","extra":true,"questions":{"q":{"type":"noul","instructions":"?","extra":true}}}`
	var req SystemOneRequest
	if err := json.Unmarshal([]byte(wire), &req); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(strings.NewReader(wire))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err == nil {
		t.Fatal("strict envelope accepted extra field")
	}
	d = json.NewDecoder(strings.NewReader(`{"state":"x","questions":{"q":{"type":"noul","instructions":"?","extra":true}}}`))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSystemOneModelPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, model, want string
		opts              []CallOption
	}{
		{name: "default", want: "client"},
		{name: "request", model: "request", want: "request"},
		{name: "option", model: "request", want: "option", opts: []CallOption{WithModel("option")}},
		{name: "explicit empty", model: "request", want: "", opts: []CallOption{WithModel("")}},
		{name: "extra body", model: "request", want: "extra", opts: []CallOption{WithModel("option"), WithExtraBody(map[string]any{"model": "extra"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(ok(okBody))
			c := fakeClient(t, f, WithModel("client"))
			req := SystemOneRequest{State: json.RawMessage(`"x"`), Model: tc.model, Questions: spamQuestion}
			if _, err := c.CreateSystemOne(bg, req, tc.opts...); err != nil {
				t.Fatal(err)
			}
			var body SystemOneRequest
			if err := json.Unmarshal(f.all()[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			if body.Model != tc.want {
				t.Fatalf("model = %q, want %q", body.Model, tc.want)
			}
			if req.Model != tc.model {
				t.Fatal("request mutated")
			}
		})
	}
}

func TestRequestAndConvenienceCallsAgree(t *testing.T) {
	f := newFake(ok(okBody))
	c := fakeClient(t, f)
	req := SystemOneRequest{State: json.RawMessage(`{"id":9007199254740993}`), Model: "m", Questions: spamQuestion}
	if _, err := c.CreateSystemOne(bg, req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SystemOne(bg, req.State, req.Questions, WithModel("m")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.all()[0].Body, f.all()[1].Body) {
		t.Fatal("entry points sent different requests")
	}
}

func TestRequestPreservesRawQuestionEncoding(t *testing.T) {
	// RawQuestion deliberately allows fields outside the typed primitive schema.
	q := RawQuestion{"type": "noul", "instructions": 42, "extra": "kept"}
	req := SystemOneRequest{State: json.RawMessage(`"x"`), Questions: Questions{"q": q}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	f := newFake(ok(okBody))
	c := fakeClient(t, f)
	if _, err := c.CreateSystemOne(bg, req); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(f.all()[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, body.Questions["q"], want) {
		t.Fatalf("raw question changed: %s", body.Questions["q"])
	}
}
