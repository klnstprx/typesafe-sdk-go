// Command systemone asks a yes/no, a choice, and a score question about a
// message. Set TYPESAFE_API_KEY before running.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/klnstprx/typesafe-sdk-go"
)

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
	resp, err := client.SystemOne(ctx, "I was charged twice. Please help.", typesafe.Questions{
		"billing": typesafe.Noul{Instructions: "Is this about billing?"},
		"tone": typesafe.Choice{
			Instructions: "What is the tone?",
			Criteria:     map[string]any{"calm": nil, "angry": "An upset or hostile message"},
		},
		"urgency": typesafe.Score{
			Instructions: "How urgent is this message?",
			Criteria:     []any{"Can wait", "Needs attention this week", "Needs attention today"},
		},
	})
	if err != nil {
		return err
	}

	// Require every answer this program uses: a missing answer, or one of a
	// type this SDK version skipped, is an error rather than a zero value.
	billing, err := answer[typesafe.NoulAnswer](resp, "billing")
	if err != nil {
		return err
	}
	tone, err := answer[typesafe.ChoiceAnswer](resp, "tone")
	if err != nil {
		return err
	}
	urgency, err := answer[typesafe.ScoreAnswer](resp, "urgency")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "model:   %s (request %s)\n", resp.Model, resp.RequestID())
	fmt.Fprintf(w, "billing: %.2f\n", billing.Noul)
	fmt.Fprintf(w, "tone:    %s (confidence %.2f)\n", tone.Choice, tone.Confidence)
	fmt.Fprintf(w, "urgency: %.2f of %d levels\n", urgency.Score, len(urgency.Legend))
	fmt.Fprintf(w, "tokens:  %d in, %d out\n", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	return nil
}

// answer returns the named answer if it is present and of type T.
func answer[T typesafe.Answer](resp *typesafe.SystemOneResponse, name string) (T, error) {
	var zero T
	a, ok := resp.Answers[name]
	if !ok {
		return zero, fmt.Errorf("answer %q is missing", name)
	}
	v, ok := a.(T)
	if !ok {
		return zero, fmt.Errorf("answer %q is a %T, want %T", name, a, zero)
	}
	return v, nil
}
