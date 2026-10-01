package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go"
	"github.com/klnstprx/typesafe-sdk-go/internal/fakeapi"
)

const (
	spamOK = `"spam":{"type":"noul","noul":0}`
	toneOK = `"tone":{"type":"choice","choice":"calm","confidence":0,"probabilities":{"calm":0,"pushy":1}}`
)

func body(answers ...string) string {
	return `{"model":"jev-1","answers":{` + strings.Join(answers, ",") + `}}`
}

func TestZeroValuesAreValid(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), fakeapi.Client(t, 200, body(spamOK, toneOK)), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"spam: 0.00 (model jev-1)", "tone: calm (p=0.00)", "request id: req-test"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestMalformedResponsesFail(t *testing.T) {
	cases := map[string]string{
		"missing spam":           body(toneOK),
		"spam wrong type":        body(`"spam":{"type":"choice","noul":1}`, toneOK),
		"spam noul missing":      body(`"spam":{"type":"noul"}`, toneOK),
		"spam noul null":         body(`"spam":{"type":"noul","noul":null}`, toneOK),
		"spam noul string":       body(`"spam":{"type":"noul","noul":"high"}`, toneOK),
		"missing tone":           body(spamOK),
		"tone wrong type":        body(spamOK, `"tone":{"type":"score","choice":"calm","confidence":1,"probabilities":{"calm":1}}`),
		"tone choice null":       body(spamOK, `"tone":{"type":"choice","choice":null,"confidence":1,"probabilities":{"calm":1}}`),
		"tone confidence absent": body(spamOK, `"tone":{"type":"choice","choice":"calm","probabilities":{"calm":1}}`),
		"probabilities missing":  body(spamOK, `"tone":{"type":"choice","choice":"calm","confidence":1}`),
		"probabilities null":     body(spamOK, `"tone":{"type":"choice","choice":"calm","confidence":1,"probabilities":null}`),
		"probability null":       body(spamOK, `"tone":{"type":"choice","choice":"calm","confidence":1,"probabilities":{"calm":1,"pushy":null}}`),
		"chosen label absent":    body(spamOK, `"tone":{"type":"choice","choice":"calm","confidence":1,"probabilities":{"pushy":1}}`),
		"model missing":          `{"answers":{` + spamOK + `,` + toneOK + `}}`,
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			err := run(context.Background(), fakeapi.Client(t, 200, b), &out)
			var invalid *typesafe.ResponseValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("err = %v, want a *ResponseValidationError (output %q)", err, out.String())
			}
		})
	}
}
