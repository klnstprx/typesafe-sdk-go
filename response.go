package typesafe

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
)

// ResponseMeta carries HTTP metadata of a decoded response. Embed it in a
// custom response type passed to [SystemOneAs] to receive it. Header and RawJSON
// return independent copies. RawHTTPResponse returns a diagnostic copy with
// shared, read-only TLS state; see its documentation for ownership details.
type ResponseMeta struct {
	requestID string
	header    http.Header
	raw       []byte
	resp      *http.Response // shallow snapshot without Body; nil without HTTP metadata
}

// RequestID returns the x-typesafe-request-id response header, or "" if absent.
func (m ResponseMeta) RequestID() string { return m.requestID }

// Header returns a copy of the response headers, or nil without HTTP metadata.
func (m ResponseMeta) Header() http.Header { return m.header.Clone() }

// RawJSON returns a copy of the response body exactly as received, including
// fields and answer types the SDK does not model.
func (m ResponseMeta) RawJSON() json.RawMessage { return bytes.Clone(m.raw) }

// RawHTTPResponse returns a diagnostic copy of the HTTP response: cloned
// headers and trailers, a fresh in-memory Body reading the full payload (each
// call starts at byte zero), and a clone of the originating Request with its
// own headers, URL, and a re-readable Body. It holds no network connection.
// Only TLS is shared; do not modify it. The Request headers include the
// Authorization credential, so never log or format it whole. It returns nil
// without HTTP metadata.
func (m ResponseMeta) RawHTTPResponse() *http.Response {
	if m.resp == nil {
		return nil
	}
	r := *m.resp
	r.Header = m.header.Clone()
	r.Trailer = m.resp.Trailer.Clone()
	r.Body = io.NopCloser(bytes.NewReader(m.raw))
	if req := m.resp.Request; req != nil {
		r.Request = req.Clone(req.Context())
		r.Request.Body = nil
		if req.GetBody != nil {
			r.Request.Body, _ = req.GetBody()
		}
	}
	return &r
}

func (m *ResponseMeta) setMeta(meta ResponseMeta) { *m = meta }

// metaSetter is implemented by every type that embeds ResponseMeta.
type metaSetter interface{ setMeta(ResponseMeta) }

func newMeta(resp *http.Response, raw []byte) ResponseMeta {
	snapshot := *resp
	snapshot.Body = nil
	header := resp.Header.Clone()
	snapshot.Header = header
	snapshot.Trailer = resp.Trailer.Clone()
	return ResponseMeta{
		requestID: resp.Header.Get(headerRequestID),
		header:    header,
		raw:       bytes.Clone(raw),
		resp:      &snapshot,
	}
}

// SystemOneResponse holds the answers to a [Client.SystemOne] call.
//
// Decoding with [json.Unmarshal] applies the same validation as the client and
// returns a *ResponseValidationError for invalid data. Answers with types this
// SDK does not model are skipped; [ResponseMeta.RawJSON] still has them.
type SystemOneResponse struct {
	ResponseMeta
	// Model is the model that answered, which may differ from the requested alias.
	Model string `json:"model"`
	// Usage reports token counts.
	Usage Usage `json:"usage"`
	// Answers maps question names to [NoulAnswer], [ChoiceAnswer], or [ScoreAnswer] values.
	Answers map[string]Answer `json:"answers"`
}

// Nouls returns the yes/no answers keyed by question name.
func (r *SystemOneResponse) Nouls() map[string]NoulAnswer { return answersOf[NoulAnswer](r.Answers) }

// Choices returns the choice answers keyed by question name. Probabilities maps
// are shared with Answers; do not mutate them.
func (r *SystemOneResponse) Choices() map[string]ChoiceAnswer {
	return answersOf[ChoiceAnswer](r.Answers)
}

// Scores returns the score answers keyed by question name. Legend and
// Probabilities maps are shared with Answers; do not mutate them.
func (r *SystemOneResponse) Scores() map[string]ScoreAnswer { return answersOf[ScoreAnswer](r.Answers) }

func answersOf[T Answer](answers map[string]Answer) map[string]T {
	out := map[string]T{}
	for name, a := range answers {
		if v, ok := a.(T); ok {
			out[name] = v
		}
	}
	return out
}

// Answer is one of [NoulAnswer], [ChoiceAnswer], or [ScoreAnswer].
type Answer interface{ isAnswer() }

// NoulAnswer is the answer to a [Noul] question.
type NoulAnswer struct {
	// Noul is the probability of yes or true, from 0 to 1.
	Noul float64 `json:"noul"`
}

// ChoiceAnswer is the answer to a [Choice] question.
type ChoiceAnswer struct {
	// Choice is the most probable label.
	Choice string `json:"choice"`
	// Confidence in the selection, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Probabilities maps every label to its probability.
	Probabilities map[string]float64 `json:"probabilities"`
}

// ScoreAnswer is the answer to a [Score] question.
type ScoreAnswer struct {
	// Score is the probability-weighted average level; it may fall between levels.
	Score float64 `json:"score"`
	// Confidence in the score, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Legend maps each level to its criteria description: a string,
	// map[string]any, or []any.
	Legend map[int]any `json:"legend"`
	// Probabilities maps each level to its probability.
	Probabilities map[int]float64 `json:"probabilities"`
}

