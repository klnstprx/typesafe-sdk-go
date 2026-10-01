// Package fakeapi serves canned TypeSafe API responses for example tests.
package fakeapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klnstprx/typesafe-sdk-go"
)

// Client returns a client, without retries, whose every request is answered
// with status and body.
func Client(t testing.TB, status int, body string) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-TypeSafe-Request-Id", "req-test")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := typesafe.New(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
