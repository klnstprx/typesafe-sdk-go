package typesafe

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"
)

// RetryPolicy controls how the SDK retries a failed call. The zero value makes
// one attempt and never waits; [DefaultRetryPolicy] returns the SDK defaults,
// which match the Python SDK.
//
// [WithRetryPolicy] replaces the whole policy, so start from
// [DefaultRetryPolicy] to keep the other defaults. An application that already
// retries should pass RetryPolicy{MaxRetries: 0} to avoid multiplying attempts.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the first attempt; 0 disables
	// retries.
	MaxRetries int
	// BackoffInitial is the first backoff delay, doubled for each retry up to
	// BackoffMax. Zero disables backoff.
	BackoffInitial time.Duration
	// BackoffMax caps the backoff delay. Zero disables backoff.
	BackoffMax time.Duration
	// BackoffJitter is the fraction of each backoff delay randomly subtracted,
	// between 0 and 1.
	BackoffJitter float64
	// HTTPStatuses lists the retried HTTP status codes. Nil means the default
	// set {408, 429, 500–599}; an empty, non-nil slice retries no status.
	HTTPStatuses []int
	// DisableRetryAfter ignores the retry-after-ms and Retry-After response
	// headers, which are otherwise honored exactly, without cap or jitter.
	DisableRetryAfter bool
	// DisableConnectionErrorRetry stops retrying *ConnectionError.
	DisableConnectionErrorRetry bool
	// DisableTimeoutRetry stops retrying *TimeoutError.
	DisableTimeoutRetry bool
	// Predicate adds retries for errors not selected by the rules above; true
	// allows a retry and false cannot veto a retry selected by those rules.
	// It is not called for errors already selected, an ended caller context,
	// or after MaxRetries is reached. It runs synchronously and must be fast
	// and safe for concurrent calls. MaxRetries and Timeout still apply.
	Predicate func(error) bool
	// Timeout is the retry-scheduling budget for one call. After a failed
	// attempt the SDK stops, returning that attempt's error, when the budget is
	// spent or the next delay would reach the remaining budget. It does not
	// interrupt an attempt in flight, so a call can outlast it; use a context
	// deadline for a hard bound. Zero means unlimited.
	Timeout time.Duration
}

// DefaultRetryPolicy returns the SDK default: two retries, backoff from 500ms
// up to 5s with 25% jitter, the default status set, Retry-After honored,
// connection and timeout errors retried, and a 30s scheduling budget.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     2,
		BackoffInitial: 500 * time.Millisecond,
		BackoffMax:     5 * time.Second,
		BackoffJitter:  0.25,
		Timeout:        30 * time.Second,
	}
}

func (p RetryPolicy) validate() error {
	switch {
	case p.MaxRetries < 0:
		return errors.New("typesafe: retry policy: MaxRetries must not be negative")
	case p.BackoffInitial < 0:
		return errors.New("typesafe: retry policy: BackoffInitial must not be negative")
	case p.BackoffMax < 0:
		return errors.New("typesafe: retry policy: BackoffMax must not be negative")
	case math.IsNaN(p.BackoffJitter) || p.BackoffJitter < 0 || p.BackoffJitter > 1:
		return errors.New("typesafe: retry policy: BackoffJitter must be between 0 and 1")
	case p.Timeout < 0:
		return errors.New("typesafe: retry policy: Timeout must not be negative")
	}
	return nil
}

func (p RetryPolicy) clone() RetryPolicy {
	p.HTTPStatuses = slices.Clone(p.HTTPStatuses)
	return p
}

func (p RetryPolicy) retriesStatus(status int) bool {
	if p.HTTPStatuses == nil {
		return status == 408 || status == 429 || (status >= 500 && status <= 599)
	}
	return slices.Contains(p.HTTPStatuses, status)
}

func (p RetryPolicy) retryable(err error) bool {
	var (
		timeoutErr    *TimeoutError
		connErr       *ConnectionError
		apiErr        *APIError
		validationErr *ResponseValidationError
		builtin       bool
	)
	switch {
	case errors.As(err, &timeoutErr):
		builtin = !p.DisableTimeoutRetry
	case errors.As(err, &connErr):
		builtin = !p.DisableConnectionErrorRetry
	case errors.As(err, &apiErr):
		builtin = p.retriesStatus(apiErr.StatusCode)
	case errors.As(err, &validationErr):
		builtin = p.retriesStatus(validationErr.StatusCode)
	}
	return builtin || (p.Predicate != nil && p.Predicate(err))
}

// wait returns the delay before retry number n (1-based) after err.
func (p RetryPolicy) wait(n int, err error) time.Duration {
	if !p.DisableRetryAfter {
		var apiErr *APIError
		var validationErr *ResponseValidationError
		switch {
		case errors.As(err, &apiErr):
			if d, ok := parseRetryAfter(apiErr.Header); ok {
				return d
			}
		case errors.As(err, &validationErr):
			if d, ok := parseRetryAfter(validationErr.Header); ok {
				return d
			}
		}
	}
	return p.backoff(n, rand.Float64())
}

// backoff returns the exponential delay for retry n (1-based), capped at
// BackoffMax, with the jitter fraction r·BackoffJitter subtracted and the result
// rounded to the millisecond. It never overflows.
func (p RetryPolicy) backoff(n int, r float64) time.Duration {
	if p.BackoffInitial == 0 || p.BackoffMax == 0 {
		return 0
	}
	exp := p.BackoffMax
	if f := float64(p.BackoffInitial) * math.Ldexp(1, n-1); f < float64(p.BackoffMax) {
		// f < float64(BackoffMax) <= 2^63, so the conversion stays in range.
		exp = time.Duration(f)
	}
	d := float64(exp) * (1 - r*p.BackoffJitter)
	d = math.Round(d/float64(time.Millisecond)) * float64(time.Millisecond)
	if d >= float64(exp) {
		return exp
	}
	jittered, _ := durationFromFloat(d)
	return jittered
}

func retryReason(err error) string {
	var (
		timeoutErr    *TimeoutError
		apiErr        *APIError
		validationErr *ResponseValidationError
	)
	switch {
	case errors.As(err, &timeoutErr):
		return "timeout"
	case errors.As(err, &apiErr):
		return "status=" + strconv.Itoa(apiErr.StatusCode)
	case errors.As(err, &validationErr):
		return "invalid_response"
	default:
		return "connection"
	}
}

// sleep waits for d or until ctx ends, returning ctx's error in that case.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
