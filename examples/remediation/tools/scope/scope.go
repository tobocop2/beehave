// Package scope refuses an issue that a pull request started to close after its first review.
// The first review of a key records the issues its pull request closes. A later issue needs an owner-add row for the key.
package scope

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/ledger"
	"github.com/tobocop2/beehave/internal/model"
)

// CodeRefused is the exit code of a pull request that closes a new issue with no approval of the owner.
const CodeRefused = 3

// Records is what the check reads from the ledger.
type Records interface {
	Closes(key string) ([]string, error)
	OwnerAdds(key string) ([]string, error)
}

// Reader is the forge read of the check: the numbers of the issues that merging the pull request closes.
type Reader interface {
	ClosingIssues(ctx context.Context, pr int) ([]int, error)
}

// BodyReader is the read of a forge that cannot list closing references. It gives the body of a pull request.
type BodyReader interface {
	PRBody(ctx context.Context, pr int) (string, error)
}

// Listing is a forge that lists the closing references of a pull request. One read gives the issues, the base branch of the pull request and the default branch.
type Listing interface {
	BodyReader
	ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error)
}

// ReaderOf picks the read of the issues a pull request closes. It is the one read of the scope check and of the first review row of the ledger.
// GitHub links a closing keyword only for a pull request into the default branch, so a pull request into any other branch has no list there.
// https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue
// A forge with the list gives it for a pull request into the default branch. Any other pull request, and any forge with no list, is read by the closing keywords of the body.
func ReaderOf(forge BodyReader) ByBranch { return ByBranch{forge} }

// ByBranch is the Reader that ReaderOf makes.
type ByBranch struct{ forge BodyReader }

// ClosingIssues reads the list of the forge for a pull request into the default branch, and the body for any other.
func (b ByBranch) ClosingIssues(ctx context.Context, pr int) ([]int, error) {
	listing, ok := b.forge.(Listing)
	if !ok {
		return FromBody(b.forge).ClosingIssues(ctx, pr)
	}

	return listed(ctx, listing, pr)
}

// listed reads the closing references of a Listing forge for a pull request into the default branch, and the body for any other.
// The base branch and the default branch come from the same answer as the list. A name the answer lacks is a refusal, never a guess.
func listed(ctx context.Context, forge Listing, pr int) ([]int, error) {
	refs, err := forge.ClosingRefs(ctx, pr)
	if err != nil {
		return nil, errors.Join(&check.Refusal{Message: fmt.Sprintf("cannot read the issues that pull request %d closes.", pr)}, err)
	}

	switch {
	case refs.BaseBranch == "":
		return nil, &check.Refusal{Message: fmt.Sprintf("cannot read the base branch of pull request %d.", pr)}
	case refs.DefaultBranch == "":
		return nil, &check.Refusal{Message: fmt.Sprintf("cannot read the default branch of the repository of pull request %d.", pr)}
	case refs.BaseBranch != refs.DefaultBranch:
		return FromBody(forge).ClosingIssues(ctx, pr)
	}

	return refs.Issues, nil
}

// FromBody makes a BodyClosing, a Reader, of a forge with no closing-reference read. It takes the issues from the closing keywords of the body,
// so it misses an issue that is linked in the sidebar and not named in the body.
func FromBody(forge BodyReader) BodyClosing { return BodyClosing{forge} }

// BodyClosing is the Reader that FromBody makes.
type BodyClosing struct{ forge BodyReader }

// ClosingIssues reads the body of the pull request and returns the issues its closing keywords name.
func (b BodyClosing) ClosingIssues(ctx context.Context, pr int) ([]int, error) {
	body, err := b.forge.PRBody(ctx, pr)
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

// Request names the pull request. Key is the ledger key of the item, the pull request number when empty. Repo is named in an error.
type Request struct {
	PR   string
	Key  string
	Repo string
}

// Result is the verdict. Code is 0 or CodeRefused. Lines are the lines to show.
type Result struct {
	Code  int
	Lines []string
}

// pullNumber reads the number of the pull request. Any run of digits is a number; one above the range of int is too large.
func pullNumber(text string) (int, error) {
	if text == "" || strings.TrimLeft(text, "0123456789") != "" {
		return 0, &check.Refusal{Message: "name the pull request by its number, digits only."}
	}

	number, err := strconv.Atoi(text)
	if err != nil {
		return 0, &check.Refusal{Message: "the pull request number is too large."}
	}

	return number, nil
}

// Check compares the issues the pull request closes now with the ones the first review recorded.
// It reads the forge only when the ledger holds a first review for the key.
func Check(ctx context.Context, records Records, reader Reader, request Request) (Result, error) {
	number, err := pullNumber(request.PR)
	if err != nil {
		return Result{}, err
	}

	key := request.Key
	if key == "" {
		key = request.PR
	}

	then, err := records.Closes(key)
	if err != nil {
		return Result{}, err
	}

	if len(then) == 0 {
		return ok("OK: the ledger holds no review row for %s, so its scope is not fixed yet.", key), nil
	}

	return compare(ctx, records, reader, request, key, number, then)
}

// compare reads the body and the owner rows, and refuses an issue that is in neither list.
func compare(ctx context.Context, records Records, reader Reader, request Request, key string, number int, then []string) (Result, error) {
	closing, err := reader.ClosingIssues(ctx, number)
	if err != nil {
		return Result{}, unreadable(err, request)
	}

	approved, err := records.OwnerAdds(key)
	if err != nil {
		return Result{}, err
	}

	if fresh := newIssues(closing, then, approved); fresh != "" {
		return refused(request.PR, key, fresh, then), nil
	}

	return ok("OK: pull request %s closes no issue that is new since its first review without an owner row.", request.PR), nil
}

// unreadable makes a refusal of a failed read. A refusal of the reader already names the pull request and stays as it is.
func unreadable(err error, request Request) error {
	var already *check.Refusal
	if errors.As(err, &already) {
		return err
	}

	refusal := &check.Refusal{Message: fmt.Sprintf("cannot read the issues that pull request %s closes in %s.", request.PR, request.Repo)}

	return errors.Join(refusal, err)
}

func refused(pr, key, fresh string, then []string) Result {
	return Result{Code: CodeRefused, Lines: []string{
		fmt.Sprintf("REFUSED: pull request %s closes%s now, and did not at its first review (then: %s).", pr, fresh, strings.Join(then, " ")),
		"         Move each one to a new pull request. To keep one here, ask the owner and run",
		fmt.Sprintf("         remedy ledger add %s <issue> owner-add --state <folder> --words <the owner's words>", key),
		"         once for each issue.",
	}}
}

// newIssues lists, as " #12 #13" in numeric order, each closed issue that is in neither list.
func newIssues(closing []int, then, approved []string) string {
	var fresh strings.Builder

	for _, number := range slices.Sorted(slices.Values(closing)) {
		issue := strconv.Itoa(number)
		if !has(then, issue) && !has(approved, issue) {
			fresh.WriteString(" #" + issue)
		}
	}

	return fresh.String()
}

func ok(format string, args ...any) Result {
	return Result{Lines: []string{fmt.Sprintf(format, args...)}}
}

// has reports whether issues names the issue. Two spellings of one number, such as 12 and 012, name one issue.
func has(issues []string, issue string) bool {
	return slices.ContainsFunc(issues, func(other string) bool {
		return strings.TrimLeft(other, "0") == strings.TrimLeft(issue, "0")
	})
}
