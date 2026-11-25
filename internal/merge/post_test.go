package merge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// poster is a fake Poster that writes its statuses into the writes of the world, so a test sees the post and the merge call in one order.
type poster struct {
	w        *world
	statuses []model.CommitStatus
	skipped  string
	err      error
}

func (p *poster) Post(context.Context) (string, error) {
	if p.err != nil || p.skipped != "" {
		return p.skipped, p.err
	}

	for _, status := range p.statuses {
		p.w.writes = append(p.w.writes, "Post "+status.Context+" "+string(status.State)+" "+status.SHA)
	}

	return "", nil
}

var gateStatus = model.CommitStatus{SHA: sha, Context: "beehave/gate", State: model.StatusSuccess, Description: "d"}

// posting is a request whose run posts the gate status through a fake poster over w.
func posting(w *world) (merge.Request, *poster) {
	p := &poster{w: w, statuses: []model.CommitStatus{gateStatus}}
	req := request()
	req.Post = p

	return req, p
}

func TestRun_TheStatusesArePostedAfterEveryGuardAndRightBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req, _ := posting(w)

	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop, all(result))
	assert.Equal(t, []string{"Post beehave/gate success " + sha, "MergePR 5 " + sha}, w.writes)
}

func TestRun_APullRequestThatAGuardRefusesGetsNoStatus(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"headRefOid": moved})
	req, _ := posting(w)

	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, w.writes, "a refused pull request carries no status")
}

func TestRun_ADraftWhoseReadyStepFailsGetsNoStatusAndNoMergeCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"isDraft": true})
	w.fails["SetDraft"] = true
	req, _ := posting(w)
	req.AllowReady = true

	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop, all(result))
	assert.Equal(t, []string{"SetDraft 5 false"}, w.writes, "the ready step comes before the post, so a draft that stays a draft carries no status")
}

func TestRun_ABaseThatMovedBeforeTheMergeCallDefersAndPostsNoStatus(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.tipReads = []tipRead{{sha: tipSHA, found: true}, {sha: moved, found: true}}
	req, _ := posting(w)

	result := w.run(req)

	assert.Equal(t, merge.Transient, result.Reason, all(result))
	assert.Empty(t, w.writes, "a deferred pull request carries no status")
}

func TestRun_APostThatFailsDefersBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req, p := posting(w)
	p.err = errors.New("the forge refused the status")

	result := w.run(req)

	assert.Equal(t, merge.Transient, result.Reason)
	assert.Equal(t, "REFUSE: the forge refused the status", last(result))
	assert.Empty(t, w.writes, "no merge call without the statuses")
}

func TestRun_APostThatWasSkippedSaysWhyAndMerges(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req, p := posting(w)
	p.skipped = "no commit status was posted: no writer"

	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, result.Lines, "WARN: no commit status was posted: no writer")
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
}

func TestRun_AResumeAfterAPostThatWorkedAndAMergeThatFailedPostsTheSameStatusAgainAndMerges(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["MergePR"] = true
	req, _ := posting(w)

	first := w.run(req)

	assert.Equal(t, merge.Transient, first.Reason, all(first))

	w.fails["MergePR"] = false
	second := w.run(req)

	assert.Equal(t, merge.Done, second.Stop, all(second))
	post := "Post beehave/gate success " + sha
	assert.Equal(t, []string{post, "MergePR 5 " + sha, post, "MergePR 5 " + sha}, w.writes, "the second post carries the same context, state and commit")
}
