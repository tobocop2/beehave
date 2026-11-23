package flow

import (
	"cmp"
	"context"
	"time"
)

// releasePoll is how often the claim step reads the cancel markers while an earlier attempt of its item is not released yet.
const releasePoll = 50 * time.Millisecond

// awaitRelease waits until no other run of the item has a cancel marker: the marker stays until the give-back of that run has released its claim and its worktree.
// A replace cancels attempt k and starts attempt k+1 at once, and the engine releases k when its function returns, so the claim of k+1 would meet the claim of k.
// The wait ends at the release time of the clock. A marker that stays longer, because an engine stopped, leaves the refusal of the claim to speak: it names the holder.
func (e *Engine) awaitRelease(ctx context.Context, item, self string) error {
	timeout := time.NewTimer(cmp.Or(e.deps.Clock.Release, releaseTimeout))
	defer timeout.Stop()

	poll := time.NewTicker(cmp.Or(e.deps.Clock.Poll, releasePoll))
	defer poll.Stop()

	for e.otherMarked(ctx, item, self) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return nil
		case <-poll.C:
		}
	}

	return nil
}

// otherMarked reports whether a run of the item other than self has a cancel marker. A marker folder that cannot be read is logged, and the claim does not wait for it.
func (e *Engine) otherMarked(ctx context.Context, item, self string) bool {
	pending, err := pendingCancels(e.dir, e.logger)
	if err != nil {
		e.logger.WarnContext(ctx, "cannot read the cancel markers, so the claim does not wait for an earlier attempt", "item", item, "error", err)

		return false
	}

	for id, mark := range pending {
		if mark.Item == item && id != self {
			return true
		}
	}

	return false
}
