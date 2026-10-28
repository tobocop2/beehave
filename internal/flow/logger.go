package flow

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/tobocop2/obee/internal/agent"
)

// redactingHandler removes secrets from the message and the attributes of every record before the inner handler writes it.
type redactingHandler struct {
	inner  slog.Handler
	redact func(string) string
}

// newLogger returns a logger that writes text records at INFO or above to out, with secrets removed. One redactor serves every record.
func newLogger(out io.Writer) *slog.Logger {
	return slog.New(redactingHandler{inner: slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}), redact: agent.Redactor()})
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(redactAttr(h.redact, attr))

		return true
	})

	return h.inner.Handle(ctx, clean)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for index, attr := range attrs {
		clean[index] = redactAttr(h.redact, attr)
	}

	return redactingHandler{inner: h.inner.WithAttrs(clean), redact: h.redact}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{inner: h.inner.WithGroup(name), redact: h.redact}
}

// redactAttr redacts a text value, an error or any other value, and each member of a group.
func redactAttr(redact func(string) string, attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()

	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, redact(value.String()))
	case slog.KindGroup:
		members := value.Group()
		clean := make([]any, len(members))

		for index, member := range members {
			clean[index] = redactAttr(redact, member)
		}

		return slog.Group(attr.Key, clean...)
	case slog.KindAny:
		return slog.String(attr.Key, redact(fmt.Sprint(value.Any())))
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64, slog.KindTime, slog.KindUint64, slog.KindLogValuer:
		// Numbers, times and booleans carry no secret.
	}

	return attr
}
