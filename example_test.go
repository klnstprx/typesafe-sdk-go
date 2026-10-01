package typesafe_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/klnstprx/typesafe-sdk-go"
)

// fakeAPI stands in for https://api.typesafe.ai so the examples run offline.
func fakeAPI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-TypeSafe-Request-Id", "req-123")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"models":[{"name":"jev-latest","description":"General-purpose system one model.","release_date":"2026-09-15"}]}`)
		case "/v1/systemone":
			if r.Header.Get("X-Demo") == "rate-limit" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"Rate limit exceeded"}`)
				return
			}
			_, _ = io.WriteString(w, `{"model":"jev-1","usage":{"input_tokens":120,"output_tokens":12},"answers":{`+
				`"billing":{"type":"noul","noul":0.98},`+
				`"tone":{"type":"choice","choice":"angry","confidence":0.9,"probabilities":{"angry":0.9,"calm":0.1}}}}`)
		}
	}))
}

func ExampleClient_SystemOne() {
	srv := fakeAPI()
	defer srv.Close()

	client, err := typesafe.New(typesafe.WithAPIKey("sk-example"), typesafe.WithBaseURL(srv.URL))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	resp, err := client.SystemOne(context.Background(), "I was charged twice. Please help.", typesafe.Questions{
		"billing": typesafe.Noul{Instructions: "Is this about billing?"},
		"tone": typesafe.Choice{
			Instructions: "What is the tone?",
			Criteria:     map[string]any{"calm": nil, "angry": "An upset or hostile message"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	// Require the answers you depend on, with their types: a missing answer,
	// or one of a type this SDK version skipped, is not a safe zero value.
	billing, ok := resp.Answers["billing"].(typesafe.NoulAnswer)
	if !ok {
		log.Fatal(`answer "billing" is missing or not a noul answer`)
	}
	tone, ok := resp.Answers["tone"].(typesafe.ChoiceAnswer)
	if !ok {
		log.Fatal(`answer "tone" is missing or not a choice answer`)
	}
	fmt.Printf("billing=%.2f tone=%s request=%s\n", billing.Noul, tone.Choice, resp.RequestID())
	// Output: billing=0.98 tone=angry request=req-123
}

func ExampleSystemOneAs() {
	srv := fakeAPI()
	defer srv.Close()
	client, _ := typesafe.New(typesafe.WithAPIKey("sk-example"), typesafe.WithBaseURL(srv.URL))
	defer client.Close()

	// billingResponse (below) uses pointer fields and Validate to fail closed.
	resp, err := typesafe.SystemOneAs[billingResponse](context.Background(), client, "I was charged twice.", typesafe.Questions{
		"billing": typesafe.Noul{Instructions: "Is this about billing?"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(*resp.Answers.Billing.Noul, resp.RequestID())
	// Output: 0.98 req-123
}

// billingResponse is a custom response type for SystemOneAs. Pointer fields
// tell a missing or null value apart from a valid zero, and Validate requires
// the answer and its type.
type billingResponse struct {
	typesafe.ResponseMeta
	Answers struct {
		Billing *struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"billing"`
	} `json:"answers"`
}

func (r *billingResponse) Validate() error {
	b := r.Answers.Billing
	switch {
	case b == nil:
		return errors.New("answers.billing is missing")
	case b.Type != "noul":
		return fmt.Errorf("answers.billing has type %q, want noul", b.Type)
	case b.Noul == nil:
		return errors.New("answers.billing.noul is missing or null")
	}
	return nil
}

func ExampleModelsService_List() {
	srv := fakeAPI()
	defer srv.Close()
	client, _ := typesafe.New(typesafe.WithAPIKey("sk-example"), typesafe.WithBaseURL(srv.URL))
	defer client.Close()

	resp, err := client.Models.List(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range resp.Models {
		fmt.Println(m.Name, m.ReleaseDate)
	}
	// Output: jev-latest 2026-09-15
}

func ExampleAPIError() {
	srv := fakeAPI()
	defer srv.Close()
	client, _ := typesafe.New(
		typesafe.WithAPIKey("sk-example"),
		typesafe.WithBaseURL(srv.URL),
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{MaxRetries: 0}),
	)
	defer client.Close()

	_, err := client.SystemOne(context.Background(), "hello", typesafe.Questions{"spam": typesafe.Noul{}},
		typesafe.WithHeader("X-Demo", "rate-limit"))
	var apiErr *typesafe.APIError
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Message, apiErr.RequestID())
	}
	// Output: 429 Rate limit exceeded req-123
}

func ExampleRetryPolicy() {
	// Keep the defaults but retry more often, within a 10s scheduling budget.
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = 4
	policy.Timeout = 10 * time.Second

	client, err := typesafe.New(typesafe.WithAPIKey("sk-example"), typesafe.WithRetryPolicy(policy))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// An application that already retries disables SDK retries instead:
	_ = typesafe.WithRetryPolicy(typesafe.RetryPolicy{MaxRetries: 0})
	fmt.Println(client)
	// Output: typesafe.Client
}
