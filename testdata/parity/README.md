# Python parity fixtures

`python-0.7.2.json` records how the official Python SDK, version 0.7.2 at
commit `f078f1e208a0d885154dc758344ae4fce77ac168`, handles a fixed set of
inputs:

- response bodies: successes, missing and null fields, unknown answer types,
  validation paths, and API error messages
- request encoding: state, questions, models, extra body fields, and headers
- retries: attempt counts and `X-TypeSafe-Retry-Count` headers
- `Retry-After` and `retry-after-ms` parsing
- API key validation

`parity_test.go` replays the same inputs through the Go SDK. It needs neither
Python nor network access.

## Differences

`differences.json` lists every intentional difference, each with a reason:

- `normalize` entries map Python's representation to Go's for every case. The
  matching function lives in `parityNormalizers` in `parity_test.go`.
- `override` entries merge-patch (RFC 7386) the expected result of specific
  cases.
- `note` entries document differences that the fixture does not exercise.

The test fails when a normalizer or an override no longer changes anything, so
the list cannot go stale. Any other mismatch is a bug to investigate, not a new
entry to add.

## Regenerating

Cases are defined in `generate.py`. After changing them, regenerate the fixture
from a checkout of the Python SDK at the pinned commit (requires `uv`):

```sh
git clone https://github.com/typesafe-ai/typesafe-sdk-python ../typesafe-sdk-python
git -C ../typesafe-sdk-python checkout f078f1e208a0d885154dc758344ae4fce77ac168
testdata/parity/regenerate.sh ../typesafe-sdk-python
```

CI regenerates the fixture the same way and fails if it differs from the
committed file.
