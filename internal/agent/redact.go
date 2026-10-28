package agent

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	redacted     = "[redacted]"
	droppedLine  = "[line dropped: longer than the limit]\n"
	maxLineBytes = 16 << 20
	tailBytes    = 4 << 10
)

var tokenShape = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)

// redactor removes secrets from text: the exact values it holds and any token shaped like an Anthropic key.
type redactor struct {
	replacer *strings.Replacer
}

func newRedactor(secrets ...string) redactor {
	var pairs []string

	for _, secret := range secrets {
		if secret != "" {
			pairs = append(pairs, secret, redacted)
		}
	}

	return redactor{replacer: strings.NewReplacer(pairs...)}
}

func (r redactor) redact(text string) string {
	return tokenShape.ReplaceAllString(r.replacer.Replace(text), redacted)
}

// redactWriter passes whole lines through a redactor. A line past the limit is dropped, so a secret never splits across two writes.
type redactWriter struct {
	out      io.Writer
	redactor redactor
	pending  []byte
	dropping bool
	limit    int
}

func (w *redactWriter) Write(data []byte) (int, error) {
	w.pending = append(w.pending, data...)

	if bytes.IndexByte(data, '\n') >= 0 {
		if err := w.drain(); err != nil {
			return 0, err
		}
	}

	if len(w.pending) > w.limit {
		w.pending, w.dropping = nil, true
	}

	return len(data), nil
}

// drain writes each complete line of the pending bytes.
func (w *redactWriter) drain() error {
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return nil
		}

		line := w.pending[:end+1]
		w.pending = w.pending[end+1:]

		if err := w.emit(string(line)); err != nil {
			return err
		}
	}
}

func (w *redactWriter) emit(line string) error {
	if w.dropping {
		w.dropping = false
		line = droppedLine
	}

	_, err := io.WriteString(w.out, w.redactor.redact(line))

	return err
}

// flush writes the last line when the stream ended without a newline.
func (w *redactWriter) flush() error {
	rest := string(w.pending)
	w.pending = nil

	if w.dropping {
		rest = droppedLine
	}

	if rest == "" {
		return nil
	}

	return w.emit(rest)
}

// tailBuffer keeps the last tailBytes bytes written to it, cut on a rune boundary.
type tailBuffer struct {
	data []byte
}

func (t *tailBuffer) Write(data []byte) (int, error) {
	t.data = append(t.data, data...)
	if over := len(t.data) - tailBytes; over > 0 {
		t.data = t.data[over:]
	}

	for len(t.data) > 0 && !utf8.RuneStart(t.data[0]) {
		t.data = t.data[1:]
	}

	return len(data), nil
}
