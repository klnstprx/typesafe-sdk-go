package typesafe

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Image is an image asset: PNG or JPEG file bytes. Place it anywhere in state,
// instructions, or criteria, and it is sent with the request under its
// reference, String(). To mention it inside text, format it in, as in
// fmt.Sprintf("Is %s damaged?", img), and pass it to the call with
// [WithImages].
//
// The API detects the format from the bytes and rejects formats other than
// PNG and JPEG. It requires the TypeSafe-Danger-Zone-Images: true header for
// requests with images; send it with [WithHeader].
//
// Create images with [NewImage] or [ImageFromBase64]; the zero Image is
// invalid. Images are immutable, safe for concurrent use, and reusable across
// calls. Copies are the same image.
type Image struct {
	img *imageData
}

type imageData struct {
	ref         string // "<image:" + 32 lowercase hex digits + ">"
	data        []byte
	contentType string
}

// NewImage returns an image of a copy of data, which holds the file bytes.
func NewImage(data []byte) (Image, error) {
	if len(data) == 0 {
		return Image{}, errors.New("typesafe: image data is empty")
	}
	var id [16]byte
	_, _ = rand.Read(id[:]) // never fails
	return Image{&imageData{
		ref:         "<image:" + hex.EncodeToString(id[:]) + ">",
		data:        bytes.Clone(data),
		contentType: imageContentType(data),
	}}, nil
}

// ImageFromBase64 returns an image of the standard base64 encoding of its file
// bytes, with padding and without a data: URL prefix. ASCII whitespace, such
// as the line breaks of wrapped base64, is ignored.
func ImageFromBase64(s string) (Image, error) {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t\n\r\v\f", r) {
			return -1
		}
		return r
	}, s)
	if s == "" || !isASCII(s) {
		return Image{}, errors.New("typesafe: image data must contain non-empty ASCII base64 text")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(data) == 0 {
		return Image{}, errors.New("typesafe: image data must contain valid base64 text")
	}
	return NewImage(data)
}

// String returns the image's unique reference, or "" for the zero Image.
func (i Image) String() string {
	if i.img == nil {
		return ""
	}
	return i.img.ref
}

// MarshalJSON encodes the image as its reference. It fails for the zero Image.
func (i Image) MarshalJSON() ([]byte, error) {
	if i.img == nil {
		return nil, errZeroImage
	}
	return []byte(strconv.Quote(i.img.ref)), nil
}

var errZeroImage = errors.New("typesafe: zero Image; create images with NewImage or ImageFromBase64")

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// imageContentType labels an image part by its file signature. The API
// detects the format itself, so other bytes are sent as generic binary data.
func imageContentType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	}
	return "application/octet-stream"
}

// Asset is a SystemOneRequest.Assets entry in its wire form: an image given
// either as Format and DataBase64, or, in a multipart request, as the name of
// the Part that holds its bytes.
type Asset struct {
	Type       string `json:"type,omitempty"`   // "image"
	Format     string `json:"format,omitempty"` // "png", "jpg", or "jpeg", with DataBase64
	DataBase64 string `json:"data_base64,omitempty"`
	Part       string `json:"part,omitempty"`
}

func validateAssets(assets map[string]Asset) error {
	if assets == nil {
		return nil
	}
	if len(assets) == 0 {
		return errors.New("typesafe: assets must not be empty when present")
	}
	for placeholder := range assets {
		if strings.TrimSpace(placeholder) == "" {
			return errors.New("typesafe: asset placeholders must be non-empty")
		}
	}
	return nil
}

// imageReference matches an image reference in a JSON body, including the
// \u003c and \u003e escapes that encoding/json writes by default.
var imageReference = regexp.MustCompile(`(?:<|\\u003[cC])image:([0-9a-f]{32})(?:>|\\u003[eE])`)

