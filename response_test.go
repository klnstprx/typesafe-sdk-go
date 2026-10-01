package typesafe

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"
	"testing"
)

const fullBody = `{
	"model": "jev-1",
	"usage": {"input_tokens": 120, "output_tokens": 12, "cached": 3},
	"answers": {
		"spam": {"type": "noul", "noul": 1, "extra": true},
		"tone": {"type": "choice", "choice": "calm", "confidence": 0.9, "probabilities": {"calm": 0.9, "angry": 0.1}},
		"urgency": {"type": "score", "score": 1.7, "confidence": 0.8,
			"legend": {"0": "low", "1": {"level": "mid"}, "2": ["high"]},
			"probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}},
		"future": {"type": "ranking", "order": [1, 2]}
	},
	"trace": "ignored"
}`

func TestSystemOneResponseDecoding(t *testing.T) {
	c := fakeClient(t, newFake(reply{status: 200, body: fullBody, header: hdr("X-Typesafe-Request-Id", "req-9")}))
	resp, err := c.SystemOne(bg, "x", spamQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "jev-1" || resp.Usage != (Usage{120, 12}) || resp.RequestID() != "req-9" {
		t.Errorf("resp = %+v", resp)
	}
	want := map[string]Answer{
		"spam": NoulAnswer{Noul: 1},
		"tone": ChoiceAnswer{Choice: "calm", Confidence: 0.9, Probabilities: map[string]float64{"calm": 0.9, "angry": 0.1}},
		"urgency": ScoreAnswer{
			Score: 1.7, Confidence: 0.8,
			Legend:        map[int]any{0: "low", 1: map[string]any{"level": "mid"}, 2: []any{"high"}},
			Probabilities: map[int]float64{0: 0.1, 1: 0.1, 2: 0.8},
		},
	}
	if !reflect.DeepEqual(resp.Answers, want) {
		t.Errorf("Answers = %#v", resp.Answers)
	}
	if resp.Nouls()["spam"] != want["spam"] || len(resp.Nouls()) != 1 {
		t.Errorf("Nouls() = %v", resp.Nouls())
	}
	if !reflect.DeepEqual(resp.Choices()["tone"], want["tone"]) || len(resp.Choices()) != 1 {
		t.Errorf("Choices() = %v", resp.Choices())
	}
	if !reflect.DeepEqual(resp.Scores()["urgency"], want["urgency"]) || len(resp.Scores()) != 1 {
		t.Errorf("Scores() = %v", resp.Scores())
	}
	if string(resp.RawJSON()) != fullBody {
		t.Error("RawJSON() differs from the original body")
	}
}

func TestSystemOneResponseLenientFields(t *testing.T) {
	cases := map[string]struct {
		body  string
		usage Usage
		n     int
	}{
		"answers absent":    {`{"model":"m","usage":{}}`, Usage{}, 0},
		"usage nulls":       {`{"model":"m","usage":{"input_tokens":null,"output_tokens":null},"answers":{}}`, Usage{}, 0},
		"only unknown type": {`{"model":"m","usage":{"input_tokens":5},"answers":{"x":{"type":"new"}}}`, Usage{InputTokens: 5}, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var r SystemOneResponse
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatal(err)
			}
			if r.Usage != tc.usage || r.Answers == nil || len(r.Answers) != tc.n {
				t.Errorf("r = %+v", r)
			}
			if string(r.RawJSON()) != tc.body {
				t.Error("RawJSON lost the original body")
			}
		})
	}
}

