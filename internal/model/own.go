package model

import "errors"

// ErrMorePages marks a list that holds more rows than one page, so the rest are unknown. A refusal wraps it.
var ErrMorePages = errors.New("more rows than one page")

// ReviewStamp is who reviewed a pull request and when. SubmittedAt is empty for a review that is not sent.
type ReviewStamp struct {
	Login       string `json:"login"`
	SubmittedAt string `json:"submitted_at"`
}

// OwnPull is the review state of one open pull request of ours: the time of its last push, its reviews,
// its standing change requests and its open threads. A false Known flag means the list was more than one page.
type OwnPull struct {
	Number         int             `json:"number"`
	Draft          bool            `json:"draft"`
	Base           string          `json:"base"`
	Title          string          `json:"title"`
	Push           string          `json:"push"`
	Reviews        []ReviewStamp   `json:"reviews"`
	ChangeRequests []ChangeRequest `json:"change_requests"`
	ChangesKnown   bool            `json:"changes_known"`
	OpenThreads    int             `json:"open_threads"`
	ThreadsKnown   bool            `json:"threads_known"`
}