// referencedImages returns the images whose references appear in body, in
// order of first appearance. They are found among attached and in values,
// which are searched only when body refers to an image. With failClosed, a
// reference with no image is an error rather than plain text: a missing
// WithImages would otherwise send the image's reference without the image.
func referencedImages(body []byte, failClosed bool, attached []Image, values ...any) ([]Image, error) {
	for _, img := range attached {
		if img.img == nil {
			return nil, errZeroImage
		}
	}
	matches := imageReference.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	known := map[string]Image{}
	for _, img := range attached {
		known[img.img.ref] = img
	}
	f := imageFinder{found: known, seen: map[imageVisit]bool{}}
	for _, v := range values {
		f.walk(reflect.ValueOf(v))
	}
	var out []Image
	sent := map[string]bool{}
	for _, m := range matches {
		ref := "<image:" + string(m[1]) + ">"
		if sent[ref] {
			continue
		}
		img, ok := known[ref]
		if !ok && !failClosed {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("typesafe: image reference %q has no Image in the call; pass the Image with WithImages", ref)
		}
		sent[ref] = true
		out = append(out, img)
	}
	return out, nil
}

var imageType = reflect.TypeFor[Image]()

// imageFinder collects the Image values reachable through exported fields,
// pointers, interfaces, maps, slices, and arrays. It calls no methods, so
// images that only a custom MarshalJSON produces are not found.
type imageFinder struct {
	found map[string]Image
	seen  map[imageVisit]bool
}

type imageVisit struct {
	typ reflect.Type
	ptr uintptr
	len int
}

func (f *imageFinder) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface:
		f.walk(v.Elem())
	case reflect.Pointer:
		if !v.IsNil() && f.visit(v, 0) {
			f.walk(v.Elem())
		}
	case reflect.Struct:
		if v.Type() == imageType {
			if img, ok := v.Interface().(Image); ok && img.img != nil {
				f.found[img.img.ref] = img
			}
			return
		}
		// Like encoding/json, follow exported fields and embedded structs,
		// whose exported fields are promoted even when the type is not.
		for i := range v.NumField() {
			if sf := v.Type().Field(i); sf.IsExported() || sf.Anonymous {
				f.walk(v.Field(i))
			}
		}
	case reflect.Map:
		if !v.IsNil() && f.visit(v, 0) {
			for it := v.MapRange(); it.Next(); {
				f.walk(it.Value())
			}
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 && !v.IsNil() && f.visit(v, v.Len()) {
			f.walkElems(v)
		}
	case reflect.Array:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			f.walkElems(v)
		}
	}
}

func (f *imageFinder) walkElems(v reflect.Value) {
	for i := range v.Len() {
		f.walk(v.Index(i))
	}
}

// visit reports whether v is visited for the first time, so cycles end.
func (f *imageFinder) visit(v reflect.Value, n int) bool {
	key := imageVisit{v.Type(), v.Pointer(), n}
	if f.seen[key] {
		return false
	}
	f.seen[key] = true
	return true
}

// imagePart names the multipart part that holds the i-th image.
func imagePart(i int) string { return "asset-" + strconv.Itoa(i) }

// withImages adds images to a JSON request body: an "assets" entry naming
// each image's part, then a multipart/form-data body (RFC 7578) whose first
// part, "request", is the JSON, followed by each image's bytes.
func withImages(body []byte, images []Image) (content []byte, contentType string, err error) {
	entries := make(map[string]Asset, len(images))
	for i, img := range images {
		entries[img.img.ref] = Asset{Type: "image", Part: imagePart(i)}
	}
	assets, err := marshalJSON(entries)
	if err != nil {
		return nil, "", err
	}
	// body is a JSON object; append the entry instead of re-encoding the
	// caller's values.
	request := make([]byte, 0, len(body)+len(assets)+len(`,"assets":`))
	request = append(request, body[:len(body)-1]...)
	request = append(request, `,"assets":`...)
	request = append(request, assets...)
	request = append(request, '}')

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	parts := []struct {
		disposition, contentType string
		data                     []byte
	}{{`form-data; name="request"`, jsonContentType, request}}
	for i, img := range images {
		name := imagePart(i)
		parts = append(parts, struct {
			disposition, contentType string
			data                     []byte
		}{`form-data; name="` + name + `"; filename="` + name + `"`, img.img.contentType, img.img.data})
	}
	for _, p := range parts {
		pw, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {p.disposition}, "Content-Type": {p.contentType}})
		if err != nil {
			return nil, "", err
		}
		if _, err := pw.Write(p.data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
