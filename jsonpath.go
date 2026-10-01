package typesafe

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// member is one key/value pair of a JSON object, in document order.
type member struct {
	key   string
	value json.RawMessage
}

// decodeObject decodes a JSON object into a lookup map; ok is false when raw
// is not an object. Duplicate keys keep their last value.
func decodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	if firstByte(raw) != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	return m, json.Unmarshal(raw, &m) == nil
}

// decodeMembers splits a JSON object into its members in document order, for
// callers whose first reported error must follow the document. Duplicate keys
// keep their first position and their last value. ok is false when raw is not
// an object.
func decodeMembers(raw []byte) ([]member, bool) {
	if firstByte(raw) != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var members []member
	index := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		if i, dup := index[key]; dup {
			members[i].value = value
		} else {
			index[key] = len(members)
			members = append(members, member{key, value})
		}
	}
	return members, true
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// pathError locates the first invalid field of a response body.
type pathError struct{ path string }

func (e *pathError) Error() string { return "invalid response data at '" + e.path + "'" }

func invalidAt(path string) error { return &pathError{path} }

// pathAtOffset finds the dotted path (with [i] for array indices) of the JSON
// token at offset, as reported by *json.UnmarshalTypeError. That is the first
// token ending at or after offset: encoding/json reports the end of a scalar,
// the end of an opening delimiter, or, for an object key that does not convert
// to the map's key type, the end of the key (Go 1.26 and later) or a position
// inside it (Go 1.25).
func pathAtOffset(data []byte, offset int64) (string, bool) {
	type frame struct {
		array   bool
		index   int
		key     string
		wantKey bool
	}
	var stack []frame
	path := func() string {
		var b []byte
		for _, f := range stack {
			if f.array {
				b = append(b, '[')
				b = strconv.AppendInt(b, int64(f.index), 10)
				b = append(b, ']')
			} else {
				if len(b) > 0 {
					b = append(b, '.')
				}
				b = append(b, f.key...)
			}
		}
		return string(b)
	}
	// valueDone advances the enclosing container past a completed value.
	valueDone := func() {
		if n := len(stack); n > 0 {
			if stack[n-1].array {
				stack[n-1].index++
			} else {
				stack[n-1].wantKey = true
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil { // io.EOF or a syntax error: no value ends at offset
			return "", false
		}
		end := dec.InputOffset()
		if n := len(stack); n > 0 && !stack[n-1].array && stack[n-1].wantKey {
			if key, ok := tok.(string); ok {
				stack[n-1].key = key
				stack[n-1].wantKey = false
				if end >= offset {
					return path(), true
				}
				continue
			}
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if end >= offset {
				return path(), true
			}
			stack = append(stack, frame{array: tok == json.Delim('['), wantKey: tok == json.Delim('{')})
		case json.Delim('}'), json.Delim(']'):
			if end >= offset {
				return path(), true // the offset fell inside the closing container
			}
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			if end >= offset {
				return path(), true
			}
			valueDone()
		}
	}
}
