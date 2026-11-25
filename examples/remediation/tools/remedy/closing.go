package remedy

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/ledger"
	"github.com/tobocop2/beehave/internal/model"
)

// bodyReader is the read of a forge that cannot list closing references. It gives the body of a pull request.
type bodyReader interface {
	PRBody(ctx context.Context, pr int) (string, error)
}

// Listing is a forge that lists the closing references of a pull request. One read gives the issues, the base branch of the pull request and the default branch.
type Listing interface {
	bodyReader
	ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error)
}

// closingIssues reads the issues a pull request closes. It is the read of the first review row of the ledger.
// GitHub links a closing keyword only for a pull request into the default branch, so a pull request into any other branch has no list there.
// https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue
// A forge with the list gives it for a pull request into the default branch. Any other pull request, and any forge with no list, is read by the closing keywords of the body.
func closingIssues(ctx context.Context, forge bodyReader, pr int) ([]int, error) {
	lister, ok := forge.(Listing)
	if !ok {
		return bodyIssues(ctx, forge, pr)
	}

	// The base branch and the default branch come from the same answer as the list. A name the answer lacks is a refusal, never a guess.
	refs, err := lister.ClosingRefs(ctx, pr)
	if err != nil {
		return nil, errors.Join(&check.Refusal{Message: fmt.Sprintf("cannot read the issues that pull request %d closes.", pr)}, err)
	}

	switch {
	case refs.BaseBranch == "":
		return nil, &check.Refusal{Message: fmt.Sprintf("cannot read the base branch of pull request %d.", pr)}
	case refs.DefaultBranch == "":
		return nil, &check.Refusal{Message: fmt.Sprintf("cannot read the default branch of the repository of pull request %d.", pr)}
	case refs.BaseBranch != refs.DefaultBranch:
		return bodyIssues(ctx, forge, pr)
	}

	return refs.Issues, nil
}

// bodyIssues reads the body of the pull request and returns the issues its closing keywords name.
// It misses an issue that is linked in the sidebar and not named in the body.
func bodyIssues(ctx context.Context, forge bodyReader, pr int) ([]int, error) {
	body, err := forge.PRBody(ctx, pr)
	if err != nil {
		return nil, errors.Join(&check.Refusal{Message: fmt.Sprintf("cannot read the body of pull request %d.", pr)}, err)
	}

	var numbers []int

	for _, text := range ledger.ClosingNumbers(body) {
		number, err := strconv.Atoi(text)
		if err != nil {
			return nil, &check.Refusal{Message: fmt.Sprintf("the body of pull request %d names the issue number %s, which is too large.", pr, text)}
		}

		numbers = append(numbers, number)
	}

	return numbers, nil
}
