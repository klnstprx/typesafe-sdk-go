# Changelog

This project follows [Semantic Versioning](https://semver.org). From v1.0.0,
the exported API of `github.com/klnstprx/typesafe-sdk-go` is stable: breaking
changes require a new major version.

## v1.1.0 — 2026-10-29

### API

- `Box` questions locate what their instructions describe in the request's
  image. They are answered with `BoxAnswer` (edges in `BoxCoordinates` and a
  `Confidence` that is nil when the model gives none) and selected with
  `SystemOneResponse.Boxes()`. `Questions.UnmarshalJSON` decodes `"box"` into
  `Box`, and box answers are no longer skipped as an unknown type.
- Image assets: `Image`, created with `NewImage` or `ImageFromBase64`, is sent
  with the request wherever it is placed in state, instructions, criteria, or
  `WithExtraBody` values, as `multipart/form-data`. `WithImages` makes images
  available to text that refers to them. In `SystemOne` and `SystemOneAs`, a
  reference the call has no image for fails before any network I/O; Python
  sends it as plain text, the JS SDK rejects it as Go does. `CreateSystemOne`
  sends it as plain text, so proxies forward decoded requests unchanged.
- `SystemOneRequest.Assets` and the `Asset` type carry the wire `"assets"`
  field, so proxies no longer drop it when decoding and forwarding requests.
  `Validate` rejects an empty map and blank placeholders.

### Behavior

- A `Noul` needs `Instructions` or at least one non-nil outcome in `Criteria`,
  and a raw `"noul"` needs `"instructions"` or a non-null outcome; its
  `"criteria"` must be an object or null. Such questions previously went out as
  `{"type":"noul"}`. This matches Python SDK 0.7.3.
- `Choice` and raw `"choice"` questions with empty criteria fail before any
  network I/O, as in Python SDK 0.7.3.
- The SDK-owned transport keeps up to 100 idle connections per host for 30s
  (previously net/http's 2 per host for 90s), so bursts of concurrent calls no
  longer reopen connections. Clients passed to `WithHTTPClient` are unchanged.
- Parity fixtures now come from Python SDK 0.7.5 (commit `f36e6854`). Two new
  documented differences: Go still rejects NaN and infinities in request
  content (Python 0.7.4 sends them as strings or null), and Go does not
  zstd-compress request bodies (Python 0.7.5 does by default).

## v1.0.2 — 2026-10-29

### Fixed

- `Models.List` accepts a model entry whose `release_date` is absent or null
  and reports `ReleaseDate` as `""`. llama-server SystemOne servers omit
  `release_date`, so the SDK rejected their whole model list. A present
  non-string `release_date` is still invalid. The OpenAPI spec and Python SDK
  0.7.2 still require the field; this is a documented deviation.

## v1.0.1 — 2026-10-15

### API

- `SystemOneRequest` exposes the SystemOne wire request (`State`, `Model`,
  `Questions`) so proxies and compatible servers can decode it with
  `encoding/json`. `Validate` applies the same state and question rules as
  `SystemOne` without a client call.
- `Client.CreateSystemOne` sends a decoded or constructed `SystemOneRequest`.
  A non-empty request `Model` overrides the client default; `WithModel`
  overrides both, even when explicitly empty. `SystemOne` is unchanged.
- `Questions` implements `json.Marshaler` and `json.Unmarshaler`. Decoding
  selects `Noul`, `Choice`, or `Score` by type and falls back to `RawQuestion`
  for unknown types; structured content decodes with `json.Number` so large
  integers survive a round trip. Unknown fields on known types are ignored.

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
