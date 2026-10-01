package typesafe

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var secretHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"cookie":              true,
	"set-cookie":          true,
}

func isSecretHeader(name string) bool {
	lower := strings.ToLower(name)
	return secretHeaders[lower] || strings.Contains(lower, "token") || strings.Contains(lower, "secret")
}

// redactor masks credentials in error messages.
type redactor struct {
	replacer *strings.Replacer
	secrets  []string
}

// newRedactor collects the API key, every secret-named request header value,
// the credential following an Authorization scheme, each cookie value, and the
// userinfo of u (which may be nil), in raw, Go-quoted, and JSON-escaped forms.
func newRedactor(apiKey string, header http.Header, u *url.URL) *redactor {
	values := []string{apiKey}
	// Split cookies leniently: a caller-supplied Cookie header need not be
	// well formed, and every value in it is a credential.
	for _, line := range header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			_, value, _ := strings.Cut(pair, "=")
			values = append(values, pair, value, strings.Trim(value, `"`))
		}
	}
	if u != nil && u.User != nil {
		name := u.User.Username()
		if pw, ok := u.User.Password(); ok {
			escaped := strings.TrimPrefix(url.UserPassword("", pw).String(), ":")
			values = append(values, u.User.String(), name+":"+pw, pw, escaped)
		} else {
			// A username without a password is often a token.
			values = append(values, u.User.String(), name)
		}
	}
	for name, vs := range header {
		if !isSecretHeader(name) {
			continue
		}
		for _, v := range vs {
			values = append(values, v)
			lower := strings.ToLower(name)
			if lower == "authorization" || lower == "proxy-authorization" {
				if _, cred, ok := strings.Cut(strings.TrimSpace(v), " "); ok {
					values = append(values, strings.TrimSpace(cred))
				}
			}
		}
	}
	seen := map[string]bool{}
	var secrets []string
	for _, v := range values {
		for _, variant := range secretVariants(v) {
			if variant != "" && !seen[variant] {
				seen[variant] = true
				secrets = append(secrets, variant)
			}
		}
	}
	// Longest first, so a full "Bearer <key>" is masked as a whole.
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(secrets))
	for _, s := range secrets {
		pairs = append(pairs, s, "***")
	}
	return &redactor{replacer: strings.NewReplacer(pairs...), secrets: secrets}
}

func secretVariants(v string) []string {
	if v == "" {
		return nil
	}
	quoted := strconv.Quote(v)
	ascii := strconv.QuoteToASCII(v)
	variants := []string{v, quoted[1 : len(quoted)-1], ascii[1 : len(ascii)-1]}
	if b, err := json.Marshal(v); err == nil {
		variants = append(variants, string(b[1:len(b)-1]))
	}
	return variants
}

func (r *redactor) redact(s string) string {
	if len(r.secrets) == 0 {
		return s
	}
	return r.replacer.Replace(s)
}

func (r *redactor) leaks(s string) bool {
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// sanitize returns err unchanged when no message in its chain contains a
// secret. Otherwise it replaces the whole error with one that carries only the
// redacted message, keeps errors.Is and net.Error behavior, and exposes neither
// the original value nor its message.
func (r *redactor) sanitize(err error) error {
	if err == nil || !r.chainLeaks(err) {
		return err
	}
	return &redactedError{
		msg:  r.redact(err.Error()),
		orig: err,
		timeout: chainHas(err, func(e error) bool {
			t, ok := e.(interface{ Timeout() bool })
			return ok && t.Timeout()
		}),
		temporary: chainHas(err, func(e error) bool {
			t, ok := e.(interface{ Temporary() bool })
			return ok && t.Temporary()
		}),
	}
}

func (r *redactor) chainLeaks(err error) bool {
	return chainHas(err, func(e error) bool { return r.leaks(e.Error()) })
}

// redactedError stands in for an error whose chain contained a credential.
// It deliberately has no Unwrap or As method.
type redactedError struct {
	msg                string
	orig               error
	timeout, temporary bool
}

func (e *redactedError) Error() string        { return e.msg }
func (e *redactedError) Is(target error) bool { return errors.Is(e.orig, target) }
func (e *redactedError) Timeout() bool        { return e.timeout }
func (e *redactedError) Temporary() bool      { return e.temporary }
func (e *redactedError) GoString() string {
	return "&typesafe.redactedError{" + strconv.Quote(e.msg) + "}"
}
