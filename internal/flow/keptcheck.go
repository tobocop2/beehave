package flow

import (
	"fmt"
	"slices"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// keepWaitPrefix starts the name of the wait that asks the owner to keep the new closing issues of the member of an item: keep-<item>.
const keepWaitPrefix = "keep-"

// trustKept refuses a call whose member keeps an issue that no recorded owner answer keeps. The kept list is a field that the workflow passes,
// so the merge takes an issue of it only when an answer to wait(name = "keep-<item>") of this run or of an earlier run of the run item keeps that issue.
func (s *script) trustKept(call batchCall) error {
	for _, m := range call.members {
		if err := s.trustMemberKept(m); err != nil {
			return fmt.Errorf("merge %q: %w", call.name, err)
		}
	}

	return nil
}

// trustMemberKept refuses the first kept issue of the member that no recorded owner answer keeps. A member that keeps nothing reads nothing.
func (s *script) trustMemberKept(m batch.Member) error {
	if len(m.Kept) == 0 {
		return nil
	}

	answered, err := s.keptByOwner(m.Item)
	if err != nil {
		return err
	}

	for _, issue := range m.Kept {
		if !slices.Contains(answered, issue) {
			return fmt.Errorf("item %s keeps %s, and no owner answer keeps it. The merge takes a kept issue only from an answer to wait(name = %q) in this run or in an earlier run of item %s. Pass the kept list that hold_scope returned",
				m.Item, issue, keepWaitPrefix+m.Item, s.in.Item)
		}
	}

	return nil
}

// keptByOwner lists the issues that the recorded answers to wait(name = "keep-<item>") keep, in this run and in the earlier runs of the run item.
// It reads the records that the library reads with earlier_reads, and records no step: a record of a step never changes, so a replay finds the same answers or more.
func (s *script) keptByOwner(item string) ([]model.IssueRef, error) {
	answers, err := s.ownerAnswers(keepWaitPrefix + item)
	if err != nil {
		return nil, err
	}

	var kept []model.IssueRef
	for _, answer := range answers {
		kept = append(kept, workflow.KeepIssues(answer.Answer)...)
	}

	return kept, nil
}

// ownerAnswers reads the answers to the wait steps of the name, in this run and in the earlier runs of the run item.
func (s *script) ownerAnswers(name string) ([]waitRead, error) {
	ids, err := s.runIDs(s.in.Item, true)
	if err != nil {
		return nil, err
	}

	return collect[waitRead](s.e.ctx, ids, stepPrefix(waitStepKind, name))
}
