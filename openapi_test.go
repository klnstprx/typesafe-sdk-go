package typesafe

// Contract test against the vendored OpenAPI document. It detects drift
// between the spec and the SDK's wire shapes; it is not a schema validator.
// Refresh the spec with:
//
//	curl -sSf -o openapi.json https://api.typesafe.ai/openapi.json

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

type openAPISchema struct {
	Properties map[string]struct {
		Const string `json:"const"`
	} `json:"properties"`
	Required      []string `json:"required"`
	Discriminator struct {
		Mapping map[string]string `json:"mapping"`
	} `json:"discriminator"`
}

func loadOpenAPI(t *testing.T) (paths map[string]map[string]json.RawMessage, schemas map[string]openAPISchema) {
	t.Helper()
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]openAPISchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Paths, doc.Components.Schemas
}

// requiredFields is the SDK's view of each schema's required properties.
var requiredFields = map[string][]string{
	"NoulQuestion":      {"type"},
	"ChoiceQuestion":    {"type", "criteria"},
	"ScoreQuestion":     {"type", "criteria"},
	"BoxQuestion":       {"type"},
	"NoulCriteria":      {},
	"SystemOneRequest":  {"state", "model", "questions"},
	"SystemOneResponse": {"model", "answers", "usage"},
	"NoulAnswer":        {"type", "noul"},
	"ChoiceAnswer":      {"type", "choice", "confidence", "probabilities"},
	"ScoreAnswer":       {"type", "score", "confidence", "legend", "probabilities"},
	"BoxAnswer":         {"type", "box", "confidence"},
	"BoxCoordinates":    {"top", "bottom", "left", "right"},
	"Asset":             {},
	"Usage":             {"input_tokens", "output_tokens"},
	"ModelMetadata":     {"name", "description", "release_date"},
	"ModelMetadataList": {"models"},
}

// decoderTolerates lists spec-required response fields that the SDK
// deliberately accepts when absent or null. Usage and SystemOneResponse match
// the Python SDK; ModelMetadata.release_date deviates from it because
// llama-server SystemOne servers omit it (see "optional-release-date" in
// testdata/parity/differences.json).
var decoderTolerates = map[string][]string{
	"Usage":             {"input_tokens", "output_tokens"},
	"SystemOneResponse": {"answers"},
	"ModelMetadata":     {"release_date"},
}

// pendingTypes lists spec question and answer types the SDK does not decode
// yet. Unsupported answers are skipped and unsupported questions stay
// RawQuestion values.
var pendingTypes = map[string]bool{}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(m))
}

