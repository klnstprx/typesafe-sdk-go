package typesafe

import (
	"context"
	"log/slog"
	"time"
)

// The SDK logs only through the logger given to WithLogger, always with the
// call's context so handlers can attach correlation values. It never logs
// bodies, headers, credentials, or the errors it returns.

func (c *Client) logAttempt(ctx context.Context, req *request, attempt, status int, errKind string, d time.Duration, requestID string) {
	if !c.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("method", req.method),
		slog.String("url", req.logURL),
		slog.Int("attempt", attempt),
	}
	if errKind != "" {
		attrs = append(attrs, slog.String("error_kind", errKind))
	} else {
		attrs = append(attrs, slog.Int("status", status))
	}
	attrs = append(attrs,
		slog.Int64("duration_ms", d.Milliseconds()),
		slog.String("request_id", requestID),
	)
	c.logger.LogAttrs(ctx, slog.LevelDebug, "typesafe.attempt", attrs...)
}

func (c *Client) logRetry(ctx context.Context, nextAttempt int, delay time.Duration, reason string) {
	c.logger.LogAttrs(ctx, slog.LevelDebug, "typesafe.retry",
		slog.Int("attempt", nextAttempt),
		slog.Int64("delay_ms", delay.Milliseconds()),
		slog.String("reason", reason),
	)
}

func (c *Client) logUnknownAnswer(ctx context.Context, question, typ string) {
	c.logger.LogAttrs(ctx, slog.LevelWarn, "typesafe.unknown_answer_type",
		slog.String("question", question),
		slog.String("type", typ),
	)
}
