package forge

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/model"
)

// Ability is one optional thing that a forge can do. A forge has an ability when it implements the interface of the ability.
// The string is the name that a refusal prints.
type Ability string

// The abilities. The core methods of Forge are not in the list: every forge has them.
const (
	AbilityStatuses    Ability = "commit statuses"
	AbilityOpener      Ability = "opening pull requests"
	AbilityStacks      Ability = "native stacks"
	AbilitySyncer      Ability = "syncing a branch"
	AbilityAssignees   Ability = "issue assignees"
	AbilityAssociation Ability = "the association of an author"
	AbilityReadyMarks  Ability = "ready marks"
	AbilityBoards      Ability = "project boards"
	AbilityQueue       Ability = "queued merges"
	AbilityAncestry    Ability = "commit ancestry"
	AbilityTrees       Ability = "commit trees"
	AbilityLogs        Ability = "check logs"
	AbilityAlerts      Ability = "security alerts"
)

// Abilities returns every ability, in the order of the list above.
func Abilities() []Ability {
	return []Ability{
		AbilityStatuses, AbilityOpener, AbilityStacks, AbilitySyncer, AbilityAssignees, AbilityAssociation, AbilityReadyMarks,
		AbilityBoards, AbilityQueue, AbilityAncestry, AbilityTrees, AbilityLogs, AbilityAlerts,
	}
}

// ErrNoCapability is the cause when a forge cannot do what a caller asks. A caller tests for it with errors.Is.
var ErrNoCapability = errors.New("the forge cannot do this")

// NoCapability returns an error with the cause ErrNoCapability. It names the ability and the reason.
func NoCapability(a Ability, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrNoCapability, a, reason)
}

// Queuer is the optional side of a forge that queues a merge. A forge that lacks it cannot merge a pull request in a native stack.
// AutoMergePR returns when the forge has accepted the queued merge, not when it has merged.
// The forge checks the pin (the expected head) when it enables the queued merge, not when it merges, and a push by a user with write access keeps it armed.
// DisableAutoMergePR turns the queued merge off, and it succeeds when none is armed.
type Queuer interface {
	AutoMergePR(ctx context.Context, pr int, sha string) error
	DisableAutoMergePR(ctx context.Context, pr int) error
}

// Ancestry is the optional side of a forge that compares commits. Compare reads the status and both counts of the comparison of commit with the base,
// and Comparison.Contains is true when commit is the base tip or lies in its history.
type Ancestry interface {
	Compare(ctx context.Context, commit, base string) (model.Comparison, error)
}

// Trees is the optional side of a forge that reads trees. CommitTree returns the tree id of a commit.
// TreeBlobs returns the files of a tree, path to mode and content id; a list cut short is an error.
type Trees interface {
	CommitTree(ctx context.Context, commit string) (string, error)
	TreeBlobs(ctx context.Context, tree string) (map[string]string, error)
}

// Logs is the optional side of a forge that reads the log of a check. FailedLog returns the log of the failed steps of the check run at link.
type Logs interface {
	FailedLog(ctx context.Context, link string) (string, error)
}

// Prober is the optional side of a forge whose ability depends on the instance, such as a plan tier or a feature that the owner switched off.
// Probe returns nil when the ability works, and an error with the cause ErrNoCapability when it does not. Any other error is a failed read.
// A caller calls Probe only for an ability that Has reports.
type Prober interface {
	Probe(ctx context.Context, a Ability) error
}

// Has reports whether the forge implements the interface of the ability. It makes no call to the forge.
func Has(f any, a Ability) bool {
	var ok bool

	switch a {
	case AbilityStatuses:
		_, ok = f.(Statuses)
	case AbilityOpener:
		_, ok = f.(Opener)
	case AbilityStacks:
		_, ok = f.(Stacker)
	case AbilitySyncer:
		_, ok = f.(Syncer)
	case AbilityAssignees:
		_, ok = f.(AssigneeReads)
	case AbilityAssociation:
		_, ok = f.(AssociationReads)
	case AbilityReadyMarks:
		_, ok = f.(ReadyReads)
	case AbilityBoards:
		_, ok = f.(Boarder)
	case AbilityQueue:
		_, ok = f.(Queuer)
	case AbilityAncestry:
		_, ok = f.(Ancestry)
	case AbilityTrees:
		_, ok = f.(Trees)
	case AbilityLogs:
		_, ok = f.(Logs)
	case AbilityAlerts:
		_, ok = f.(AlertReads)
	}

	return ok
}

// Need checks that the forge has each ability and that its Probe, when it has one, accepts each. The error joins one ErrNoCapability for each ability that is missing.
// A Probe that fails with another error returns that error.
func Need(ctx context.Context, f any, needs ...Ability) error {
	var missing []error

	prober, probes := f.(Prober)

	for _, need := range needs {
		if !Has(f, need) {
			missing = append(missing, NoCapability(need, "this forge does not have it"))

			continue
		}

		if !probes {
			continue
		}

		if err := prober.Probe(ctx, need); err != nil {
			missing = append(missing, err)
		}
	}

	return errors.Join(missing...)
}
