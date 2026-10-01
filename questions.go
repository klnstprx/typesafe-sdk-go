package typesafe

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
)

// Questions maps question names to questions. Answers use the same names.
type Questions map[string]Question

// Question is one of [Noul], [Choice], [Score], or [RawQuestion].
//
// Content fields typed any (instructions and criteria descriptions) accept any
// value that encodes to a JSON string, object, or array: a string, a struct, a
// map, a slice, or a [json.RawMessage].
type Question interface{ isQuestion() }

// Noul is a yes/no question or statement. See
// https://docs.typesafe.ai/primitives/noul.
type Noul struct {
	// Instructions is the question or statement; nil omits it.
	Instructions any
	// Criteria optionally describes what counts as yes and no; nil omits it.
	Criteria *NoulCriteria
}

// NoulCriteria describes the yes and no outcomes of a [Noul]. A nil field is
// omitted.
type NoulCriteria struct {
	True  any
	False any
}

// Choice selects one of the labels in Criteria. See
// https://docs.typesafe.ai/primitives/choice.
type Choice struct {
	// Instructions describes the decision; nil omits it.
	Instructions any
	// Criteria maps each label to its description. A nil description is sent
	// as JSON null and leaves the label undescribed. Criteria is required.
	Criteria map[string]any
}

// Score rates content against an ordered rubric. See
// https://docs.typesafe.ai/primitives/score.
type Score struct {
	// Instructions describes what to rate; nil omits it.
	Instructions any
	// Criteria lists the score levels from zero; it must be non-empty and its
	// items non-nil.
	Criteria []any
}

// RawQuestion is a question in its wire form, such as
// RawQuestion{"type": "noul", "instructions": "Is this spam?"}. It must have a
// non-empty string "type", and "criteria" when the type is "choice" or "score".
// Otherwise it is sent unchanged.
type RawQuestion map[string]any

func (Noul) isQuestion()        {}
func (Choice) isQuestion()      {}
func (Score) isQuestion()       {}
func (RawQuestion) isQuestion() {}

type questionWire struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions,omitempty"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// MarshalJSON emits {"type":"noul"} followed by the non-nil fields.
func (q Noul) MarshalJSON() ([]byte, error) { return q.encode("") }

// MarshalJSON emits {"type":"choice"} followed by the non-nil fields. It fails
// when Criteria is nil.
func (q Choice) MarshalJSON() ([]byte, error) { return q.encode("") }

// MarshalJSON emits {"type":"score"} followed by the non-nil fields. It fails
// when Criteria is empty.
func (q Score) MarshalJSON() ([]byte, error) { return q.encode("") }

// MarshalJSON emits the non-nil outcomes as "true" and "false".
func (c NoulCriteria) MarshalJSON() ([]byte, error) { return c.encode("noul criteria") }

// questionLabel names a question in errors. quotedName is the quoted question
// name in a call, or "" for a standalone json.Marshal.
func questionLabel(kind, quotedName string) string {
	if quotedName == "" {
		return kind + " question"
	}
	return kind + " question " + quotedName
}

func (q Noul) encode(quotedName string) ([]byte, error) {
	label := questionLabel("noul", quotedName)
	w := questionWire{Type: "noul"}
	var err error
	if w.Instructions, err = encodeOptional(q.Instructions, label+" instructions"); err != nil {
		return nil, err
	}
	if q.Criteria != nil {
		if w.Criteria, err = q.Criteria.encode(label + " criteria"); err != nil {
			return nil, err
		}
	}
	return marshalJSON(w)
}

func (c NoulCriteria) encode(label string) ([]byte, error) {
	var w struct {
		True  json.RawMessage `json:"true,omitempty"`
		False json.RawMessage `json:"false,omitempty"`
	}
	var err error
	if w.True, err = encodeOptional(c.True, label+`."true"`); err != nil {
		return nil, err
	}
	if w.False, err = encodeOptional(c.False, label+`."false"`); err != nil {
		return nil, err
	}
	return marshalJSON(w)
}

