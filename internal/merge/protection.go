package merge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
)

// ErrNoProtectionRead is the error of a forge that cannot read the protection of a branch.
var ErrNoProtectionRead = errors.New("this forge cannot read the protection of a branch")

// Protections is what the merge reads to learn what the forge requires of the base. A forge that also implements Protections lets the merge refuse a base
// that would not require the gate and the review. A forge that cannot read it returns ErrNoProtectionRead.
type Protections interface {
	Protection(ctx context.Context, branch string) (model.Protection, error)
}

// required lists the contexts that a base must require, in the order that a refusal names them.
func required() []string {
	return []string{model.ContextGate, model.ContextReview}
}

// protected refuses before any write when the protection of the base does not match the project. A project with credentials needs a base that requires
// both beehest statuses and a pull request, because the forge, not beehest, enforces them. A project with no credentials posts no status, so a base that
// requires one would block every merge, and the merge says so. A read that fails for such a project does not refuse: the forge still enforces its own rules.
func (a *attempt) protected() *refusal {
	reads, ok := a.forge.(Protections)
	if !ok {
		return a.unreadable(nil)
	}

	protection, err := reads.Protection(a.ctx, a.req.Base)
	if err != nil {
		return a.unreadable(err)
	}

	if !a.req.Credentialed {
		return a.needsCredentials(protection)
	}

	return a.incomplete(protection)
}

// incomplete refuses a base whose protection lacks a pull request rule or a required status. The refusal names the base, the missing pieces and the fix.
func (a *attempt) incomplete(protection model.Protection) *refusal {
	missing := missingOf(protection)
	if len(missing) == 0 {
		return nil
	}

	return refuse(
		fmt.Sprintf("REFUSE: the base %s does not require %s.", a.req.Base, strings.Join(missing, " and ")),
		fmt.Sprintf("        The project has `credentials`, so the forge must enforce the gate and the review of %s. Without that rule a member could merge with no verdict.", a.req.Base),
		fmt.Sprintf("        Add a ruleset on %s that requires a pull request and the statuses %s (docs/design/trust.md, section \"The ruleset\"), then run the merge again.", a.req.Base, strings.Join(required(), " and ")),
	)
}

// unreadable is the answer when the protection of the base was not read. A project with credentials is refused. A failed read is a cause that can pass,
// and a forge that cannot read at all is not. A project with no credentials goes on.
func (a *attempt) unreadable(err error) *refusal {
	if !a.req.Credentialed {
		return nil
	}

	if err != nil && !errors.Is(err, ErrNoProtectionRead) {
		return transient(fmt.Sprintf("REFUSE: cannot read the protection of the base %s: %v", a.req.Base, err))
	}

	return refuse(
		fmt.Sprintf("REFUSE: the forge %s cannot read the protection of the base %s.", a.req.ForgeName, a.req.Base),
		"        The project has `credentials`, and merge needs the protection to prove that the forge requires the gate and the review.",
		"        Use a forge that can read it, or remove `credentials` from the project to keep the guards of a project with no writer.",
	)
}

// needsCredentials refuses a project with no credentials when the base requires a status that beehest posts. Nothing posts it, so no merge could pass.
func (a *attempt) needsCredentials(protection model.Protection) *refusal {
	for _, name := range required() {
		if !slices.Contains(protection.RequiredStatuses, name) {
			continue
		}

		return refuse(
			fmt.Sprintf("REFUSE: the base %s requires the status %s, and the project has no `credentials` key, so beehest cannot post it.", a.req.Base, name),
			"        The project needs credentials: add a `writer` entry to `credentials` (docs/design/trust.md, section \"Credential config\"), then run the merge again.",
		)
	}

	return nil
}

// missingOf lists what the protection lacks: a pull request rule, and each required context.
func missingOf(protection model.Protection) []string {
	var missing []string

	if !protection.PullRequest {
		missing = append(missing, "a pull request")
	}

	for _, name := range required() {
		if !slices.Contains(protection.RequiredStatuses, name) {
			missing = append(missing, name)
		}
	}

	return missing
}
