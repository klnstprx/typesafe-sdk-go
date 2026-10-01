# typesafe-sdk-go

A Go client for the [TypeSafe AI API](https://typesafe.ai), with typed questions
and answers, custom response types, automatic retries, and optional logging.

Requires Go 1.25 or later. No third-party dependencies. The public API is stable
from v1.0.0 and follows semantic versioning.

[API reference](https://pkg.go.dev/github.com/klnstprx/typesafe-sdk-go) ·
[Examples](examples) · [Changelog](CHANGELOG.md)

## Install

```sh
go get github.com/klnstprx/typesafe-sdk-go@v1.0.0
```

## Quickstart

Set `TYPESAFE_API_KEY` before running, or pass `typesafe.WithAPIKey(key)` to `New`.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/klnstprx/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.New() // reads TYPESAFE_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.SystemOne(ctx, "I was charged twice. Please help.", typesafe.Questions{
		"billing": typesafe.Noul{Instructions: "Is this about billing?"},
	})
	if err != nil {
		log.Fatal(err)
	}
	answer, ok := resp.Answers["billing"].(typesafe.NoulAnswer)
	if !ok {
		log.Fatal("billing answer is missing or has an unexpected type")
	}
	fmt.Println(answer.Noul) // probability that the answer is yes
}
```

Use `Noul` for yes/no questions, `Choice` for labels, and `Score` for ordered
rubrics. State and question descriptions accept JSON strings, objects, or
arrays, including Go structs and maps. See [systemone](examples/systemone) for
all three question types and [structuredstate](examples/structuredstate) for
structured input.

## Configuration

| Setting | Option | Environment | Default |
| --- | --- | --- | --- |
| API key (required) | `WithAPIKey` | `TYPESAFE_API_KEY` | — |
| Base URL | `WithBaseURL` | `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` |
| Model | `WithModel` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest` |
| Per-attempt timeout | `WithTimeout` | — | 10s |
| Retry policy | `WithRetryPolicy` | — | `DefaultRetryPolicy()` |
| Extra headers | `WithHeader`, `WithHeaders` | — | — |
| HTTP client | `WithHTTPClient` | — | private transport |
| Logger | `WithLogger` | — | discard |

Explicit options override environment values. Model, timeout, retry, and header
options also work per call. `WithExtraBody` adds or overrides top-level fields
in a `SystemOne` request.

Clients created with `New` are safe for concurrent calls. Keep referenced inputs
unchanged during calls. `WithHTTPClient` shares your client's transport and
cookie jar without mutating the client or closing its transport; its timeout
applies unless overridden by `WithTimeout`.

## Timeouts and retries

The caller's context bounds network I/O and retry waits across the operation.
`WithTimeout` sets a positive timeout per attempt, including body reads.
`RetryPolicy.Timeout` is a scheduling budget (default 30s), not a deadline for
an attempt already in flight. JSON processing and custom validation hooks run
synchronously and cannot be interrupted.

By default, the SDK retries 408, 429, 5xx, connection errors, and timeouts twice,
using exponential backoff with jitter and honoring `Retry-After` headers.
`WithRetryPolicy` replaces the whole policy; start from `DefaultRetryPolicy()`
to retain defaults. If your application already retries, disable SDK retries:

```go
typesafe.WithRetryPolicy(typesafe.RetryPolicy{MaxRetries: 0})
```

See [retrypolicy](examples/retrypolicy) for customization.

## Responses

`Answers` contains `NoulAnswer`, `ChoiceAnswer`, and `ScoreAnswer` values.
Always check that each answer you use exists and has the expected type: unknown
answer types are skipped. Validation checks required fields and types, not
probability ranges or sums.

Use `SystemOneAs[T]` for custom types, with pointer fields and a `Validate() error`
method to reject missing or null values while accepting valid zeros. Embed
`ResponseMeta` to receive metadata. See [typedresponse](examples/typedresponse).

Responses expose `RequestID()`, `Header()`, `RawJSON()`, and `RawHTTPResponse()`.
Headers and raw JSON are copies; diagnostic HTTP responses contain request
credentials and share read-only TLS state. Use `client.Models.List(ctx)` to
discover available models; see [listmodels](examples/listmodels).

## Errors

Use `errors.As` to inspect typed errors:

| Error | Meaning |
| --- | --- |
| `*APIError` | Non-2xx response, including redirects; includes status, message, headers, and body |
| `*ResponseValidationError` | Invalid response or custom validation failure; includes a field path when available |
| `*TimeoutError` | Per-attempt or transport timeout |
| `*ConnectionError` | Transport or body-read failure, or caller cancellation during an attempt |

Use `errors.Is` to check caller cancellation. Invalid inputs fail before
network I/O. Redirects are never followed. Transport errors redact request
credentials; server content and custom validation errors are preserved without
redaction.

## Logging

Silent by default, including when `WithLogger(nil)` is passed. Inject your own
`*slog.Logger` with `WithLogger`; its handler controls filtering and output.
The SDK never uses the global logger or writes directly to stdout or stderr.
Records carry the call's context: attempts and retries at DEBUG, skipped unknown
answer types at WARN. Bodies, headers, credentials, and returned errors are
never logged.

## Development

```sh
go vet ./...
go test -race ./...
(cd testdata/consumer && go test ./...)
```

Tests use checked-in Python SDK 0.7.2 parity fixtures, the vendored OpenAPI
schema, and fuzz regression seeds; no API key or external network is needed.
See [parity documentation](testdata/parity/README.md) for regeneration and
[CI](.github/workflows/ci.yml) for the platform matrix and fuzz runs.

## License

[MIT](LICENSE)