func (q Choice) encode(quotedName string) ([]byte, error) {
	label := questionLabel("choice", quotedName)
	if q.Criteria == nil {
		return nil, fmt.Errorf(`typesafe: %s requires "criteria"`, label)
	}
	w := questionWire{Type: "choice"}
	var err error
	if w.Instructions, err = encodeOptional(q.Instructions, label+" instructions"); err != nil {
		return nil, err
	}
	criteria := make(map[string]json.RawMessage, len(q.Criteria))
	for _, k := range slices.Sorted(maps.Keys(q.Criteria)) {
		v := q.Criteria[k]
		if v == nil {
			criteria[k] = json.RawMessage("null")
			continue
		}
		if criteria[k], err = encodeContent(v, label+" criteria "+strconv.Quote(k)); err != nil {
			return nil, err
		}
	}
	if w.Criteria, err = marshalJSON(criteria); err != nil {
		return nil, err
	}
	return marshalJSON(w)
}

func (q Score) encode(quotedName string) ([]byte, error) {
	label := questionLabel("score", quotedName)
	if len(q.Criteria) == 0 {
		return nil, scoreCriteriaError(label)
	}
	w := questionWire{Type: "score"}
	var err error
	if w.Instructions, err = encodeOptional(q.Instructions, label+" instructions"); err != nil {
		return nil, err
	}
	criteria := make([]json.RawMessage, len(q.Criteria))
	for i, v := range q.Criteria {
		if criteria[i], err = encodeContent(v, fmt.Sprintf("%s criteria[%d]", label, i)); err != nil {
			return nil, err
		}
	}
	if w.Criteria, err = marshalJSON(criteria); err != nil {
		return nil, err
	}
	return marshalJSON(w)
}

func scoreCriteriaError(label string) error {
	return fmt.Errorf("typesafe: %s has no criteria; at least one score is required", label)
}

func encodeOptional(v any, what string) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return encodeContent(v, what)
}

// normalizeQuestions validates and encodes each question. Questions are
// visited in name order, so the first error reported is deterministic.
func normalizeQuestions(qs Questions) (map[string]json.RawMessage, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("typesafe: at least one question is required")
	}
	out := make(map[string]json.RawMessage, len(qs))
	for _, name := range slices.Sorted(maps.Keys(qs)) {
		b, err := encodeQuestion(name, qs[name])
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, nil
}

func encodeQuestion(name string, q Question) ([]byte, error) {
	quoted := strconv.Quote(name)
	switch v := q.(type) {
	case Noul:
		return v.encode(quoted)
	case *Noul:
		if v != nil {
			return v.encode(quoted)
		}
	case Choice:
		return v.encode(quoted)
	case *Choice:
		if v != nil {
			return v.encode(quoted)
		}
	case Score:
		return v.encode(quoted)
	case *Score:
		if v != nil {
			return v.encode(quoted)
		}
	case RawQuestion:
		return encodeRaw(name, v)
	case *RawQuestion:
		if v != nil {
			return encodeRaw(name, *v)
		}
	}
	return nil, fmt.Errorf(`typesafe: question %q must be a Noul, Choice, Score, or RawQuestion with a non-empty string "type"`, name)
}

func encodeRaw(name string, q RawQuestion) ([]byte, error) {
	typ, ok := q["type"].(string)
	if !ok || typ == "" {
		return nil, fmt.Errorf(`typesafe: question %q must be a Noul, Choice, Score, or RawQuestion with a non-empty string "type"`, name)
	}
	if typ == "choice" || typ == "score" {
		criteria, ok := q["criteria"]
		if !ok {
			return nil, fmt.Errorf(`typesafe: question %q requires "criteria"`, name)
		}
		if typ == "score" && isEmpty(criteria) {
			return nil, scoreCriteriaError("score question " + strconv.Quote(name))
		}
	}
	b, err := marshalJSON(map[string]any(q))
	if err != nil {
		return nil, fmt.Errorf("typesafe: the request body could not be encoded as JSON: question %q: %w", name, err)
	}
	return b, nil
}

// isEmpty mirrors Python truthiness for a criteria value: nil, false, numeric
// zero (including -0.0), and empty strings, slices, arrays, and maps are empty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return rv.Complex() == 0
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
