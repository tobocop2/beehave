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
		origin, err := e.readOrigin(c, in)

		return struct{}{}, e.admit(c, in, startRefused, origin, err)
	})

	return err
}

// readOrigin reads who wrote the item: its issue, which the reader finds when the tracker keeps its items as issues on the forge, and, for a project
// with a forge, the pull request that the one read of its facts finds. A build with no reader reads nothing for a project with no forge.
func (e *Engine) readOrigin(ctx context.Context, in ItemInput) (model.Origin, error) {
	forged := e.facts(in.Project).Forge != ""

	reader := e.deps.Preflight
	if reader == nil {
		if forged {
			return model.Origin{}, errNoPreflight
		}

		return model.Origin{}, nil
	}

	issue, err := reader.IssueAuthor(ctx, in.Project, in.Item)
	if err != nil || !forged {
		return model.Origin{Issue: issue}, err
	}

	facts, err := reader.ReadPull(ctx, in.Project, in.Item, true)

	return model.Origin{Issue: issue, Pull: facts.Authored()}, err
}

// admit refuses the item of the run when another account wrote its issue or its pull request, or when the read failed, unless the run
// is in the sandbox on a build host declared credentials = "none" that passes its check.
func (e *Engine) admit(ctx context.Context, in ItemInput, outcome string, origin model.Origin, read error) error {
	from, foreign := foreignPart(origin, read)
	if !foreign {
		return nil
	}

	fix, allowed := e.mayHoldForeign(ctx, in)
	if allowed {
		return nil
	}

	return fmt.Errorf(`item %s %s: %s. A foreign item runs only in the sandbox on a build host with credentials = "none". %s, then run beehave run %s`, in.Item, outcome, from, fix, in.Item)
}

// foreignPart says which part of the item another account wrote. A failed read counts as foreign.
func foreignPart(origin model.Origin, read error) (string, bool) {
	switch {
	case read != nil:
		return fmt.Sprintf("beehave cannot read who wrote it (%v)", read), true
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
		return fmt.Sprintf("Fix the build host: %v", err), false
	}

	return "", true
}

// owns reports whether a later read of the item with the id judges its author: the id names the item of the run, or is empty, and the project has a forge.
func (s *script) owns(id string) bool {
	return (id == "" || id == s.in.Item) && s.e.facts(s.in.Project).Forge != ""
}

// admitOwn applies the rule of the start again to the pull request that a later read of the item of the run found, because a pull request can open
// after the start. The check judges the pull request of that one read, which carries its author. A read that is not own passes.
func (s *script) admitOwn(ctx context.Context, own bool, pull model.Authored) error {
	if !own {
		return nil
	}

	return s.e.admit(ctx, s.in, readRefused, model.Origin{Pull: pull}, nil)
}
