package main

import (
	"context"
	"strings"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go/internal/fakeapi"
)

func TestZeroIsValid(t *testing.T) {
	var out strings.Builder
	c := fakeapi.Client(t, 200, `{"model":"m","usage":{},"answers":{"refund":{"type":"noul","noul":0}}}`)
	if err := run(context.Background(), c, &out); err != nil || out.String() != "refund requested: 0.00\n" {
		t.Fatalf("err = %v, output %q", err, out.String())
	}
}

func TestMalformedResponsesFail(t *testing.T) {
	for name, b := range map[string]string{
		"missing answer": `{"model":"m","usage":{},"answers":{}}`,
		"answers absent": `{"model":"m","usage":{}}`,
		"unknown type":   `{"model":"m","usage":{},"answers":{"refund":{"type":"future"}}}`,
		"wrong type":     `{"model":"m","usage":{},"answers":{"refund":{"type":"choice","choice":"a","confidence":1,"probabilities":{"a":1}}}}`,
		"null noul":      `{"model":"m","usage":{},"answers":{"refund":{"type":"noul","noul":null}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), fakeapi.Client(t, 200, b), &strings.Builder{}); err == nil {
				t.Error("run succeeded")
			}
		})
	}
}
