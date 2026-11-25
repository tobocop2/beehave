package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// originStepName is the recorded step before the claim of every run: it reads who wrote the item and refuses a foreign item that the run may not hold.
const originStepName = "origin"

// The outcomes that a refusal of a foreign item names: at the start of a run, and at a later read of its pull request.
const (
	startRefused = "was not started"
	readRefused  = "stopped"
)

// origin runs the recorded step that refuses an item from another account before the claim, unless the run may hold one.
func (e *Engine) origin(ctx dbos.Context, in ItemInput) error {
	_, err := runStep(e, ctx, originStepName, func(c context.Context) (struct{}, error) {
		return struct{}{}, e.admit(c, in, startRefused)
	})

	return err
}

// admit refuses the item of the run when another account wrote its issue or its pull request, or when that cannot be read, unless the run
// is in the sandbox on a build host declared credentials = "none" that passes its check. A project with no forge reads nothing.
func (e *Engine) admit(ctx context.Context, in ItemInput, outcome string) error {
	if e.facts(in.Project).Forge == "" {
		return nil
	}

	from, foreign := e.foreignPart(ctx, in)
	if !foreign {
		return nil
	}

	fix, allowed := e.mayHoldForeign(ctx, in)
	if allowed {
		return nil
	}

	return fmt.Errorf(`item %s %s: %s. A foreign item runs only in the sandbox on a build host with credentials = "none". %s, then run beehave run %s`, in.Item, outcome, from, fix, in.Item)
}

// foreignPart reads who wrote the item and says which part another account wrote. A build with no reader and a failed read count as foreign.
func (e *Engine) foreignPart(ctx context.Context, in ItemInput) (string, bool) {
	origin, err := model.Origin{}, error(errNoPreflight)
	if e.deps.Preflight != nil {
		origin, err = e.deps.Preflight.Origin(ctx, in.Project, in.Item)
	}

	switch {
	case err != nil:
		return fmt.Sprintf("beehave cannot read who wrote it (%v)", err), true
	case origin.Issue.Foreign():
		return fmt.Sprintf("its issue #%d comes from another account (%s)", origin.Issue.Number, origin.Issue.Association), true
	case origin.Pull.Foreign():
		return fmt.Sprintf("its pull request #%d comes from another account (%s)", origin.Pull.Number, origin.Pull.Association), true
	}

	return "", false
}

// mayHoldForeign says whether the run may hold a foreign item: its mode is on and the build host is declared credentials = "none" and passes its check.
// When it may not, the text says what to change.
func (e *Engine) mayHoldForeign(ctx context.Context, in ItemInput) (string, bool) {
	if in.Sandbox != model.SandboxOn {
		return `Remove sandbox = "off" from the project and declare the host`, false
	}

	if e.deps.Hosts == nil {
		return "Use a build that can check the build host", false
	}

	if err := e.deps.Hosts.CheckCredentials(ctx, in.Project); err != nil {
		return fmt.Sprintf(`Declare the build host with credentials = "none" (%v)`, err), false
	}

	return "", true
}

// admitOwn runs the rule of the start again at a later read of the pull request of the item of the run, because a pull request can open after the start.
// A read of another item is not the item of the run, and passes.
func (s *script) admitOwn(ctx context.Context, id string) error {
	if id != "" && id != s.in.Item {
		return nil
	}

	return s.e.admit(ctx, s.in, readRefused)
}
