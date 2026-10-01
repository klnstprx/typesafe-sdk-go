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
	Required []string `json:"required"`
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
	"NoulCriteria":      {},
	"SystemOneRequest":  {"state", "model", "questions"},
	"SystemOneResponse": {"model", "answers", "usage"},
	"NoulAnswer":        {"type", "noul"},
	"ChoiceAnswer":      {"type", "choice", "confidence", "probabilities"},
	"ScoreAnswer":       {"type", "score", "confidence", "legend", "probabilities"},
	"Usage":             {"input_tokens", "output_tokens"},
	"ModelMetadata":     {"name", "description", "release_date"},
	"ModelMetadataList": {"models"},
}

// decoderTolerates lists spec-required response fields that the SDK
// deliberately accepts when absent or null, matching the Python SDK.
var decoderTolerates = map[string][]string{
	"Usage":             {"input_tokens", "output_tokens"},
	"SystemOneResponse": {"answers"},
}

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

	samples := map[string]any{
		"NoulQuestion":      Noul{Instructions: "i", Criteria: &NoulCriteria{True: "t", False: "f"}},
		"ChoiceQuestion":    Choice{Instructions: "i", Criteria: map[string]any{"a": nil}},
		"ScoreQuestion":     Score{Instructions: "i", Criteria: []any{"low"}},
		"NoulCriteria":      NoulCriteria{True: "t", False: "f"},
		"SystemOneRequest":  systemOneBody{State: json.RawMessage(`"s"`), Model: "m", Questions: map[string]json.RawMessage{}},
		"SystemOneResponse": SystemOneResponse{Model: "m"},
		"NoulAnswer":        NoulAnswer{},
		"ChoiceAnswer":      ChoiceAnswer{},
		"ScoreAnswer":       ScoreAnswer{},
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
			if got, want := keysOf(t, sample), slices.Sorted(maps.Keys(schema.Properties)); !slices.Equal(got, want) {
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
		"NoulQuestion": "noul", "ChoiceQuestion": "choice", "ScoreQuestion": "score",
		"NoulAnswer": "noul", "ChoiceAnswer": "choice", "ScoreAnswer": "score",
	} {
		if got := schemas[schema].Properties["type"].Const; got != want {
			t.Errorf("%s.type const = %q, want %q", schema, got, want)
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
		case "NoulAnswer", "ChoiceAnswer", "ScoreAnswer":
			target = doc["answers"].(map[string]any)["a"].(map[string]any)
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
