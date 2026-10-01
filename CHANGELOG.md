# Changelog

This project follows [Semantic Versioning](https://semver.org). From v1.0.0,
the exported API of `github.com/klnstprx/typesafe-sdk-go` is stable: breaking
changes require a new major version.

## v1.0.0 — 2026-10-01

Initial release. Matches the observable behavior of the Python SDK
`typesafe-sdk` 0.7.2 (commit `f078f1e2`), apart from the documented
differences in `testdata/parity/differences.json`.

### API

- `Client.SystemOne`, generic `SystemOneAs[T]` with an optional
  `Validate() error` hook, and `Models.List`.
- Typed questions (`Noul`, `Choice`, `Score`) and wire-form `RawQuestion`,
  validated before any network I/O; `json.Marshal` rejects the same values.
- Strict response validation with field paths (`answers.tone.confidence`,
  `models[1].name`), forward-compatible skipping of unknown answer types, and
  copy-on-access `ResponseMeta` (`RequestID`, `Header`, `RawJSON`,
  `RawHTTPResponse`).
- Typed errors: `APIError`, `ResponseValidationError`, `ConnectionError`, and
  `TimeoutError`, with `HTTPStatusCode()` on the HTTP errors.
- Python-compatible retries: default statuses, exponential backoff with
  jitter, `Retry-After`/`retry-after-ms`, a retry-scheduling budget, and a
  `Predicate` hook. `RetryPolicy{MaxRetries: 0}` hands retries to the
  application.

### Behavior

- The caller's context bounds the whole call; `WithTimeout` bounds each
  attempt, including its body read; an expired context never reports success.
- Redirects are never followed, including 3xx responses with malformed
  `Location` headers, which surface as `*APIError`.
- Credentials (API key, secret headers, cookie-jar cookies, URL userinfo) are
  redacted from transport errors and never appear in formatted clients.
- Logging is silent unless `WithLogger` is given; records carry the call's
  context.
- A supplied `http.Client` is copied, never mutated, and its transport is never
  closed.
- No third-party dependencies.

### Verification

- Python parity fixtures in `testdata/parity`, regenerated and checked for
  drift in CI.
- Fuzz targets for response decoding, validation paths, credential redaction,
  and retry delays, with seeds from previously found failures.
- An external consumer module in `testdata/consumer`.
- CI on Linux, macOS, and Windows with Go 1.25 and the latest stable Go.