func TestSystemOneValidationPaths(t *testing.T) {
	const usage = `"usage":{"input_tokens":1,"output_tokens":1}`
	cases := []struct {
		name, body, path string
	}{
		{"not object", `[1]`, ""},
		{"not json", `oops`, ""},
		{"model missing", `{` + usage + `}`, "model"},
		{"model number", `{"model":1,` + usage + `}`, "model"},
		{"usage missing", `{"model":"m"}`, "usage"},
		{"usage null", `{"model":"m","usage":null}`, "usage"},
		{"input tokens string", `{"model":"m","usage":{"input_tokens":"1"}}`, "usage.input_tokens"},
		{"output tokens float", `{"model":"m","usage":{"output_tokens":1.5}}`, "usage.output_tokens"},
		{"answers null", `{"model":"m",` + usage + `,"answers":null}`, "answers"},
		{"answers array", `{"model":"m",` + usage + `,"answers":[]}`, "answers"},
		{"answer not object", `{"model":"m",` + usage + `,"answers":{"c":"x"}}`, "answers.c.type"},
		{"answer type missing", `{"model":"m",` + usage + `,"answers":{"c":{}}}`, "answers.c.type"},
		{"answer type checked before model", `{"answers":{"c":{"type":1}}}`, "answers.c.type"},
		{"noul missing", `{"model":"m",` + usage + `,"answers":{"n":{"type":"noul"}}}`, "answers.n.noul"},
		{"noul string", `{"model":"m",` + usage + `,"answers":{"n":{"type":"noul","noul":"0.5"}}}`, "answers.n.noul"},
		{"choice missing choice", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","confidence":1,"probabilities":{}}}}`, "answers.c.choice"},
		{"choice confidence", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","choice":"a","confidence":true,"probabilities":{}}}}`, "answers.c.confidence"},
		{"choice probability null", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"a":1,"x":null}}}}`, "answers.c.probabilities.x"},
		{"choice probability string", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"x":"1"}}}}`, "answers.c.probabilities.x"},
		{"choice probabilities array", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","choice":"a","confidence":1,"probabilities":[]}}}`, "answers.c.probabilities"},
		{"score legend array", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":[],"probabilities":{}}}}`, "answers.s.legend"},
		{"score legend key", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{"x":"a"},"probabilities":{}}}}`, "answers.s.legend.x"},
		{"score legend number", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{"0":1},"probabilities":{}}}}`, "answers.s.legend.0"},
		{"score legend bool", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{"0":true},"probabilities":{}}}}`, "answers.s.legend.0"},
		{"score legend null", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{"0":null},"probabilities":{}}}}`, "answers.s.legend.0"},
		{"score probability key", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{},"probabilities":{"1.5":1}}}}`, "answers.s.probabilities.1.5"},
		{"score probability null", `{"model":"m",` + usage + `,"answers":{"s":{"type":"score","score":1,"confidence":1,"legend":{},"probabilities":{"0":null}}}}`, "answers.s.probabilities.0"},
		{"first invalid probability in document order", `{"model":"m",` + usage + `,"answers":{"c":{"type":"choice","choice":"a","confidence":1,"probabilities":{"z":null,"a":null}}}}`, "answers.c.probabilities.z"},
		{"first invalid answer in document order", `{"model":"m",` + usage + `,"answers":{"z":{"type":"noul"},"a":{"type":"noul"}}}`, "answers.z.noul"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, newFake(reply{status: 200, body: tc.body, header: hdr("X-Typesafe-Request-Id", "req-v")}), noRetry())
			_, err := c.SystemOne(bg, "x", spamQuestion)
			var ve *ResponseValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %T %v", err, err)
			}
			if ve.FieldPath != tc.path {
				t.Errorf("FieldPath = %q, want %q", ve.FieldPath, tc.path)
			}
			want := "POST https://api.test/v1/systemone: 200 Invalid response data at '" + tc.path + "'. (request_id=req-v)"
			if err.Error() != want {
				t.Errorf("Error() = %q\nwant      %q", err.Error(), want)
			}
			if ve.StatusCode != 200 || string(ve.RawBody) != tc.body {
				t.Errorf("status=%d raw=%q", ve.StatusCode, ve.RawBody)
			}

			// Standalone decoding reports the same path, without HTTP metadata.
			var r SystemOneResponse
			err = json.Unmarshal([]byte(tc.body), &r)
			if json.Valid([]byte(tc.body)) {
				if !errors.As(err, &ve) || ve.FieldPath != tc.path || ve.Header != nil {
					t.Errorf("json.Unmarshal err = %v", err)
				}
			}
		})
	}
}

func TestModelsValidationPaths(t *testing.T) {
	cases := []struct{ name, body, path string }{
		{"null", `null`, ""},
		{"empty", `{}`, "models"},
		{"not array", `{"models":"bad"}`, "models"},
		{"missing card field", `{"models":[{"name":"a","description":"d","release_date":"r"},{"description":"d","release_date":"r"}]}`, "models[1].name"},
		{"card not object", `{"models":[1]}`, "models[0]"},
		{"date number", `{"models":[{"name":"a","description":"d","release_date":5}]}`, "models[0].release_date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, newFake(ok(tc.body)), noRetry())
			_, err := c.Models.List(bg)
			var ve *ResponseValidationError
			if !errors.As(err, &ve) || ve.FieldPath != tc.path || ve.Endpoint != "GET https://api.test/v1/models" {
				t.Fatalf("err = %v, want path %q", err, tc.path)
			}
			var r ListModelsResponse
			if err := json.Unmarshal([]byte(tc.body), &r); !errors.As(err, &ve) || ve.FieldPath != tc.path {
				t.Errorf("json.Unmarshal err = %v", err)
			}
		})
	}
}

func TestResponseMetaCopies(t *testing.T) {
	c := fakeClient(t, newFake(reply{status: 200, body: fullBody, header: hdr("X-Typesafe-Request-Id", "req-1", "X-Other", "v")}))
	resp, err := c.SystemOne(bg, "x", spamQuestion)
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Header()
	h.Set("X-Other", "mutated")
	h.Del("X-Typesafe-Request-Id")
	if resp.Header().Get("X-Other") != "v" || resp.Header().Get("X-Typesafe-Request-Id") != "req-1" {
		t.Error("Header() result aliases the stored headers")
	}
	raw := resp.RawJSON()
	raw[0] = 'X'
	if string(resp.RawJSON()) != fullBody {
		t.Error("RawJSON() result aliases the stored body")
	}
	for range 2 {
		hr := resp.RawHTTPResponse()
		b, err := io.ReadAll(hr.Body)
		if err != nil || string(b) != fullBody || hr.StatusCode != 200 {
			t.Fatalf("RawHTTPResponse body = %q, %v", b, err)
		}
		hr.Header.Set("X-Other", "mutated")
	}
	if resp.Header().Get("X-Other") != "v" {
		t.Error("RawHTTPResponse().Header aliases the stored headers")
	}
	first := resp.RawHTTPResponse().Request
	first.Header.Set("Authorization", "mutated")
	first.URL.Path = "/mutated"
	second := resp.RawHTTPResponse().Request
	if second.Header.Get("Authorization") != "Bearer "+testKey || second.URL.Path != "/v1/systemone" {
		t.Error("RawHTTPResponse().Request aliases the stored request")
	}
	if b, _ := io.ReadAll(second.Body); string(b) != `{"state":"x","model":"jev-latest","questions":{"spam":{"type":"noul","instructions":"Is this spam?"}}}` {
		t.Errorf("Request body = %s", b)
	}
	if string(resp.RawJSON()) != fullBody {
		t.Error("reading RawHTTPResponse consumed RawJSON")
	}

	var standalone SystemOneResponse
	if err := json.Unmarshal([]byte(fullBody), &standalone); err != nil {
		t.Fatal(err)
	}
	if string(standalone.RawJSON()) != fullBody || standalone.Header() != nil || standalone.RawHTTPResponse() != nil || standalone.RequestID() != "" {
		t.Error("standalone decode metadata wrong")
	}
}

func TestRawJSONPreservesNulls(t *testing.T) {
	body := `{"model":"m","usage":{"input_tokens":null},"answers":{"x":{"type":"new"}}}`
	c := fakeClient(t, newFake(ok(body)))
	resp, err := c.SystemOne(bg, "x", spamQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.RawJSON()) != body {
		t.Errorf("RawJSON = %s", resp.RawJSON())
	}
}

func TestResponseSerialization(t *testing.T) {
	var resp SystemOneResponse
	if err := json.Unmarshal([]byte(fullBody), &resp); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(top)); !slices.Equal(keys, []string{"answers", "model", "usage"}) {
		t.Errorf("top-level keys = %v", keys)
	}
	var wire struct {
		Model   string                    `json:"model"`
		Usage   map[string]any            `json:"usage"`
		Answers map[string]map[string]any `json:"answers"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string][]string{
		"spam":    {"noul", "type"},
		"tone":    {"choice", "confidence", "probabilities", "type"},
		"urgency": {"confidence", "legend", "probabilities", "score", "type"},
	}
	for name, keys := range wantKeys {
		if got := slices.Sorted(maps.Keys(wire.Answers[name])); !slices.Equal(got, keys) {
			t.Errorf("%s keys = %v, want %v", name, got, keys)
		}
	}
	if wire.Answers["urgency"]["type"] != "score" || wire.Answers["urgency"]["legend"].(map[string]any)["1"] == nil {
		t.Errorf("urgency = %v", wire.Answers["urgency"])
	}
	if got := slices.Sorted(maps.Keys(wire.Usage)); !slices.Equal(got, []string{"input_tokens", "output_tokens"}) {
		t.Errorf("usage keys = %v", got)
	}

	var again SystemOneResponse
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatal(err)
	}
	again.ResponseMeta, resp.ResponseMeta = ResponseMeta{}, ResponseMeta{}
	if !reflect.DeepEqual(again, resp) {
		t.Errorf("round trip changed the value:\n%#v\n%#v", again, resp)
	}

	models := ListModelsResponse{Models: []ModelMetadata{{"a", "b", "c"}}}
	out, _ = json.Marshal(models)
	if string(out) != `{"models":[{"name":"a","description":"b","release_date":"c"}]}` {
		t.Errorf("models JSON = %s", out)
	}
	var modelsAgain ListModelsResponse
	if err := json.Unmarshal(out, &modelsAgain); err != nil {
		t.Fatal(err)
	}
	modelsAgain.ResponseMeta = ResponseMeta{}
	if !reflect.DeepEqual(modelsAgain, models) {
		t.Errorf("models round trip = %#v", modelsAgain)
	}
}
