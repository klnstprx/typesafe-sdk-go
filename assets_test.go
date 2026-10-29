package typesafe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"regexp"
	"strings"
	"testing"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\npixels")

func newTestImage(t *testing.T) Image {
	t.Helper()
	img, err := NewImage(pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// sentImages decodes a multipart request into its JSON part and its image
// parts' bytes keyed by the reference that names them.
func sentImages(t *testing.T, r recorded) (body map[string]any, images map[string][]byte) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
	}
	parts := map[string][]byte{}
	mr := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		parts[p.FormName()] = data
	}
	if err := json.Unmarshal(parts["request"], &body); err != nil {
		t.Fatalf("request part: %v", err)
	}
	images = map[string][]byte{}
	assets, _ := body["assets"].(map[string]any)
	for ref, a := range assets {
		images[ref] = parts[a.(map[string]any)["part"].(string)]
	}
	if len(parts) != len(images)+1 {
		t.Errorf("%d parts for %d assets", len(parts), len(images))
	}
	return body, images
}

func TestNewImage(t *testing.T) {
	data := bytes.Clone(pngBytes)
	img, err := NewImage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^<image:[0-9a-f]{32}>$`).MatchString(img.String()) {
		t.Errorf("String() = %q", img.String())
	}
	if other := newTestImage(t); other.String() == img.String() {
		t.Error("two images share a reference")
	}
	var decoded string
	if b, err := json.Marshal(img); err != nil || json.Unmarshal(b, &decoded) != nil || decoded != img.String() {
		t.Errorf("json.Marshal = %s, %v", b, err)
	}
	data[0] = 'X' // NewImage copies its input.
	f := newFake(ok(okBody))
	if _, err := fakeClient(t, f).SystemOne(bg, img, spamQuestion); err != nil {
		t.Fatal(err)
	}
	if _, images := sentImages(t, f.all()[0]); !bytes.Equal(images[img.String()], pngBytes) {
		t.Errorf("sent %q, want the bytes at construction", images[img.String()])
	}
	for _, data := range [][]byte{nil, {}} {
		if _, err := NewImage(data); err == nil || err.Error() != "typesafe: image data is empty" {
			t.Errorf("NewImage(%v) error = %v", data, err)
		}
	}
}

func TestZeroImage(t *testing.T) {
	var img Image
	if img.String() != "" {
		t.Errorf("String() = %q", img.String())
	}
	if _, err := json.Marshal(img); err == nil {
		t.Error("json.Marshal accepted a zero Image")
	}
	f := newFake(ok(okBody))
	c := fakeClient(t, f)
	for _, tc := range []struct {
		state any
		opts  []CallOption
	}{
		{state: map[string]any{"photo": img}},
		{state: "x", opts: []CallOption{WithImages(newTestImage(t), img)}},
	} {
		if _, err := c.SystemOne(bg, tc.state, spamQuestion, tc.opts...); err == nil || !strings.HasPrefix(err.Error(), "typesafe: ") {
			t.Errorf("err = %v, want a client error", err)
		}
	}
	if f.count() != 0 {
		t.Error("request sent with a zero Image")
	}
}

// TestPlacedImagesFound places images where callers keep them: by value and
// by pointer in structs, behind interfaces, in maps, slices, and arrays, and
// next to a cycle the JSON encoding skips.
func TestPlacedImagesFound(t *testing.T) {
	type Photo struct {
		Image   Image
		Caption string
	}
	type node struct {
		Next  *node `json:"-"`
		Photo *Photo
	}
	type embedded struct{ Image Image }
	type Listing struct {
		embedded
		Gallery [1]Image
	}
	imgs := make([]Image, 7)
	for i := range imgs {
		imgs[i] = newTestImage(t)
	}
	cycle := &node{Photo: &Photo{Image: imgs[1]}}
	cycle.Next = cycle
	self := []any{nil, imgs[6]}
	self[0] = map[string]any{"self": "x"}
	state := map[string]any{
		"value":   Photo{Image: imgs[0]},
		"cycle":   cycle,
		"pointer": &imgs[2],
		"slice":   []any{[]Image{imgs[3]}},
		"listing": Listing{embedded{imgs[4]}, [1]Image{imgs[5]}},
		"self":    self,
		"raw":     json.RawMessage(`"plain"`),
		"bytes":   []byte("bytes"),
	}
	f := newFake(ok(okBody))
	if _, err := fakeClient(t, f).SystemOne(bg, state, spamQuestion); err != nil {
		t.Fatal(err)
	}
	_, images := sentImages(t, f.all()[0])
	for i, img := range imgs {
		if images[img.String()] == nil {
			t.Errorf("image %d was not sent", i)
		}
	}
}

func TestImagesInQuestionsAndExtraBody(t *testing.T) {
	inQuestion, inExtra := newTestImage(t), newTestImage(t)
	f := newFake(ok(okBody))
	qs := Questions{"q": &Box{Instructions: map[string]any{"like": inQuestion}}}
	if _, err := fakeClient(t, f).SystemOne(bg, "x", qs, WithExtraBody(map[string]any{"context": []Image{inExtra}})); err != nil {
		t.Fatal(err)
	}
	if _, images := sentImages(t, f.all()[0]); len(images) != 2 || images[inQuestion.String()] == nil || images[inExtra.String()] == nil {
		t.Errorf("sent %d images", len(images))
	}
}

func TestTextReferences(t *testing.T) {
	img := newTestImage(t)
	text := fmt.Sprintf("Compare %s with the listing.", img)
	escaped, err := json.Marshal(map[string]string{"note": text}) // escapes < and > as \u003c and \u003e
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		state any
	}{
		{"text", text},
		{"escaped raw JSON", json.RawMessage(escaped)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(ok(okBody))
			c := fakeClient(t, f)
			_, err := c.SystemOne(bg, tc.state, spamQuestion)
			if want := `typesafe: image reference "` + img.String() + `" has no Image in the call; pass the Image with WithImages`; err == nil || err.Error() != want {
				t.Errorf("without WithImages: err = %v, want %q", err, want)
			}
			if f.count() != 0 {
				t.Fatal("request sent with an unknown image reference")
			}
			if _, err := c.SystemOne(bg, tc.state, spamQuestion, WithImages(img)); err != nil {
				t.Fatal(err)
			}
			if _, images := sentImages(t, f.all()[0]); !bytes.Equal(images[img.String()], pngBytes) {
				t.Errorf("images = %v", images)
			}
		})
	}
}

// TestCreateSystemOneSendsUnknownReferencesAsText covers proxies: a decoded
// request holds no Image, so a reference in its text is the client's data.
func TestCreateSystemOneSendsUnknownReferencesAsText(t *testing.T) {
	const lookalike = "<image:0123456789abcdef0123456789abcdef>"
	var req SystemOneRequest
	if err := json.Unmarshal([]byte(`{"state":"see `+lookalike+`","questions":{"q":{"type":"noul","instructions":"Is this spam?"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	f := newFake(ok(okBody), ok(okBody))
	c := fakeClient(t, f)
	if _, err := c.CreateSystemOne(bg, req); err != nil {
		t.Fatal(err)
	}
	r := f.all()[0]
	var body struct{ State string }
	if r.Header.Get("Content-Type") != jsonContentType || json.Unmarshal(r.Body, &body) != nil || body.State != "see "+lookalike {
		t.Errorf("sent %s %s", r.Header.Get("Content-Type"), r.Body)
	}

	placed, attached := newTestImage(t), newTestImage(t)
	req.Questions = Questions{"q": Box{Instructions: fmt.Sprintf("%s like %s", attached, placed)}, "p": Box{Instructions: placed}}
	if _, err := c.CreateSystemOne(bg, req, WithImages(attached)); err != nil {
		t.Fatal(err)
	}
	sent, images := sentImages(t, f.all()[1])
	if len(images) != 2 || images[placed.String()] == nil || images[attached.String()] == nil {
		t.Errorf("sent %d images", len(images))
	}
	if sent["state"] != "see "+lookalike {
		t.Errorf("state = %v", sent["state"])
	}
}

func TestImageRequestRetriesSendSameBytes(t *testing.T) {
	img := newTestImage(t)
	f := newFake(status(503), ok(okBody))
	c := fakeClient(t, f, WithRetryPolicy(RetryPolicy{MaxRetries: 1}), WithHeader("TypeSafe-Danger-Zone-Images", "true"))
	if _, err := c.SystemOne(bg, img, Questions{"q": Box{}}, WithHeader("Content-Type", "text/plain")); err != nil {
		t.Fatal(err)
	}
	first, second := f.all()[0], f.all()[1]
	if !bytes.Equal(first.Body, second.Body) || first.Header.Get("Content-Type") != second.Header.Get("Content-Type") {
		t.Error("retry sent a different body")
	}
	if got := first.Header.Get("TypeSafe-Danger-Zone-Images"); got != "true" {
		t.Errorf("TypeSafe-Danger-Zone-Images = %q", got)
	}
	if body, _ := sentImages(t, first); body["state"] != img.String() {
		t.Errorf("state = %v", body["state"])
	}
}

func TestCallerAssetsReplaceImages(t *testing.T) {
	img := newTestImage(t)
	asset := Asset{Type: "image", Format: "png", DataBase64: "AA=="}
	for name, call := range map[string]func(*Client) error{
		"extra body": func(c *Client) error {
			_, err := c.SystemOne(bg, img, spamQuestion, WithImages(img), WithExtraBody(map[string]any{"assets": map[string]Asset{"<p>": asset}}))
			return err
		},
		"request": func(c *Client) error {
			_, err := c.CreateSystemOne(bg, SystemOneRequest{State: json.RawMessage(`"<p> ` + img.String() + `"`), Questions: spamQuestion, Assets: map[string]Asset{"<p>": asset}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(ok(okBody))
			if err := call(fakeClient(t, f)); err != nil {
				t.Fatal(err)
			}
			r := f.all()[0]
			if r.Header.Get("Content-Type") != jsonContentType {
				t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			var body struct {
				Assets map[string]Asset `json:"assets"`
			}
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Assets) != 1 || body.Assets["<p>"] != asset {
				t.Errorf("assets = %+v", body.Assets)
			}
		})
	}
}

func TestSystemOneRequestAssets(t *testing.T) {
	const wire = `{"state":"<p>","questions":{"q":{"type":"box"}},"assets":{"<p>":{"type":"image","format":"png","data_base64":"AA==","part":"p"}}}`
	var req SystemOneRequest
	if err := json.Unmarshal([]byte(wire), &req); err != nil {
		t.Fatal(err)
	}
	if want := (Asset{Type: "image", Format: "png", DataBase64: "AA==", Part: "p"}); req.Assets["<p>"] != want {
		t.Errorf("Assets = %+v", req.Assets)
	}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		assets map[string]Asset
		want   string
	}{
		{map[string]Asset{}, "typesafe: assets must not be empty when present"},
		{map[string]Asset{" ": {}}, "typesafe: asset placeholders must be non-empty"},
	} {
		req.Assets = tc.assets
		if err := req.Validate(); err == nil || err.Error() != tc.want {
			t.Errorf("Validate() = %v, want %q", err, tc.want)
		}
		f := newFake(ok(okBody))
		if _, err := fakeClient(t, f).CreateSystemOne(bg, req); err == nil || f.count() != 0 {
			t.Errorf("CreateSystemOne sent assets %v", tc.assets)
		}
	}
}

func TestModelsRejectsImages(t *testing.T) {
	f := newFake(ok(`{"models":[]}`))
	_, err := fakeClient(t, f).Models.List(bg, WithImages())
	if err == nil || err.Error() != "typesafe: WithImages is not supported by Models.List" || f.count() != 0 {
		t.Errorf("err = %v", err)
	}
}

func TestImageFromBase64(t *testing.T) {
	img, err := ImageFromBase64(" iVBORw0K\r\nGgpwaXhl\tbHM=\n")
	if err != nil {
		t.Fatal(err)
	}
	f := newFake(ok(okBody))
	if _, err := fakeClient(t, f).SystemOne(bg, img, spamQuestion); err != nil {
		t.Fatal(err)
	}
	r := f.all()[0]
	if _, images := sentImages(t, r); !bytes.Equal(images[img.String()], pngBytes) {
		t.Errorf("images = %q", images)
	}
	if !strings.Contains(string(r.Body), "Content-Type: image/png") {
		t.Error("image part is not labeled image/png")
	}
	for in, want := range map[string]string{
		"":     "typesafe: image data must contain non-empty ASCII base64 text",
		"aé==": "typesafe: image data must contain non-empty ASCII base64 text",
		"ab$d": "typesafe: image data must contain valid base64 text",
	} {
		if _, err := ImageFromBase64(in); err == nil || err.Error() != want {
			t.Errorf("ImageFromBase64(%q) error = %v, want %q", in, err, want)
		}
	}
}

func TestImageRequestsNotLogged(t *testing.T) {
	img := newTestImage(t)
	rec := &recordingHandler{}
	c := fakeClient(t, newFake(ok(okBody)), WithLogger(slog.New(rec)))
	if _, err := c.SystemOne(bg, img, spamQuestion); err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.all() {
		for k, v := range r.attrs {
			if s := fmt.Sprint(v); strings.Contains(s, "image:") || strings.Contains(s, "PNG") || strings.Contains(s, "multipart") {
				t.Errorf("record %s attr %s = %q", r.msg, k, s)
			}
		}
	}
}
