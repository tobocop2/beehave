package proc

import "strings"

// tailWriter keeps the last limit bytes written to it. It holds at most twice the limit, so an endless output stays bounded.
type tailWriter struct {
	data  []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)

	if len(w.data) > 2*w.limit {
		w.data = append(w.data[:0], w.data[len(w.data)-w.limit:]...)
	}

	return len(p), nil
}

// text returns the last limit bytes, without a cut character at the start.
func (w *tailWriter) text() string {
	data := w.data[max(len(w.data)-w.limit, 0):]

	return strings.ToValidUTF8(string(data), "")
}