// Usage reports token counts; a count the API omits or nulls is 0.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (NoulAnswer) isAnswer()   {}
func (ChoiceAnswer) isAnswer() {}
func (ScoreAnswer) isAnswer()  {}

// MarshalJSON emits the answer with "type":"noul".
func (a NoulAnswer) MarshalJSON() ([]byte, error) {
	type wire NoulAnswer
	return marshalJSON(struct {
		Type string `json:"type"`
		wire
	}{"noul", wire(a)})
}

// MarshalJSON emits the answer with "type":"choice".
func (a ChoiceAnswer) MarshalJSON() ([]byte, error) {
	type wire ChoiceAnswer
	if a.Probabilities == nil {
		a.Probabilities = map[string]float64{}
	}
	return marshalJSON(struct {
		Type string `json:"type"`
		wire
	}{"choice", wire(a)})
}

// MarshalJSON emits the answer with "type":"score" and string level keys.
func (a ScoreAnswer) MarshalJSON() ([]byte, error) {
	type wire ScoreAnswer
	if a.Legend == nil {
		a.Legend = map[int]any{}
	}
	if a.Probabilities == nil {
		a.Probabilities = map[int]float64{}
	}
	return marshalJSON(struct {
		Type string `json:"type"`
		wire
	}{"score", wire(a)})
}

// MarshalJSON emits model, answers (each with its type), and usage.
func (r SystemOneResponse) MarshalJSON() ([]byte, error) {
	answers := r.Answers
	if answers == nil {
		answers = map[string]Answer{}
	}
	return marshalJSON(struct {
		Model   string            `json:"model"`
		Answers map[string]Answer `json:"answers"`
		Usage   Usage             `json:"usage"`
	}{r.Model, answers, r.Usage})
}

// UnmarshalJSON validates and decodes a response body. Invalid data yields a
// *ResponseValidationError naming the offending field. Unknown answer types are
// skipped silently.
func (r *SystemOneResponse) UnmarshalJSON(data []byte) error {
	decoded, err := decodeSystemOne(data, nil)
	if err != nil {
		return standaloneValidationError(data, err)
	}
	*r = *decoded
	r.ResponseMeta = ResponseMeta{raw: bytes.Clone(data)}
	return nil
}

func standaloneValidationError(data []byte, err error) error {
	pe, ok := err.(*pathError)
	if !ok {
		return err
	}
	return &ResponseValidationError{Body: decodeBody(data), RawBody: bytes.Clone(data), FieldPath: pe.path}
}

// decodeSystemOne validates a SystemOne body in the order the Python SDK does:
// the body shape and every answer's type first, then model, usage, and the
// fields of each known answer in document order. onUnknown, if set, is called
// for each skipped answer.
func decodeSystemOne(data []byte, onUnknown func(name, typ string)) (*SystemOneResponse, error) {
	top, ok := decodeObject(data)
	if !ok {
		return nil, invalidAt("")
	}

	type typedAnswer struct {
		name, typ string
		fields    map[string]json.RawMessage
	}
	var answers []typedAnswer
	rawAnswers, hasAnswers := top["answers"]
	answerMembers, answersIsObject := decodeMembers(rawAnswers)
	if hasAnswers && answersIsObject {
		for _, m := range answerMembers {
			path := joinPath("answers", m.key)
			fields, ok := decodeObject(m.value)
			if !ok {
				return nil, invalidAt(path + ".type")
			}
			typ, ok := decodeString(fields["type"])
			if !ok {
				return nil, invalidAt(path + ".type")
			}
			switch typ {
			case "noul", "choice", "score":
				answers = append(answers, typedAnswer{m.key, typ, fields})
			default:
				if onUnknown != nil {
					onUnknown(m.key, typ)
				}
			}
		}
	}

	var r SystemOneResponse
	if r.Model, ok = decodeString(top["model"]); !ok {
		return nil, invalidAt("model")
	}
	usage, ok := decodeObject(top["usage"])
	if !ok {
		return nil, invalidAt("usage")
	}
	for _, field := range [...]struct {
		key string
		dst *int
	}{{"input_tokens", &r.Usage.InputTokens}, {"output_tokens", &r.Usage.OutputTokens}} {
		raw, present := usage[field.key]
		if !present || firstByte(raw) == 'n' { // absent or null counts are 0
			continue
		}
		if *field.dst, ok = decodeInt(raw); !ok {
			return nil, invalidAt("usage." + field.key)
		}
	}
	if hasAnswers && !answersIsObject {
		return nil, invalidAt("answers")
	}

	r.Answers = make(map[string]Answer, len(answers))
	for _, a := range answers {
		answer, err := decodeAnswer(joinPath("answers", a.name), a.typ, a.fields)
		if err != nil {
			return nil, err
		}
		r.Answers[a.name] = answer
	}
	return &r, nil
}

