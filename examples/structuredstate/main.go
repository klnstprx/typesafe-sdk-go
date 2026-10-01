// Command structuredstate sends a struct as state and structured question
// instructions. Set TYPESAFE_API_KEY before running.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/klnstprx/typesafe-sdk-go"
)

type ticket struct {
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Customer string   `json:"customer"`
	Tags     []string `json:"tags"`
}

func main() {
	client, err := typesafe.New()
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := run(context.Background(), client, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, client *typesafe.Client, w io.Writer) error {
	state := ticket{
		Subject:  "Duplicate charge",
		Body:     "I was charged twice for my subscription this month.",
		Customer: "enterprise",
		Tags:     []string{"billing"},
	}
	resp, err := client.SystemOne(ctx, state, typesafe.Questions{
		"refund": typesafe.Noul{
			Instructions: map[string]any{"task": "Decide whether the customer asks for a refund."},
			Criteria: &typesafe.NoulCriteria{
				True:  "The customer explicitly or implicitly asks for money back.",
				False: "The customer only reports a problem.",
			},
		},
	})
	if err != nil {
		return err
	}
	// A missing or differently typed answer is an error, never a zero value.
	refund, ok := resp.Answers["refund"].(typesafe.NoulAnswer)
	if !ok {
		return errors.New(`answer "refund" is missing or not a noul answer`)
	}
	fmt.Fprintf(w, "refund requested: %.2f\n", refund.Noul)
	return nil
}
