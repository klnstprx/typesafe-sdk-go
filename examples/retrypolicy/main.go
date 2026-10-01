// Command retrypolicy shows the three timeout roles and how to customize or
// disable SDK retries. Set TYPESAFE_API_KEY before running.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/klnstprx/typesafe-sdk-go"
)

func main() {
	// Start from the defaults so only the changed fields differ.
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = 4
	policy.HTTPStatuses = []int{429, 502, 503, 504}
	policy.Timeout = 8 * time.Second // retry-scheduling budget, checked between attempts

	client, err := typesafe.New(
		typesafe.WithTimeout(3*time.Second), // bounds each attempt, including the body read
		typesafe.WithRetryPolicy(policy),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// The context is the hard deadline for the whole call, waits included.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	questions := typesafe.Questions{"spam": typesafe.Noul{Instructions: "Is this spam?"}}
	resp, err := client.SystemOne(ctx, "Buy cheap watches now!!!", questions)
	report(os.Stdout, resp, err)

	// An application that already retries (or a transport that does) should
	// make exactly one SDK attempt per call.
	resp, err = client.SystemOne(ctx, "Buy cheap watches now!!!", questions,
		typesafe.WithRetryPolicy(typesafe.RetryPolicy{MaxRetries: 0}))
	report(os.Stdout, resp, err)
}

func report(w io.Writer, resp *typesafe.SystemOneResponse, err error) {
	var (
		apiErr     *typesafe.APIError
		timeoutErr *typesafe.TimeoutError
	)
	switch {
	case errors.As(err, &apiErr):
		fmt.Fprintf(w, "API error %d: %s\n", apiErr.StatusCode, apiErr.Message)
	case errors.As(err, &timeoutErr):
		fmt.Fprintf(w, "attempt timed out after %v\n", timeoutErr.Duration)
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(w, "operation deadline exceeded")
	case err != nil:
		fmt.Fprintln(w, "error:", err)
	default:
		// A missing or differently typed answer is an error, never a zero value.
		spam, ok := resp.Answers["spam"].(typesafe.NoulAnswer)
		if !ok {
			fmt.Fprintln(w, `error: answer "spam" is missing or not a noul answer`)
			return
		}
		fmt.Fprintf(w, "spam: %.2f\n", spam.Noul)
	}
}
