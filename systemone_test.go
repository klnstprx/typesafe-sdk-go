package typesafe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type spamResponse struct {
	ResponseMeta
	Model   string `json:"model"`
	Answers struct {
		Spam struct {
			Noul float64 `json:"noul"`
		} `json:"spam"`
	} `json:"answers"`
}

type modelsShape struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

type validated struct {
	Model *string `json:"model"`
}

func (v *validated) Validate() error {
	if v.Model == nil {
		return errors.New("model is required")
	}
	return nil
}

func TestSystemOneAsDecodes(t *testing.T) {
	c := fakeClient(t, newFake(reply{status: 200, body: okBody, header: hdr("X-Typesafe-Request-Id", "req-t", "X-Other", "o")}))
	resp, err := SystemOneAs[spamResponse](bg, c, "x", spamQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "jev-1" || resp.Answers.Spam.Noul != 0.9 {
		t.Errorf("resp = %+v", resp)
	}
	if resp.RequestID() != "req-t" || resp.Header().Get("X-Other") != "o" || string(resp.RawJSON()) != okBody || resp.RawHTTPResponse() == nil {
		t.Error("ResponseMeta not attached")
	}
}

func TestSystemOneAsSDKType(t *testing.T) {
	c := fakeClient(t, newFake(ok(fullBody)))
	resp, err := SystemOneAs[SystemOneResponse](bg, c, "x", spamQuestion)
	if err != nil || len(resp.Answers) != 3 || resp.RawHTTPResponse() == nil {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	c = fakeClient(t, newFake(ok(`{"model":"m"}`)), noRetry())
	_, err = SystemOneAs[SystemOneResponse](bg, c, "x", spamQuestion)
	var ve *ResponseValidationError
	if !errors.As(err, &ve) || ve.FieldPath != "usage" || ve.StatusCode != 200 {
		t.Errorf("err = %v", err)
	}
}

func TestSystemOneAsValidationPaths(t *testing.T) {
	t.Run("nested struct", func(t *testing.T) {
		c := fakeClient(t, newFake(reply{status: 200, body: `{"model":"m","answers":{"spam":{"noul":"high"}}}`, header: hdr("X-Typesafe-Request-Id", "r")}), noRetry())
		_, err := SystemOneAs[spamResponse](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "answers.spam.noul" {
			t.Fatalf("err = %v", err)
		}
		if want := "POST https://api.test/v1/systemone: 200 Invalid response data at 'answers.spam.noul'. (request_id=r)"; err.Error() != want {
			t.Errorf("Error() = %q", err.Error())
		}
	})
	t.Run("composite mismatch", func(t *testing.T) {
		c := fakeClient(t, newFake(ok(`{"answers":{"spam":[1]}}`)), noRetry())
		_, err := SystemOneAs[spamResponse](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "answers.spam" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("slice index", func(t *testing.T) {
		c := fakeClient(t, newFake(ok(`{"models":[{"name":"a"},{"name":5}]}`)), noRetry())
		_, err := SystemOneAs[modelsShape](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "models[1].name" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("map key", func(t *testing.T) {
		type legendShape struct {
			Models []struct {
				Legend map[int]string `json:"legend"`
			} `json:"models"`
		}
		c := fakeClient(t, newFake(ok(`{"models":[{},{"legend":{"0":"a","x":"b"}}]}`)), noRetry())
		_, err := SystemOneAs[legendShape](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "models[1].legend.x" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("syntax error", func(t *testing.T) {
		c := fakeClient(t, newFake(ok(`{"model":`)), noRetry())
		_, err := SystemOneAs[spamResponse](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.FieldPath != "" || ve.Err == nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("Validate hook", func(t *testing.T) {
		c := fakeClient(t, newFake(ok(`{}`)), noRetry())
		_, err := SystemOneAs[validated](bg, c, "x", spamQuestion)
		var ve *ResponseValidationError
		if !errors.As(err, &ve) || ve.Err == nil || !strings.Contains(err.Error(), "model is required") {
			t.Fatalf("err = %v", err)
		}
		c = fakeClient(t, newFake(ok(`{"model":"m"}`)))
		if v, err := SystemOneAs[validated](bg, c, "x", spamQuestion); err != nil || *v.Model != "m" {
			t.Errorf("valid body: %v", err)
		}
	})
	t.Run("API errors unchanged", func(t *testing.T) {
		c := fakeClient(t, newFake(reply{status: 400, body: `{"error":"bad question"}`}))
		_, err := SystemOneAs[spamResponse](bg, c, "x", spamQuestion)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != "bad question" {
			t.Errorf("err = %v", err)
		}
	})
}

type slowValidated struct {
	Model string `json:"model"`
}

func (*slowValidated) Validate() error {
	time.Sleep(2 * time.Second)
	return nil
}

func TestSystemOneAsDeadlineDuringValidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := fakeClient(t, newFake(ok(okBody)))
		ctx, cancel := context.WithTimeout(bg, time.Second)
		defer cancel()
		resp, err := SystemOneAs[slowValidated](ctx, c, "x", spamQuestion)
		if resp != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("resp = %v, err = %v; want no result and a deadline error", resp, err)
		}
	})
}

type slowInvalid struct{}

func (*slowInvalid) Validate() error {
	time.Sleep(2 * time.Second)
	return errors.New("invalid")
}

func TestDeadlineTakesPrecedenceOverValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake(ok(okBody))
		c := fakeClient(t, f)
		ctx, cancel := context.WithTimeout(bg, time.Second)
		defer cancel()
		_, err := SystemOneAs[slowInvalid](ctx, c, "x", spamQuestion)
		var ce *ConnectionError
		var ve *ResponseValidationError
		if !errors.As(err, &ce) || errors.As(err, &ve) || !errors.Is(err, context.DeadlineExceeded) || f.count() != 1 {
			t.Errorf("err = %T %v after %d attempts", err, err, f.count())
		}
	})
}

type mapResponse struct {
	Answers map[string]map[string]any `json:"answers"`
	Model   string                    `json:"model"`
}

func (m *mapResponse) Validate() error {
	if m.Model == "" {
		return errors.New("model missing")
	}
	return nil
}

func TestSystemOneAsFreshDecodePerAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.Predicate = func(err error) bool {
			var ve *ResponseValidationError
			return errors.As(err, &ve)
		}
		f := newFake(
			ok(`{"answers":{"leak":{"a":1}},"model":5}`), // decodes answers, then fails on model
			ok(`{"answers":{"leak2":{"b":2}}}`),          // decodes, then fails Validate
			ok(`{"answers":{"real":{"c":3}},"model":"m"}`),
		)
		c := fakeClient(t, f, WithRetryPolicy(p))
		resp, err := SystemOneAs[mapResponse](bg, c, "x", spamQuestion)
		if err != nil {
			t.Fatal(err)
		}
		if f.count() != 3 || len(resp.Answers) != 1 || resp.Answers["real"] == nil {
			t.Errorf("answers = %v after %d attempts; failed attempts leaked", resp.Answers, f.count())
		}
	})
}

func TestSystemOneFreshDecodePerAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := DefaultRetryPolicy()
		p.HTTPStatuses = []int{200}
		f := newFake(
			ok(`{"model":"m","usage":{},"answers":{"leak":{"type":"noul","noul":1},"bad":{"type":"noul"}}}`),
			ok(okBody),
		)
		c := fakeClient(t, f, WithRetryPolicy(p))
		resp, err := c.SystemOne(bg, "x", spamQuestion)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := resp.Answers["leak"]; leaked || len(resp.Answers) != 1 {
			t.Errorf("answers = %v", resp.Answers)
		}
	})
}
