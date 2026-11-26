package flow

import (
	"context"

	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// runWrites is the write handle of one item run: every write of the run to the forge, the tracker or a program goes through it.
type runWrites struct {
	e    *Engine
	run  string
	item string
}

// writes returns the write handle of the run of the script.
func (s *script) writes() runWrites {
	return runWrites{e: s.e, run: workflowID(s.ctx), item: s.in.Item}
}

// trusted is the claim read of an item run: the context of its claim when the run holds a trusted claim now.
func (w runWrites) trusted(context.Context) (context.Context, error) {
	return w.e.claimed(w.run, w.item)
}

// claimedCall reads the claim with read, then runs call under ctx, which the end of the claim also ends.
func claimedCall(ctx context.Context, read func(context.Context) (context.Context, error), call func(context.Context) error) error {
	claim, err := read(ctx)
	if err != nil {
		return err
	}

	bound, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer context.AfterFunc(claim, func() { cancel(context.Cause(claim)) })()

	return call(bound)
}

// PRComment writes a comment on the pull request pr, or on the pull request of an item when pr is 0.
func (w runWrites) PRComment(ctx context.Context, project, item string, pr int, key, text string) (number int, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		number, err = w.e.deps.Forge.Comment(c, project, item, pr, key, text)

		return err
	})

	return number, err
}

// Open opens the pull request of an item.
func (w runWrites) Open(ctx context.Context, project, item string, req OpenRequest) (opened OpenResult, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		opened, err = w.e.deps.Forge.Open(c, project, item, req)

		return err
	})

	return opened, err
}

// Push pushes a head of the clone of the project to a branch.
func (w runWrites) Push(ctx context.Context, project, item, branch, head, expect string) error {
	return claimedCall(ctx, w.trusted, func(c context.Context) error { return w.e.deps.Forge.Push(c, project, item, branch, head, expect) })
}

// MarkReady marks the pull request of an item ready for review.
func (w runWrites) MarkReady(ctx context.Context, project, item, repo string) error {
	return claimedCall(ctx, w.trusted, func(c context.Context) error { return w.e.deps.Batch.SetDraft(c, project, item, repo, false) })
}

// Create files a tracker item under the key.
func (w runWrites) Create(ctx context.Context, project, key string, item trackerstore.NewItem) (id string, created bool, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		id, created, err = w.e.deps.Writer.Create(c, project, key, item)

		return err
	})

	return id, created, err
}

// SetStatus sets the status of a tracker item.
func (w runWrites) SetStatus(ctx context.Context, project, id string, status trackerstore.Status) error {
	return claimedCall(ctx, w.trusted, func(c context.Context) error { return w.e.deps.Writer.SetStatus(c, project, id, status) })
}

// ItemComment writes a comment on a tracker item under the key.
func (w runWrites) ItemComment(ctx context.Context, project, id, key, text string) error {
	return claimedCall(ctx, w.trusted, func(c context.Context) error { return w.e.deps.Writer.Comment(c, project, id, key, text) })
}

// Run starts the program of a command or a write and waits for it.
func (w runWrites) Run(ctx context.Context, request CommandRequest) (out CommandOutput, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		out, err = w.e.deps.Commands.Run(c, request)

		return err
	})

	return out, err
}
