// Command typedresponse decodes the response into an application type with
// SystemOneAs, validates it, and inspects its metadata. Set TYPESAFE_API_KEY
// before running.
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

// Pointer fields tell a missing or null value apart from a valid zero:
// encoding/json leaves a pointer nil for both, while 0 decodes to a non-nil
// pointer to 0.

type noulAnswer struct {
	Type string   `json:"type"`
	Noul *float64 `json:"noul"`
}

type choiceAnswer struct {
	Type          string              `json:"type"`
	Choice        *string             `json:"choice"`
	Confidence    *float64            `json:"confidence"`
	Probabilities map[string]*float64 `json:"probabilities"`
}

type moderationResponse struct {
	typesafe.ResponseMeta
	Model   string `json:"model"`
	Answers struct {
		Spam *noulAnswer   `json:"spam"`
		Tone *choiceAnswer `json:"tone"`
	} `json:"answers"`
}

// Validate runs after decoding; its error is returned wrapped in a
// *typesafe.ResponseValidationError. It fails closed: every value the program
// reads must be present, non-null, and of the expected answer type.
func (r *moderationResponse) Validate() error {
	if r.Model == "" {
		return errors.New("model is missing")
	}
	spam := r.Answers.Spam
	switch {
	case spam == nil:
		return errors.New("answers.spam is missing")
	case spam.Type != "noul":
		return fmt.Errorf("answers.spam has type %q, want noul", spam.Type)
	case spam.Noul == nil:
		return errors.New("answers.spam.noul is missing or null")
	}
	tone := r.Answers.Tone
	switch {
	case tone == nil:
		return errors.New("answers.tone is missing")
	case tone.Type != "choice":
		return fmt.Errorf("answers.tone has type %q, want choice", tone.Type)
	case tone.Choice == nil:
		return errors.New("answers.tone.choice is missing or null")
	case tone.Confidence == nil:
		return errors.New("answers.tone.confidence is missing or null")
	case tone.Probabilities == nil:
		return errors.New("answers.tone.probabilities is missing or null")
	}
	for label, p := range tone.Probabilities {
		if p == nil {
			return fmt.Errorf("answers.tone.probabilities.%s is null", label)
		}
	}
	if _, ok := tone.Probabilities[*tone.Choice]; !ok {
		return fmt.Errorf("answers.tone.probabilities lacks the chosen label %q", *tone.Choice)
	}
	return nil
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
	resp, err := typesafe.SystemOneAs[moderationResponse](ctx, client, "Buy cheap watches now!!!", typesafe.Questions{
		"spam": typesafe.Noul{Instructions: "Is this spam?"},
		"tone": typesafe.Choice{
			Instructions: "What is the tone?",
			Criteria:     map[string]any{"calm": nil, "pushy": nil},
		},
	})
	var invalid *typesafe.ResponseValidationError
	if errors.As(err, &invalid) {
		return fmt.Errorf("unexpected response (field %q): %w", invalid.FieldPath, err)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "spam: %.2f (model %s)\n", *resp.Answers.Spam.Noul, resp.Model)
	fmt.Fprintf(w, "tone: %s (p=%.2f)\n", *resp.Answers.Tone.Choice, *resp.Answers.Tone.Probabilities[*resp.Answers.Tone.Choice])
	fmt.Fprintf(w, "request id: %s\n", resp.RequestID())
	fmt.Fprintf(w, "content type: %s\n", resp.Header().Get("Content-Type"))
	fmt.Fprintf(w, "raw body: %s\n", resp.RawJSON())
	return nil
}
