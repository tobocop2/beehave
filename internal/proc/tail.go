package proc

import "strings"

// Tail is a writer that keeps the last limit bytes written to it. It holds at most twice the limit, so an endless output stays bounded.
// Make one with NewTail; a Tail from the zero value keeps nothing.
type Tail struct {
	data  []byte
	limit int
}

// NewTail returns a Tail that keeps the last limit bytes.
func NewTail(limit int) *Tail {
	return &Tail{limit: limit}
}

// Write adds p to the tail and always accepts all of it.
func (t *Tail) Write(p []byte) (int, error) {
	t.data = append(t.data, p...)

	if len(t.data) > 2*t.limit {
		t.data = append(t.data[:0], t.data[len(t.data)-t.limit:]...)
	}

	return len(p), nil
}

// Text returns the last limit bytes, without a cut character at the start.
func (t *Tail) Text() string {
	data := t.data[max(len(t.data)-t.limit, 0):]

	return strings.ToValidUTF8(string(data), "")
}
