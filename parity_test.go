package typesafe

// Parity test against expectations generated from the Python SDK. Inputs and
// Python's observed results live in testdata/parity/python-0.7.5.json; see
// testdata/parity/generate.py. Intentional differences are listed in
// testdata/parity/differences.json and applied here: "normalize" entries map
// Python's representation to Go's for every case, and "override" entries
// merge-patch (RFC 7386) the expectation of specific cases. Both are checked
// for staleness, so a fixed or vanished difference fails the test.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

const (
	parityPythonVersion = "0.7.5"
	parityPythonCommit  = "f36e68549ceca3191e5791e5f99ea1baa1a258b5"
)

// parityProbe is a valid question for cases that exercise responses and
// retries rather than request encoding.
var parityProbe = Questions{"q": Noul{Instructions: "?"}}

type parityFixture struct {
	PythonSDK struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	} `json:"python_sdk"`
	APIKey                string   `json:"api_key"`
	BaseURL               string   `json:"base_url"`
	IgnoredRequestHeaders []string `json:"ignored_request_headers"`
	Responses             []struct {
		Name     string            `json:"name"`
		Endpoint string            `json:"endpoint"`
		Status   int               `json:"status"`
		Headers  map[string]string `json:"headers"`
		Body     string            `json:"body"`
		BodyB64  string            `json:"body_b64"`
		Python   any               `json:"python"`
	} `json:"responses"`
	Requests []struct {
		Name          string                    `json:"name"`
		Attach        []int                     `json:"attach"`
		Endpoint      string                    `json:"endpoint"`
		State         json.RawMessage           `json:"state"`
		Questions     map[string]map[string]any `json:"questions"`
		Model         *string                   `json:"model"`
		ClientModel   *string                   `json:"client_model"`
		ExtraBody     map[string]any            `json:"extra_body"`
		ClientHeaders map[string]string         `json:"client_headers"`
		Headers       map[string]string         `json:"headers"`
		Python        any                       `json:"python"`
	} `json:"requests"`
	Retries []struct {
		Name         string            `json:"name"`
		Replies      []any             `json:"replies"`
		MaxRetries   int               `json:"max_retries"`
		HTTPStatuses *[]int            `json:"http_statuses"`
		Headers      map[string]string `json:"headers"`
		Python       any               `json:"python"`
	} `json:"retries"`
	Images      []string `json:"images"` // base64 file bytes, referenced by index
	ImageInputs []struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"` // "bytes" (base64 of the bytes) or "base64" (ImageFromBase64 input)
		Value  string `json:"value"`
		Python any    `json:"python"`
	} `json:"image_inputs"`
	RetryAfter []struct {
		Name     string            `json:"name"`
		Headers  map[string]string `json:"headers"`
		PythonMS any               `json:"python_ms"`
	} `json:"retry_after"`
	APIKeys []struct {
		Name   string `json:"name"`
		Key    string `json:"key"`
		Python any    `json:"python"`
	} `json:"api_keys"`
}

type parityDifference struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"` // normalize, override, or note
	Summary string         `json:"summary"`
	Reason  string         `json:"reason"`
	Cases   map[string]any `json:"cases"`
}

// parityNormalizers maps Python's representation of a whole section to Go's.
// Each must correspond to a "normalize" entry in differences.json.
var parityNormalizers = map[string]func(section string, v any) any{
	"error-types": func(section string, v any) any {
		return walkObjects(v, func(m map[string]any) {
			class, ok := m["class"].(string)
			if !ok || isClientSection(section) {
				return
			}
			if class == "TypeSafeAPIResponseValidationError" {
				m["class"] = "ResponseValidationError"
			} else if _, isHTTP := m["status"]; isHTTP {
				m["class"] = "APIError"
			}
		})
	},
	"absent-request-id": func(_ string, v any) any {
		return walkObjects(v, func(m map[string]any) {
			if id, ok := m["request_id"]; ok && id == nil {
				m["request_id"] = ""
			}
		})
	},
	"usage-null-zero": func(_ string, v any) any {
		return walkObjects(v, func(m map[string]any) {
			if u, ok := m["usage"].(map[string]any); ok {
				for k, n := range u {
					if n == nil {
						u[k] = float64(0)
					}
				}
			}
		})
	},
	"client-error-wording": func(section string, v any) any {
		if !isClientSection(section) {
			return v
		}
		return walkObjects(v, func(m map[string]any) {
			if msg, ok := m["message"].(string); ok {
				m["message"] = foldFirst(strings.TrimSuffix(msg, "."))
			}
			delete(m, "class")
		})
	},
}

