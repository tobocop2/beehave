package review

import (
	"fmt"
	"strconv"

	"github.com/tobocop2/obee/internal/model"
)

// Kind says what a row of the poll asks for.
type Kind string

// The kinds of a row. The first six are for a pull request we may review.
const (
	KindNew      Kind = "NEW"
	KindCurrent  Kind = "CURRENT"
	KindWaiting  Kind = "WAITING"
	KindFollowUp Kind = "FOLLOW-UP"
	KindReReview Kind = "RE-REVIEW"
	KindReplied  Kind = "REPLIED"
	KindOwn      Kind = "OWN"
	KindReply    Kind = "REPLY"
	KindCIRed    Kind = "CI-RED"
	KindCINotRun Kind = "CI-NOT-RUN"
)

const ownLineFormat = "%-10s #%d fresh_reviews=%d unresolved=%s changes_requested=%s draft=%s"

// Row is one line of the poll. String prints the line, and the JSON of the row holds the same facts.
type Row struct {
	Kind   Kind   `json:"kind"`
	Number int    `json:"number"`
	Author string `json:"author,omitempty"`
	Head   string `json:"head,omitempty"`
	Ours   string `json:"ours,omitempty"`
	OursAt string `json:"ours_at,omitempty"`
	// OursWhen and StillHead complete the verdict ledger of the row: when our last review was submitted, and whether the commit it read (OursAt) is the head.
	// A row with no review of ours has neither.
	OursWhen         string `json:"ours_when,omitempty"`
	StillHead        *bool  `json:"still_head,omitempty"`
	ReplyAt          string `json:"reply_at,omitempty"`
	IsDraft          *bool  `json:"isDraft,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
	Title            string `json:"title,omitempty"`
	FreshReviews     int    `json:"fresh_reviews,omitempty"`
	Unresolved       string `json:"unresolved,omitempty"`
	ChangesRequested string `json:"changes_requested,omitempty"`
	Failed           int    `json:"failed,omitempty"`
	NotStarted       int    `json:"not_started,omitempty"`
}

func (r Row) draft() string {
	if r.IsDraft == nil {
		return "null"
	}

	return strconv.FormatBool(*r.IsDraft)
}

// String prints the line of the row.
func (r Row) String() string {
	switch r.Kind {
	case KindOwn:
		return fmt.Sprintf(ownLineFormat, r.Kind, r.Number, r.FreshReviews, r.Unresolved, r.ChangesRequested, r.draft())
	case KindReply:
		return fmt.Sprintf("%-10s #%d at=%s %s", r.Kind, r.Number, r.ReplyAt, model.FirstRunes(r.Title, issueTitleWidth))
	case KindCIRed:
		return fmt.Sprintf("%-10s #%d failed=%d not_started=%d", r.Kind, r.Number, r.Failed, r.NotStarted)
	case KindCINotRun:
		return fmt.Sprintf("%-10s #%d not_started=%d", r.Kind, r.Number, r.NotStarted)
	case KindNew, KindCurrent, KindWaiting, KindFollowUp, KindReReview, KindReplied:
	}

	return r.reviewLine()
}

func (r Row) reviewLine() string {
	suffix := ""
	if r.ReplyAt != "" {
		suffix = " reply=" + r.ReplyAt
	}

	return fmt.Sprintf("%-10s #%d %s head=%s ours=%s@%s draft=%s upd=%s %s%s", r.Kind, r.Number, r.Author, model.FirstRunes(r.Head, headWidth),
		r.Ours, model.FirstRunes(r.OursAt, headWidth), r.draft(), model.FirstRunes(r.UpdatedAt, updatedWidth), model.FirstRunes(r.Title, titleWidth), suffix)
}
