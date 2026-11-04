package sandbox

import "io"

// OrDiscard returns w, or a writer that drops everything when w is nil.
func OrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return w
}
