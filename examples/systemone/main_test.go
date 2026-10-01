package main

import (
	"context"
	"strings"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go/internal/fakeapi"
)

const (
	billing = `"billing":{"type":"noul","noul":%s}`
	tone    = `"tone":{"type":"choice","choice":"calm","confidence":%s,"probabilities":{"calm":1,"angry":0}}`
	urgency = `"urgency":{"type":"score","score":0,"confidence":1,"legend":{"0":"a","1":"b","2":"c"},"probabilities":{"0":1,"1":0,"2":0}}`
)

func body(answers ...string) string {
	return `{"model":"jev-1","usage":{"input_tokens":1,"output_tokens":1},"answers":{` + strings.Join(answers, ",") + `}}`
}

func fill(format, v string) string { return strings.Replace(format, "%s", v, 1) }

func TestZeroValuesAreValid(t *testing.T) {
	var out strings.Builder
	c := fakeapi.Client(t, 200, body(fill(billing, "0"), fill(tone, "0"), urgency))
	if err := run(context.Background(), c, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"billing: 0.00", "tone:    calm (confidence 0.00)", "urgency: 0.00 of 3 levels"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestMalformedResponsesFail(t *testing.T) {
	cases := map[string]string{
		"missing answer":     body(fill(billing, "1"), urgency),
		"unknown type":       body(fill(billing, "1"), `"tone":{"type":"ranking"}`, urgency),
		"wrong answer type":  body(fill(billing, "1"), `"tone":{"type":"noul","noul":1}`, urgency),
		"null noul":          body(fill(billing, "null"), fill(tone, "1"), urgency),
		"missing confidence": body(fill(billing, "1"), `"tone":{"type":"choice","choice":"calm","probabilities":{}}`, urgency),
		"null probability":   body(fill(billing, "1"), `"tone":{"type":"choice","choice":"calm","confidence":1,"probabilities":{"calm":null}}`, urgency),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := run(context.Background(), fakeapi.Client(t, 200, b), &out); err == nil {
				t.Errorf("run succeeded:\n%s", out.String())
			}
		})
	}
}
