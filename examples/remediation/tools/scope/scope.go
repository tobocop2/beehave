// Package scope refuses an issue that a pull request started to close after its first review.
// The first review of a key records the issues its body closes. A later issue needs an owner-add row for the key.
package scope

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
	"github.com/tobocop2/obee/internal/check"
)

// CodeRefused is the exit code of a pull request that closes a new issue with no approval of the owner.
const CodeRefused = 3

// Records is what the check reads from the ledger.
type Records interface {
	Closes(key string) ([]string, error)
	OwnerAdds(key string) ([]string, error)
}

// Reader is the forge read of the check.
type Reader interface {
	PRBody(ctx context.Context, pr int) (string, error)
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

// Check compares the issues the pull request body closes now with the ones the first review recorded.
// It reads the body only when the ledger holds a first review for the key.
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
	body, err := reader.PRBody(ctx, number)
	if err != nil {
		refusal := &check.Refusal{Message: fmt.Sprintf("cannot read the body of pull request %s in %s.", request.PR, request.Repo)}

		return Result{}, errors.Join(refusal, err)
	}

	approved, err := records.OwnerAdds(key)
	if err != nil {
		return Result{}, err
	}

	if fresh := newIssues(ledger.ClosingNumbers(body), then, approved); fresh != "" {
		return refused(request.PR, key, fresh, then), nil
	}

	return ok("OK: pull request %s closes no issue that is new since its first review without an owner row.", request.PR), nil
}

func refused(pr, key, fresh string, then []string) Result {
	return Result{Code: CodeRefused, Lines: []string{
		fmt.Sprintf("REFUSED: pull request %s closes%s now, and did not at its first review (then: %s).", pr, fresh, strings.Join(then, " ")),
		"         Move each one to a new pull request. To keep one here, ask the owner and run",
		fmt.Sprintf("         remedy ledger add %s <issue> owner-add --state <folder> --words <the owner's words>", key),
		"         once for each issue.",
	}}
}

// newIssues lists, as " #12 #13", each closed issue that is in neither list.
func newIssues(closing, then, approved []string) string {
	var fresh strings.Builder

	for _, issue := range closing {
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
