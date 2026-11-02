package review_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func TestWatch_ReturnsMergedWaitingOrSendBack(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		change func(*pulls)
		want   review.Outcome
	}{
		"merged":                                   {func(p *pulls) { p.view.State = str("MERGED") }, review.Outcome{Kind: review.Merged}},
		"merged_beats_a_conflict":                  {func(p *pulls) { p.view.State = str("MERGED"); p.view.Mergeable = str("CONFLICTING") }, review.Outcome{Kind: review.Merged}},
		"closed_ends_the_watch":                    {func(p *pulls) { p.view.State = str("CLOSED") }, review.Outcome{Kind: review.Closed, Reason: "it is closed"}},
		"clean_and_open":                           {func(*pulls) {}, review.Outcome{Kind: review.Waiting, Head: head}},
		"an_open_thread_waits":                     {func(p *pulls) { p.threads = make([]model.ReviewThread, 1) }, review.Outcome{Kind: review.Waiting, Head: head}},
		"a_moved_head_waits_and_names_the_head":    {func(p *pulls) { p.view.HeadRefOid = str(old) }, review.Outcome{Kind: review.Waiting, Head: old}},
		"a_conflict_sends_it_back":                 {func(p *pulls) { p.view.Mergeable = str("CONFLICTING") }, review.Outcome{Kind: review.SendBack, Reason: "it conflicts with its base branch", Head: head}},
		"a_change_request_sends_it":                {func(p *pulls) { p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}} }, review.Outcome{Kind: review.SendBack, Reason: "a review asks for changes", Head: head}},
		"a_draft_with_a_conflict_says_it_is_draft": {func(p *pulls) { p.view.Mergeable = str("CONFLICTING"); p.view.IsDraft = new(true) }, review.Outcome{Kind: review.SendBack, Reason: "it conflicts with its base branch", Draft: true, Head: head}},
		"both_name_both": {func(p *pulls) {
			p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}}
			p.view.Mergeable = str("CONFLICTING")
		}, review.Outcome{Kind: review.SendBack, Reason: "a review asks for changes, it conflicts with its base branch", Head: head}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := cleanPulls()
			tt.change(p)

			got, err := review.Watch(t.Context(), p, 7)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWatch_AFailedReadIsAnErrorAndNeverWaiting(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"PRView", "OpenChangeRequests", "UnresolvedThreads"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := cleanPulls()
			p.fails[name] = true

			_, err := review.Watch(t.Context(), p, 7)

			var unread *review.UnreadError

			require.ErrorAs(t, err, &unread)
		})
	}
}
