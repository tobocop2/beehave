package cli

import (
	"encoding/json"
	"io"
	"time"

	"github.com/tobocop2/obee/internal/flow"
)

// runView is one run of the JSON list: the fields of the status line, and the seconds left to the deadline of an open wait.
// TimeLeft is nil for a run with no deadline. It is negative for a deadline that passed.
type runView struct {
	flow.RunInfo
	TimeLeft *float64 `json:"time_left_seconds,omitempty"`
}

// printRunsJSON writes the runs as one JSON array that a script reads. Every figure comes from the recorded state that the text list reads.
func printRunsJSON(stdout io.Writer, infos []flow.RunInfo, now time.Time) error {
	views := make([]runView, 0, len(infos))

	for _, info := range infos {
		view := runView{RunInfo: info}

		if !info.Deadline.IsZero() {
			left := info.Deadline.Sub(now).Seconds()
			view.TimeLeft = &left
		}

		views = append(views, view)
	}

	return json.NewEncoder(stdout).Encode(views)
}
