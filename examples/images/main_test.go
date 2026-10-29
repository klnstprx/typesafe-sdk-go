package main

import (
	"context"
	"strings"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go/internal/fakeapi"
)

var photo = []byte("\xff\xd8\xff photo")

func body(answers string) string {
	return `{"model":"jev-preview","usage":{"input_tokens":1,"output_tokens":1},"answers":{` + answers + `}}`
}

const damaged = `"damaged":{"type":"noul","noul":0.9}`

func TestLocatesDamage(t *testing.T) {
	for name, tc := range map[string]struct {
		box  string
		want []string
		not  string
	}{
		"with confidence":    {`"damage":{"type":"box","box":{"top":0.2,"bottom":0.66,"left":0.19,"right":0.68},"confidence":0.8}`, []string{"damaged: 0.90", "top 0.20, bottom 0.66, left 0.19, right 0.68", "confidence: 0.80"}, ""},
		"without confidence": {`"damage":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":null}`, []string{"top 0.00, bottom 1.00"}, "confidence"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := run(context.Background(), fakeapi.Client(t, 200, body(damaged+","+tc.box)), photo, &out); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
			if tc.not != "" && strings.Contains(out.String(), tc.not) {
				t.Errorf("output has %q:\n%s", tc.not, out.String())
			}
		})
	}
}

func TestFailures(t *testing.T) {
	cases := map[string]struct {
		data []byte
		body string
	}{
		"empty photo":     {nil, body(damaged)},
		"missing box":     {photo, body(damaged)},
		"box null":        {photo, body(damaged + `,"damage":{"type":"box","box":null,"confidence":null}`)},
		"confidence gone": {photo, body(damaged + `,"damage":{"type":"box","box":{"top":0,"bottom":1,"left":0,"right":1}}`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := run(context.Background(), fakeapi.Client(t, 200, tc.body), tc.data, &out); err == nil {
				t.Errorf("run succeeded:\n%s", out.String())
			}
		})
	}
}
