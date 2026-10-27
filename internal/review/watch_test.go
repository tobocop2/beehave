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
		"merged":                    {func(p *pulls) { p.view.State = str("MERGED") }, review.Outcome{Kind: review.Merged}},
		"merged_beats_a_conflict":   {func(p *pulls) { p.view.State = str("MERGED"); p.view.Mergeable = str("CONFLICTING") }, review.Outcome{Kind: review.Merged}},
		"clean_and_open":            {func(*pulls) {}, review.Outcome{Kind: review.Waiting}},
		"an_open_thread_waits":      {func(p *pulls) { p.threads = make([]model.ReviewThread, 1) }, review.Outcome{Kind: review.Waiting}},
		"a_moved_head_waits":        {func(p *pulls) { p.view.HeadRefOid = str(old) }, review.Outcome{Kind: review.Waiting}},
		"a_conflict_sends_it_back":  {func(p *pulls) { p.view.Mergeable = str("CONFLICTING") }, review.Outcome{Kind: review.SendBack, Reason: "it conflicts with its base branch"}},
		"a_change_request_sends_it": {func(p *pulls) { p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}} }, review.Outcome{Kind: review.SendBack, Reason: "a review asks for changes"}},
		"both_name_both": {func(p *pulls) {
			p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}}
			p.view.Mergeable = str("CONFLICTING")
		}, review.Outcome{Kind: review.SendBack, Reason: "a review asks for changes, it conflicts with its base branch"}},
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