// isClientSection reports whether a fixture section's errors are client-side
// misuse errors, which have no HTTP details.
func isClientSection(section string) bool {
	return section == "requests" || section == "api_keys" || section == "image_inputs"
}

func walkObjects(v any, f func(map[string]any)) any {
	switch x := v.(type) {
	case map[string]any:
		f(x)
		for _, e := range x {
			walkObjects(e, f)
		}
	case []any:
		for _, e := range x {
			walkObjects(e, f)
		}
	}
	return v
}

// foldFirst lowercases the first rune, so "At least…" and "at least…" match
// while acronyms such as "API key" are left alone.
func foldFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || (n < len(s) && unicode.IsUpper(rune(s[n]))) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// mergePatch applies an RFC 7386 JSON merge patch.
func mergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
		} else {
			t[k] = mergePatch(t[k], v)
		}
	}
	return t
}

// canonical round-trips v through JSON so Go and Python values compare alike.
func canonical(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadParity(t *testing.T) (*parityFixture, []parityDifference) {
	t.Helper()
	raw, err := os.ReadFile("testdata/parity/python-" + parityPythonVersion + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var fx parityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.PythonSDK.Version != parityPythonVersion || fx.PythonSDK.Commit != parityPythonCommit {
		t.Fatalf("fixture generated from %+v", fx.PythonSDK)
	}
	raw, err = os.ReadFile("testdata/parity/differences.json")
	if err != nil {
		t.Fatal(err)
	}
	var diffs []parityDifference
	if err := json.Unmarshal(raw, &diffs); err != nil {
		t.Fatal(err)
	}
	return &fx, diffs
}

// parityExpectations applies the documented differences and verifies that
// each one is still needed.
type parityExpectations struct {
	t         *testing.T
	diffs     []parityDifference
	used      map[string]bool // override keys and normalizer IDs that changed something
	overrides map[string]any
}

func newParityExpectations(t *testing.T, diffs []parityDifference) *parityExpectations {
	e := &parityExpectations{t: t, diffs: diffs, used: map[string]bool{}, overrides: map[string]any{}}
	seen := map[string]bool{}
	for _, d := range diffs {
		if d.ID == "" || d.Summary == "" || d.Reason == "" || seen[d.ID] {
			t.Errorf("difference %q needs a unique id, a summary, and a reason", d.ID)
		}
		seen[d.ID] = true
		switch d.Kind {
		case "normalize":
			if parityNormalizers[d.ID] == nil {
				t.Errorf("difference %q has no normalizer in parity_test.go", d.ID)
			}
		case "override":
			if len(d.Cases) == 0 {
				t.Errorf("override %q lists no cases", d.ID)
			}
			for key, patch := range d.Cases {
				if _, dup := e.overrides[key]; dup {
					t.Errorf("case %q is overridden twice", key)
				}
				e.overrides[key] = patch
			}
		case "note":
		default:
			t.Errorf("difference %q has unknown kind %q", d.ID, d.Kind)
		}
	}
	for id := range parityNormalizers {
		if !slices.ContainsFunc(diffs, func(d parityDifference) bool { return d.ID == id && d.Kind == "normalize" }) {
			t.Errorf("normalizer %q is not documented in differences.json", id)
		}
	}
	return e
}

// expect returns the Go expectation for a case: Python's result with the
// normalizers and any override applied.
func (e *parityExpectations) expect(section, name string, python any) any {
	want := canonical(e.t, python)
	for _, d := range e.diffs {
		if d.Kind != "normalize" {
			continue
		}
		before := mustJSON(e.t, want)
		want = parityNormalizers[d.ID](section, want)
		if mustJSON(e.t, want) != before {
			e.used[d.ID] = true
		}
	}
	key := section + "/" + name
	if patch, ok := e.overrides[key]; ok {
		before := mustJSON(e.t, want)
		want = canonical(e.t, mergePatch(want, canonical(e.t, patch)))
		if mustJSON(e.t, want) == before {
			e.t.Errorf("override for %s changes nothing; remove it", key)
		}
		e.used[key] = true
	}
	return want
}

func (e *parityExpectations) checkUsed() {
	for _, d := range e.diffs {
		if d.Kind == "normalize" && !e.used[d.ID] {
			e.t.Errorf("normalizer %q no longer changes any expectation; remove it", d.ID)
		}
	}
	for key := range e.overrides {
		if !e.used[key] {
			e.t.Errorf("override for %s matches no case", key)
		}
	}
}

func parityCompare(t *testing.T, key string, got, want any) {
	t.Helper()
	got = canonical(t, got)
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("%s differs from Python %s\n--- go\n%s\n--- want (python, after documented differences)\n%s", key, parityPythonVersion, g, w)
	}
}

// describeParityError renders an SDK error in the fixture's error schema.
func describeParityError(err error) map[string]any {
	var (
		apiErr *APIError
		ve     *ResponseValidationError
	)
	switch {
	case errors.As(err, &ve):
		return map[string]any{
			"class": "ResponseValidationError", "message": ve.Error(), "status": ve.StatusCode,
			"request_id": ve.RequestID(), "endpoint": ve.Endpoint, "body": ve.Body, "field_path": ve.FieldPath,
		}
	case errors.As(err, &apiErr):
		m := map[string]any{
			"class": "APIError", "message": apiErr.Error(), "status": apiErr.StatusCode,
			"request_id": apiErr.RequestID(), "endpoint": apiErr.Endpoint, "body": apiErr.Body,
		}
		if apiErr.StatusCode == 429 { // Python exposes retry_after_ms on rate-limit errors only.
			if d, ok := apiErr.RetryAfter(); ok {
				m["retry_after_ms"] = float64(d) / float64(1e6)
			} else {
				m["retry_after_ms"] = nil
			}
		}
		return m
	default:
		return map[string]any{"class": fmt.Sprintf("%T", err), "message": err.Error()}
	}
}

// clientErrorMessage drops the Go prefix so messages compare with Python's.
func clientErrorMessage(err error) string {
	return foldFirst(strings.TrimPrefix(err.Error(), "typesafe: "))
}

func parityClient(t *testing.T, fx *parityFixture, rt http.RoundTripper, opts ...Option) *Client {
	t.Helper()
	c, err := New(append([]Option{WithAPIKey(fx.APIKey), WithBaseURL(fx.BaseURL), WithHTTPClient(&http.Client{Transport: rt})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPythonParity(t *testing.T) {
	clearEnv(t)
	fx, diffs := loadParity(t)
	e := newParityExpectations(t, diffs)
	defer e.checkUsed()

	t.Run("responses", func(t *testing.T) {
		for _, tc := range fx.Responses {
			body := []byte(tc.Body)
			if tc.BodyB64 != "" {
				var err error
				if body, err = base64.StdEncoding.DecodeString(tc.BodyB64); err != nil {
					t.Fatal(err)
				}
			}
			h := http.Header{}
			for k, v := range tc.Headers {
				h.Set(k, v)
			}
			f := newFake(reply{status: tc.Status, body: string(body), header: h})
			rec := &recordingHandler{}
			c := parityClient(t, fx, f, WithRetryPolicy(RetryPolicy{}), WithLogger(slog.New(rec)))
			got := map[string]any{}
			var err error
			if tc.Endpoint == "models" {
				var resp *ListModelsResponse
				if resp, err = c.Models.List(bg); err == nil {
					got["result"] = map[string]any{"models": resp.Models, "request_id": resp.RequestID()}
				}
			} else {
				var resp *SystemOneResponse
				if resp, err = c.SystemOne(bg, "x", parityProbe); err == nil {
					got["result"] = map[string]any{
						"model":      resp.Model,
						"usage":      resp.Usage,
						"answers":    resp.Answers,
						"request_id": resp.RequestID(),
						"groups": map[string]any{
							"nouls":   append([]string{}, slices.Sorted(maps.Keys(resp.Nouls()))...),
							"choices": append([]string{}, slices.Sorted(maps.Keys(resp.Choices()))...),
							"scores":  append([]string{}, slices.Sorted(maps.Keys(resp.Scores()))...),
							"boxes":   append([]string{}, slices.Sorted(maps.Keys(resp.Boxes()))...),
						},
					}
				}
			}
			got["ok"] = err == nil
			if err != nil {
				got["error"] = describeParityError(err)
			}
			unknown := []any{}
			for _, r := range rec.all() {
				if r.msg == "typesafe.unknown_answer_type" {
					unknown = append(unknown, []any{r.attrs["question"], r.attrs["type"]})
				}
			}
			got["unknown_answers"] = unknown
			parityCompare(t, "responses/"+tc.Name, got, e.expect("responses", tc.Name, tc.Python))
		}
	})

	t.Run("requests", func(t *testing.T) {
		for _, tc := range fx.Requests {
			images := parityImages(t, fx)
			f := newFake(ok(okBody))
			if tc.Endpoint == "models" {
				f = newFake(ok(`{"models":[]}`))
			}
			var clientOpts []Option
			if tc.ClientModel != nil {
				clientOpts = append(clientOpts, WithModel(*tc.ClientModel))
			}
			if tc.ClientHeaders != nil {
				clientOpts = append(clientOpts, WithHeaders(headerOf(tc.ClientHeaders)))
			}
			c := parityClient(t, fx, f, clientOpts...)
			var callOpts []CallOption
			if tc.Model != nil {
				callOpts = append(callOpts, WithModel(*tc.Model))
			}
			if tc.ExtraBody != nil {
				callOpts = append(callOpts, WithExtraBody(revive(tc.ExtraBody, images).(map[string]any)))
			}
			if tc.Headers != nil {
				callOpts = append(callOpts, WithHeaders(headerOf(tc.Headers)))
			}
			if tc.Attach != nil {
				var attached []Image
				for _, i := range tc.Attach {
					attached = append(attached, images[i])
				}
				callOpts = append(callOpts, WithImages(attached...))
			}
			var err error
			if tc.Endpoint == "models" {
				_, err = c.Models.List(bg, callOpts...)
			} else {
				var state any
				if err := json.Unmarshal(tc.State, &state); err != nil {
					t.Fatal(err)
				}
				_, err = c.SystemOne(bg, revive(state, images), parityQuestions(tc.Questions, images), callOpts...)
			}
			got := describeParityCall(t, fx, f, err, images)
			parityCompare(t, "requests/"+tc.Name, got, e.expect("requests", tc.Name, tc.Python))
		}
	})

	t.Run("image_inputs", func(t *testing.T) {
		for _, tc := range fx.ImageInputs {
			var (
				img Image
				err error
			)
			if tc.Kind == "bytes" {
				data, decodeErr := base64.StdEncoding.DecodeString(tc.Value)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				img, err = NewImage(data)
			} else {
				img, err = ImageFromBase64(tc.Value)
			}
			f := newFake(ok(okBody))
			if err == nil {
				_, err = parityClient(t, fx, f).SystemOne(bg, img, Questions{"q": Noul{Instructions: "Is this spam?"}})
			}
			got := describeParityCall(t, fx, f, err, []Image{img})
			parityCompare(t, "image_inputs/"+tc.Name, got, e.expect("image_inputs", tc.Name, tc.Python))
		}
	})

	t.Run("retries", func(t *testing.T) {
		for _, tc := range fx.Retries {
			var replies []reply
			for _, r := range tc.Replies {
				switch r {
				case "invalid":
					replies = append(replies, ok(`{"model":1}`))
				case float64(200):
					replies = append(replies, ok(okBody))
				default:
					replies = append(replies, reply{status: int(r.(float64))})
				}
			}
			f := &numberedTransport{replies: replies}
			p := RetryPolicy{MaxRetries: tc.MaxRetries}
			if tc.HTTPStatuses != nil {
				p.HTTPStatuses = append([]int{}, *tc.HTTPStatuses...)
			}
			opts := []Option{WithRetryPolicy(p)}
			if tc.Headers != nil {
				opts = append(opts, WithHeaders(headerOf(tc.Headers)))
			}
			c := parityClient(t, fx, f, opts...)
			_, err := c.SystemOne(bg, "x", parityProbe)
			outcome := map[string]any{"ok": err == nil}
			if err != nil {
				outcome["error"] = describeParityError(err)
			}
			got := map[string]any{"attempts": len(f.retryHeaders), "retry_count_headers": f.retryHeaders, "outcome": outcome}
			parityCompare(t, "retries/"+tc.Name, got, e.expect("retries", tc.Name, tc.Python))
		}
	})

	t.Run("retry_after", func(t *testing.T) {
		for _, tc := range fx.RetryAfter {
			var got any
			if d, ok := parseRetryAfter(headerOf(tc.Headers)); ok {
				got = float64(d) / 1e6
			}
			want := e.expect("retry_after", tc.Name, tc.PythonMS)
			if g, w := got, want; g == nil || w == nil {
				parityCompare(t, "retry_after/"+tc.Name, g, w)
			} else if math.Abs(g.(float64)-w.(float64)) > 1e-6 {
				t.Errorf("retry_after/%s: go %vms, python %vms", tc.Name, g, w)
			}
		}
	})

	t.Run("api_keys", func(t *testing.T) {
		for _, tc := range fx.APIKeys {
			_, err := New(WithAPIKey(tc.Key))
			got := map[string]any{"ok": err == nil}
			if err != nil {
				got["message"] = clientErrorMessage(err)
			}
			parityCompare(t, "api_keys/"+tc.Name, got, e.expect("api_keys", tc.Name, tc.Python))
		}
	})
}

// numberedTransport serves replies in order, repeating the last, and records
// retry headers. Error replies carry the attempt number in their message and
// request ID, as the generator's handler does.
type numberedTransport struct {
	replies      []reply
	retryHeaders []any
}

func (n *numberedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var header any
	if v := req.Header.Values(headerRetryCount); len(v) > 0 {
		header = strings.Join(v, ", ")
	}
	n.retryHeaders = append(n.retryHeaders, header)
	attempt := len(n.retryHeaders)
	rep := n.replies[min(attempt, len(n.replies))-1]
	if rep.status != 200 {
		rep.body = fmt.Sprintf(`{"error":"attempt %d"}`, attempt)
		rep.header = hdr("X-Typesafe-Request-Id", fmt.Sprintf("req-%d", attempt))
	}
	return response(req, rep), nil
}

func headerOf(m map[string]string) http.Header {
	h := http.Header{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic when keys differ only by case
	for _, k := range keys {
		h.Set(k, m[k])
	}
	return h
}

// revive replaces the generator's markers: {"$nonfinite": repr} with the NaN
// or infinity that JSON cannot carry, {"$image": i} with images[i], and
// {"$text": template} with the template's "{i}" replaced by images[i]'s
// reference.
func revive(v any, images []Image) any {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 1 {
			if repr, ok := x["$nonfinite"].(string); ok {
				return map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "-inf": math.Inf(-1)}[repr]
			}
			if i, ok := x["$image"].(float64); ok {
				return images[int(i)]
			}
			if text, ok := x["$text"].(string); ok {
				for i, img := range images {
					text = strings.ReplaceAll(text, "{"+strconv.Itoa(i)+"}", img.String())
				}
				return text
			}
		}
		for k, e := range x {
			x[k] = revive(e, images)
		}
	case []any:
		for i, e := range x {
			x[i] = revive(e, images)
		}
	}
	return v
}

// parityImages creates fresh images from the fixture's image table.
func parityImages(t *testing.T, fx *parityFixture) []Image {
	t.Helper()
	images := make([]Image, len(fx.Images))
	for i, data := range parityImageData(t, fx) {
		var err error
		if images[i], err = NewImage(data); err != nil {
			t.Fatal(err)
		}
	}
	return images
}

func parityImageData(t *testing.T, fx *parityFixture) [][]byte {
	t.Helper()
	out := make([][]byte, len(fx.Images))
	for i, b64 := range fx.Images {
		var err error
		if out[i], err = base64.StdEncoding.DecodeString(b64); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// describeParityCall renders a request case's outcome in the fixture's
// schema: the error, or the request sent. A multipart body is described as
// its JSON part plus each part's headers, with image parts identified by
// their index in the image table. Image references become <image:i>.
func describeParityCall(t *testing.T, fx *parityFixture, f *fakeTransport, err error, images []Image) any {
	t.Helper()
	got := map[string]any{"ok": err == nil}
	if err != nil {
		got["error"] = map[string]any{"message": clientErrorMessage(err)}
		got["requests_sent"] = f.count()
	} else {
		ignored := map[string]bool{}
		for _, h := range fx.IgnoredRequestHeaders {
			ignored[h] = true
		}
		r := f.all()[0]
		headers := map[string]any{}
		for k, vs := range r.Header {
			if lk := strings.ToLower(k); !ignored[lk] {
				headers[lk] = strings.Join(vs, ", ")
			}
		}
		request := map[string]any{"method": r.Method, "path": r.Path, "headers": headers}
		jsonBody := r.Body
		if mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
			headers["content-type"] = "multipart/form-data; boundary=BOUNDARY"
			var parts []any
			jsonBody, parts = describeParityParts(t, r.Body, params["boundary"], parityImageData(t, fx))
			request["parts"] = parts
		}
		var body any
		if len(jsonBody) > 0 {
			if err := json.Unmarshal(jsonBody, &body); err != nil {
				t.Fatal(err)
			}
		}
		request["body"] = body
		got["request"] = request
	}
	b, err := marshalJSON(got) // unescaped, so references match
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for i, img := range images {
		if ref := img.String(); ref != "" {
			text = strings.ReplaceAll(text, ref, "<image:"+strconv.Itoa(i)+">")
		}
	}
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func describeParityParts(t *testing.T, body []byte, boundary string, table [][]byte) (request []byte, parts []any) {
	t.Helper()
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return request, parts
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		part := map[string]any{}
		for k, vs := range p.Header {
			part[strings.ToLower(k)] = strings.Join(vs, ", ")
		}
		if p.FormName() == "request" {
			request = data
		} else {
			part["image"] = slices.IndexFunc(table, func(b []byte) bool { return bytes.Equal(b, data) })
		}
		parts = append(parts, part)
	}
}

func parityQuestions(specs map[string]map[string]any, images []Image) Questions {
	qs := Questions{}
	for name, spec := range specs {
		spec := revive(spec, images).(map[string]any)
		if raw, ok := spec["raw"].(map[string]any); ok {
			qs[name] = RawQuestion(raw)
			continue
		}
		switch spec["kind"] {
		case "noul":
			q := Noul{Instructions: spec["instructions"]}
			if c, ok := spec["criteria"].(map[string]any); ok {
				q.Criteria = &NoulCriteria{True: c["true"], False: c["false"]}
			}
			qs[name] = q
		case "choice":
			c, _ := spec["criteria"].(map[string]any)
			qs[name] = Choice{Instructions: spec["instructions"], Criteria: c}
		case "score":
			c, _ := spec["criteria"].([]any)
			if c == nil {
				c = []any{}
			}
			qs[name] = Score{Instructions: spec["instructions"], Criteria: c}
		case "box":
			qs[name] = Box{Instructions: spec["instructions"]}
		}
	}
	return qs
}
