package model

// Call is one tool call found in a transcript: the line it sits on and its text.
type Call struct {
	Line int
	Text string
}
