// Package typesafe is a Go client for the TypeSafe AI API (https://typesafe.ai).
//
// It matches the observable behavior of the official Python SDK: the same wire
// format, request validation, retry rules, and error information.
//
// # Quickstart
//
//	client, err := typesafe.New() // reads TYPESAFE_API_KEY
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
//	resp, err := client.SystemOne(ctx, "I was charged twice. Please help.", typesafe.Questions{
//		"billing": typesafe.Noul{Instructions: "Is this about billing?"},
//		"tone": typesafe.Choice{
//			Instructions: "What is the tone?",
//			Criteria:     map[string]any{"calm": nil, "angry": nil},
//		},
//	})
//	if err != nil {
//		return err
//	}
//	// Require each answer and its type; a missing answer is not a safe zero.
//	billing, ok := resp.Answers["billing"].(typesafe.NoulAnswer)
//	if !ok {
//		return errors.New("billing answer missing")
//	}
//	tone, ok := resp.Answers["tone"].(typesafe.ChoiceAnswer)
//	if !ok {
//		return errors.New("tone answer missing")
//	}
//	fmt.Println(billing.Noul, tone.Choice)
//
// # Timeouts and retries
//
// The caller's context bounds network I/O and retry waits across the whole call.
// [WithTimeout] bounds each attempt's network I/O, including body reads (default
// 10s, or the supplied HTTP client's timeout when no SDK timeout is set). An
// explicit timeout must be positive; zero is an error. JSON processing and
// custom hooks run synchronously and cannot be interrupted; see [SystemOneAs].
// [RetryPolicy.Timeout] is a retry-scheduling budget checked between attempts;
// it does not interrupt an attempt in flight.
//
// The SDK retries by default (see [DefaultRetryPolicy]). An application that
// already retries should pass WithRetryPolicy(RetryPolicy{MaxRetries: 0}).
//
// # Errors
//
// Use [errors.As] with *[APIError], *[ResponseValidationError],
// *[ConnectionError], and *[TimeoutError]. APIError and ResponseValidationError
// implement HTTPStatusCode() int. Client-side misuse returns plain errors
// prefixed "typesafe: ". Use [errors.Is] to detect caller cancellation, which is
// wrapped in ConnectionError during an attempt or validation and returned
// directly during a retry wait. Transport error causes redact request
// credentials; server content in APIError and ResponseValidationError, and
// custom validation errors, are preserved without redaction.
//
// # Logging
//
// The SDK is silent unless a non-nil logger is passed to [WithLogger]. It never
// uses the process default logger or writes directly to stdout or stderr; the
// injected handler controls filtering and output. Records use the call's context,
// routine attempts and retries log at DEBUG, and unknown answer types at WARN.
// Bodies, headers, credentials, and returned errors are never logged.
//
// # HTTP clients
//
// [WithHTTPClient] uses a copy of the supplied client, so its transport can be
// wrapped for tracing or metrics. The SDK never follows redirects and never
// closes a supplied transport.
//
// # Ownership
//
// Create clients with [New]; the zero value is unusable. Clients support
// concurrent calls and must not be copied after first use. [Client.Close] does
// not cancel calls in progress. Options are reusable; [WithHeaders] copies its
// map and slices, [WithRetryPolicy] copies its status slice, and [WithExtraBody]
// copies its top-level map at option creation. Keep referenced state, question,
// and nested extra-body values unchanged while a call runs.
//
// [ResponseMeta.Header] and [ResponseMeta.RawJSON] return independent copies.
// [ResponseMeta.RawHTTPResponse] returns a diagnostic copy with shared read-only
// TLS state and request headers containing credentials. Grouped answer maps
// share nested values with Answers; treat those values as read-only.
package typesafe
