package typesafe

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// marshalJSON encodes v compactly without HTML escaping.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// encodeContent encodes a state, instructions, or criteria value, which must
// encode to a JSON string, object, or array. what names the value in errors.
func encodeContent(v any, what string) (json.RawMessage, error) {
	b, err := marshalJSON(v)
	if err != nil {
		return nil, fmt.Errorf("typesafe: the request body could not be encoded as JSON: %s: %w", what, err)
	}
	if !isContent(b) {
		return nil, fmt.Errorf("typesafe: %s must encode to a JSON string, object, or array", what)
	}
	return b, nil
}

func isContent(b []byte) bool {
	switch firstByte(b) {
	case '"', '{', '[':
		return true
	}
	return false
}

// firstByte returns the first non-whitespace byte of b, or 0.
func firstByte(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c
	}
	return 0
}
