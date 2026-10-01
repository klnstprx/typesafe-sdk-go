// Package consumer uses the SDK the way an application adapter would, through
// its exported API only. It is a separate module so the build proves the API
// is usable from outside the SDK.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/klnstprx/typesafe-sdk-go"
)

// Verdict is the application's view of a spam check.
type Verdict struct {
	Spam      bool
	Score     float64
	RequestID string
}

// spamResponse is decoded with SystemOneAs. Pointer fields tell missing or
// null values apart from a valid zero; Validate fails closed.
type spamResponse struct {
	typesafe.ResponseMeta
	Model   string `json:"model"`
	Answers struct {
		Spam *struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"spam"`
	} `json:"answers"`
}

func (r *spamResponse) Validate() error {
	switch s := r.Answers.Spam; {
	case s == nil:
		return errors.New("answers.spam is missing")
	case s.Type != "noul":
		return fmt.Errorf("answers.spam has type %q, want noul", s.Type)
	case s.Noul == nil:
		return errors.New("answers.spam.noul is missing or null")
	}
	return nil
}

// Checker wraps a TypeSafe client.
type Checker struct {
	client *typesafe.Client
}

// NewChecker builds a client from an application-owned HTTP client and logger,
// and disables SDK retries because the application retries itself.
func NewChecker(baseURL string, hc *http.Client, logger *slog.Logger) (*Checker, error) {
	c, err := typesafe.New(
		typesafe.WithAPIKey("sk-consumer"),
		typesafe.WithBaseURL(baseURL),
		typesafe.WithHTTPClient(hc),
		typesafe.WithLogger(logger),
		typesafe.WithTimeout(2*time.Second),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{MaxRetries: 0}),
	)
	if err != nil {
		return nil, err
	}
	return &Checker{client: c}, nil
}

// Close releases the SDK client.
func (c *Checker) Close() error { return c.client.Close() }

// Check asks whether text is spam.
func (c *Checker) Check(ctx context.Context, text string) (Verdict, error) {
	resp, err := typesafe.SystemOneAs[spamResponse](ctx, c.client, text, typesafe.Questions{
		"spam": typesafe.Noul{Instructions: "Is this spam?"},
	})
	if err != nil {
		return Verdict{}, err
	}
	score := *resp.Answers.Spam.Noul
	return Verdict{Spam: score >= 0.5, Score: score, RequestID: resp.RequestID()}, nil
}

// Status maps an SDK error to the HTTP status the application reports, as a
// generic provider-error mapper does: by probing HTTPStatusCode().
func Status(err error) int {
	var sc interface{ HTTPStatusCode() int }
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.As(err, &sc):
		return sc.HTTPStatusCode()
	default:
		return http.StatusBadGateway
	}
}