func decodeAnswer(path, typ string, f map[string]json.RawMessage) (Answer, error) {
	var ok bool
	switch typ {
	case "noul":
		var a NoulAnswer
		if a.Noul, ok = decodeNumber(f["noul"]); !ok {
			return nil, invalidAt(path + ".noul")
		}
		return a, nil
	case "choice":
		var a ChoiceAnswer
		if a.Choice, ok = decodeString(f["choice"]); !ok {
			return nil, invalidAt(path + ".choice")
		}
		if a.Confidence, ok = decodeNumber(f["confidence"]); !ok {
			return nil, invalidAt(path + ".confidence")
		}
		members, ok := decodeMembers(f["probabilities"])
		if !ok {
			return nil, invalidAt(path + ".probabilities")
		}
		a.Probabilities = make(map[string]float64, len(members))
		for _, m := range members {
			if a.Probabilities[m.key], ok = decodeNumber(m.value); !ok {
				return nil, invalidAt(path + ".probabilities." + m.key)
			}
		}
		return a, nil
	default:
		var a ScoreAnswer
		if a.Score, ok = decodeNumber(f["score"]); !ok {
			return nil, invalidAt(path + ".score")
		}
		if a.Confidence, ok = decodeNumber(f["confidence"]); !ok {
			return nil, invalidAt(path + ".confidence")
		}
		legend, ok := decodeMembers(f["legend"])
		if !ok {
			return nil, invalidAt(path + ".legend")
		}
		a.Legend = make(map[int]any, len(legend))
		for _, m := range legend {
			level, err := strconv.Atoi(m.key)
			if err != nil || !isContent(m.value) {
				return nil, invalidAt(path + ".legend." + m.key)
			}
			var v any
			if err := json.Unmarshal(m.value, &v); err != nil {
				return nil, invalidAt(path + ".legend." + m.key)
			}
			a.Legend[level] = v
		}
		probs, ok := decodeMembers(f["probabilities"])
		if !ok {
			return nil, invalidAt(path + ".probabilities")
		}
		a.Probabilities = make(map[int]float64, len(probs))
		for _, m := range probs {
			level, err := strconv.Atoi(m.key)
			if err != nil {
				return nil, invalidAt(path + ".probabilities." + m.key)
			}
			if a.Probabilities[level], ok = decodeNumber(m.value); !ok {
				return nil, invalidAt(path + ".probabilities." + m.key)
			}
		}
		return a, nil
	}
}

func decodeString(raw json.RawMessage) (string, bool) {
	if firstByte(raw) != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

func isNumber(raw json.RawMessage) bool {
	c := firstByte(raw)
	return c == '-' || (c >= '0' && c <= '9')
}

// decodeNumber accepts any finite JSON number, integers included.
func decodeNumber(raw json.RawMessage) (float64, bool) {
	if !isNumber(raw) {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// decodeInt accepts a JSON integer that fits in int.
func decodeInt(raw json.RawMessage) (int, bool) {
	if !isNumber(raw) {
		return 0, false
	}
	var n int
	return n, json.Unmarshal(raw, &n) == nil
}

// ListModelsResponse holds the models available to the account.
type ListModelsResponse struct {
	ResponseMeta
	Models []ModelMetadata `json:"models"`
}

// ModelMetadata describes one model.
type ModelMetadata struct {
	// Name is the model name or alias accepted by [WithModel].
	Name string `json:"name"`
	// Description describes the model and its capabilities.
	Description string `json:"description"`
	// ReleaseDate is formatted as YYYY-MM-DD.
	ReleaseDate string `json:"release_date"`
}

// MarshalJSON emits {"models":[...]}.
func (r ListModelsResponse) MarshalJSON() ([]byte, error) {
	models := r.Models
	if models == nil {
		models = []ModelMetadata{}
	}
	return marshalJSON(struct {
		Models []ModelMetadata `json:"models"`
	}{models})
}

// UnmarshalJSON validates and decodes a models body, returning a
// *ResponseValidationError for invalid data.
func (r *ListModelsResponse) UnmarshalJSON(data []byte) error {
	decoded, err := decodeModels(data)
	if err != nil {
		return standaloneValidationError(data, err)
	}
	*r = *decoded
	r.ResponseMeta = ResponseMeta{raw: bytes.Clone(data)}
	return nil
}

func decodeModels(data []byte) (*ListModelsResponse, error) {
	top, ok := decodeObject(data)
	if !ok {
		return nil, invalidAt("")
	}
	raw := top["models"]
	if firstByte(raw) != '[' {
		return nil, invalidAt("models")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, invalidAt("models")
	}
	r := &ListModelsResponse{Models: make([]ModelMetadata, len(items))}
	for i, item := range items {
		path := "models[" + strconv.Itoa(i) + "]"
		f, ok := decodeObject(item)
		if !ok {
			return nil, invalidAt(path)
		}
		m := &r.Models[i]
		for _, field := range [...]struct {
			key string
			dst *string
		}{{"name", &m.Name}, {"description", &m.Description}, {"release_date", &m.ReleaseDate}} {
			if *field.dst, ok = decodeString(f[field.key]); !ok {
				return nil, invalidAt(path + "." + field.key)
			}
		}
	}
	return r, nil
}
