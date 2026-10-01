package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go"
)

func decoded(t *testing.T, body string) *typesafe.SystemOneResponse {
	t.Helper()
	var r typesafe.SystemOneResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestReport(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"zero is valid":  {`{"model":"m","usage":{},"answers":{"spam":{"type":"noul","noul":0}}}`, "spam: 0.00\n"},
		"missing answer": {`{"model":"m","usage":{},"answers":{}}`, "error: answer \"spam\" is missing or not a noul answer\n"},
		"skipped type":   {`{"model":"m","usage":{},"answers":{"spam":{"type":"future"}}}`, "error: answer \"spam\" is missing or not a noul answer\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			report(&out, decoded(t, tc.body), nil)
			if out.String() != tc.want {
				t.Errorf("output = %q, want %q", out.String(), tc.want)
			}
		})
	}
}
