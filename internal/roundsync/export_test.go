package roundsync

import (
	"context"
	"time"
)

// DraftLimit is the most time that the return to draft of a landing may take.
const DraftLimit = draftLimit

// LandWithin is Land with limit in place of DraftLimit, so a test of a return to draft that hangs does not wait 30 seconds.
func LandWithin(ctx context.Context, forge Forge, req LandRequest, limit time.Duration) (Result, error) {
	return landWithin(ctx, forge, req, limit)
}
