package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// DroppedLine is the text that replaces an output line which is longer than the limit.
const DroppedLine = "[line dropped: longer than the limit]"

const (
	redacted     = "[redacted]"
	droppedLine  = DroppedLine + "\n"
	maxLineBytes = 16 << 20
	tailBytes    = 4 << 10
	// MinSecretLen is the shortest secret value that a redactor removes. A shorter value would mask common words.
	MinSecretLen = 8
)

// tokenShape matches an Anthropic key and the forge token prefixes ghp_, gho_, ghs_, github_pat_ and glpat-.
var tokenShape = regexp.MustCompile(`(?:sk-ant-|ghp_|gho_|ghs_|github_pat_|glpat-)[A-Za-z0-9_-]{8,}`)

// redactor removes secrets from text: the exact values it holds, any token shaped like an Anthropic key or a forge token, and then whatever next removes.
type redactor struct {
	replacer *strings.Replacer
	next     func(string) string
}

// newRedactor holds each secret of MinSecretLen bytes or more in four forms: as it is, JSON-escaped (with and without the escape of <, > and &), and URL-escaped.
// A secret of several lines is also held one line at a time. The replacer tries the longest form first, in a fixed order, so a secret that starts another one never leaves a suffix.
func newRedactor(secrets ...string) redactor {
	forms := map[string]bool{}

	for _, secret := range secrets {
		for _, part := range parts(secret) {
			for _, form := range []string{part, jsonText(part, true), jsonText(part, false), url.QueryEscape(part), url.PathEscape(part)} {
				forms[form] = true
			}
		}
	}

	ordered := slices.Sorted(maps.Keys(forms))
	slices.SortStableFunc(ordered, func(a, b string) int { return len(b) - len(a) })

	var pairs []string //nolint:prealloc // the list is built once at start

	for _, form := range ordered {
		pairs = append(pairs, form, redacted)
	}

	return redactor{replacer: strings.NewReplacer(pairs...)}
}

// parts returns the text of a secret that a redactor must find: the whole secret, and each line of a secret of several lines. A part shorter than MinSecretLen is left out.
func parts(secret string) []string {
	var found []string

	if len(secret) >= MinSecretLen {
		found = append(found, secret)
	}

	if !strings.Contains(secret, "\n") {
		return found
	}

	for line := range strings.Lines(secret) {
		if line = strings.TrimSpace(line); len(line) >= MinSecretLen {
			found = append(found, line)
		}
	}

	return found
}

// jsonText returns the text as it stands inside a JSON string. escapeHTML says whether <, > and & become \u escapes.
func jsonText(text string, escapeHTML bool) string {
	var out bytes.Buffer

	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(escapeHTML)

	_ = encoder.Encode(text) //nolint:errcheck,errchkjson // a string always encodes

	return strings.TrimSuffix(strings.TrimSuffix(out.String(), "\n"), `"`)[1:]
}

func (r redactor) redact(text string) string {
	text = tokenShape.ReplaceAllString(r.replacer.Replace(text), redacted)
	if r.next != nil {
		return r.next(text)
	}

	return text
}

// Redactor returns a function that removes the given secrets of MinSecretLen bytes or more and any token shaped like an Anthropic key from text. It builds the replacer once.
func Redactor(secrets ...string) func(string) string {
	return newRedactor(secrets...).redact
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