func TestOpenAPIContract(t *testing.T) {
	paths, schemas := loadOpenAPI(t)
	if _, ok := paths["/v1/systemone"]["post"]; !ok {
		t.Error("spec lacks POST /v1/systemone")
	}
	if _, ok := paths["/v1/models"]["get"]; !ok {
		t.Error("spec lacks GET /v1/models")
	}
	var post struct {
		RequestBody struct {
			Content map[string]json.RawMessage `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(paths["/v1/systemone"]["post"], &post); err != nil {
		t.Fatal(err)
	}
	for _, contentType := range []string{"application/json", "multipart/form-data"} {
		if _, ok := post.RequestBody.Content[contentType]; !ok {
			t.Errorf("POST /v1/systemone does not accept %s", contentType)
		}
	}

	samples := map[string]any{
		"NoulQuestion":      Noul{Instructions: "i", Criteria: &NoulCriteria{True: "t", False: "f"}},
		"ChoiceQuestion":    Choice{Instructions: "i", Criteria: map[string]any{"a": nil}},
		"ScoreQuestion":     Score{Instructions: "i", Criteria: []any{"low"}},
		"BoxQuestion":       Box{Instructions: "i"},
		"NoulCriteria":      NoulCriteria{True: "t", False: "f"},
		"SystemOneRequest":  SystemOneRequest{State: json.RawMessage(`"s"`), Model: "m", Questions: Questions{"q": Noul{Instructions: "i"}}, Assets: map[string]Asset{"s": {}}},
		"Asset":             Asset{Type: "image", Format: "png", DataBase64: "AA==", Part: "p"},
		"SystemOneResponse": SystemOneResponse{Model: "m"},
		"NoulAnswer":        NoulAnswer{},
		"ChoiceAnswer":      ChoiceAnswer{},
		"ScoreAnswer":       ScoreAnswer{},
		"BoxAnswer":         BoxAnswer{},
		"BoxCoordinates":    BoxCoordinates{},
		"Usage":             Usage{},
		"ModelMetadata":     ModelMetadata{},
		"ModelMetadataList": ListModelsResponse{},
	}
	for name, sample := range samples {
		t.Run(name, func(t *testing.T) {
			schema, ok := schemas[name]
			if !ok {
				t.Fatalf("schema %s missing from the spec", name)
			}
			want := slices.Sorted(maps.Keys(schema.Properties))
			if got := keysOf(t, sample); !slices.Equal(got, want) {
				t.Errorf("SDK emits %v, spec has properties %v", got, want)
			}
			if got, want := slices.Sorted(slices.Values(requiredFields[name])), slices.Sorted(slices.Values(schema.Required)); !slices.Equal(got, want) {
				t.Errorf("SDK requires %v, spec requires %v", got, want)
			}
			for _, field := range decoderTolerates[name] {
				if !slices.Contains(schema.Required, field) {
					t.Errorf("%s.%s is no longer required by the spec; drop it from decoderTolerates", name, field)
				}
			}
		})
	}
	if len(samples) != len(requiredFields) {
		t.Error("samples and requiredFields cover different schemas")
	}

	for schema, want := range map[string]string{
		"NoulQuestion": "noul", "ChoiceQuestion": "choice", "ScoreQuestion": "score", "BoxQuestion": "box",
		"NoulAnswer": "noul", "ChoiceAnswer": "choice", "ScoreAnswer": "score", "BoxAnswer": "box",
	} {
		if got := schemas[schema].Properties["type"].Const; got != want {
			t.Errorf("%s.type const = %q, want %q", schema, got, want)
		}
	}
}

// TestOpenAPITypesSupported checks that every question and answer type in
// the spec's discriminators decodes to a typed SDK value, except pendingTypes.
func TestOpenAPITypesSupported(t *testing.T) {
	_, schemas := loadOpenAPI(t)
	for _, schema := range []string{"Question", "Answer"} {
		types := slices.Sorted(maps.Keys(schemas[schema].Discriminator.Mapping))
		if len(types) == 0 {
			t.Fatalf("spec %s has no discriminator mapping", schema)
		}
		for _, typ := range types {
			var supported bool
			if schema == "Question" {
				var qs Questions
				err := json.Unmarshal([]byte(`{"q":{"type":"`+typ+`"}}`), &qs)
				_, raw := qs["q"].(RawQuestion)
				supported = err != nil || !raw // known types reject the bare question
			} else {
				supported = true
				_, _ = decodeSystemOne([]byte(`{"model":"m","usage":{},"answers":{"a":{"type":"`+typ+`"}}}`), func(string, string) { supported = false })
			}
			if supported == pendingTypes[typ] {
				t.Errorf("%s type %q: SDK supported = %v, pendingTypes says %v", schema, typ, supported, !pendingTypes[typ])
			}
		}
	}
}

// TestDecoderEnforcesRequiredFields removes each spec-required response field
// from a valid body and checks the decoder rejects it, except the documented
// tolerances.
func TestDecoderEnforcesRequiredFields(t *testing.T) {
	valid := map[string]string{
		"SystemOneResponse": `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`,
		"Usage":             `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`,
		"NoulAnswer":        `{"model":"m","usage":{},"answers":{"a":{"type":"noul","noul":1}}}`,
		"ChoiceAnswer":      `{"model":"m","usage":{},"answers":{"a":{"type":"choice","choice":"x","confidence":1,"probabilities":{}}}}`,
		"ScoreAnswer":       `{"model":"m","usage":{},"answers":{"a":{"type":"score","score":1,"confidence":1,"legend":{},"probabilities":{}}}}`,
		"BoxAnswer":         `{"model":"m","usage":{},"answers":{"a":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":null}}}`,
		"BoxCoordinates":    `{"model":"m","usage":{},"answers":{"a":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":null}}}`,
		"ModelMetadataList": `{"models":[]}`,
		"ModelMetadata":     `{"models":[{"name":"n","description":"d","release_date":"r"}]}`,
	}
	// remove deletes field from the object the schema describes within body.
	remove := func(t *testing.T, schema, body, field string) []byte {
		var doc map[string]any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		target := doc
		switch schema {
		case "Usage":
			target = doc["usage"].(map[string]any)
		case "NoulAnswer", "ChoiceAnswer", "ScoreAnswer", "BoxAnswer":
			target = doc["answers"].(map[string]any)["a"].(map[string]any)
		case "BoxCoordinates":
			target = doc["answers"].(map[string]any)["a"].(map[string]any)["box"].(map[string]any)
		case "ModelMetadata":
			target = doc["models"].([]any)[0].(map[string]any)
		}
		delete(target, field)
		b, _ := json.Marshal(doc)
		return b
	}
	for schema, body := range valid {
		for _, field := range requiredFields[schema] {
			t.Run(schema+"."+field, func(t *testing.T) {
				data := remove(t, schema, body, field)
				var err error
				if strings.HasPrefix(schema, "Model") {
					err = json.Unmarshal(data, new(ListModelsResponse))
				} else {
					err = json.Unmarshal(data, new(SystemOneResponse))
				}
				tolerated := slices.Contains(decoderTolerates[schema], field)
				if tolerated && err != nil {
					t.Errorf("tolerated field rejected: %v", err)
				}
				if !tolerated && err == nil {
					t.Errorf("decoder accepted a body without %s: %s", field, data)
				}
			})
		}
	}
}
