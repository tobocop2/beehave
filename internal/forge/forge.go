// Package forge reads pull requests, reviews and checks from GitHub through the gh command line.
package forge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	argAPI         = "api"
	argPR          = "pr"
	argRepo        = "--repo"
	argJSON        = "--json"
	argPaginate    = "--paginate"
	argJQ          = "--jq"
	cmdAPI         = "gh api"
	cmdGraphQL     = "gh api graphql"
	cmdPRList      = "gh pr list"
	cmdPRChecks    = "gh pr checks"
	cmdPRView      = "gh pr view"
	noChecksPrefix = "no checks reported on the "
	roundEnvFile   = "round.env"
)

var (
	errNonZero  = errors.New("the command failed")
	errNullPage = errors.New("a page is null")
	errNullItem = errors.New("an item is null")
)

// Runner runs gh with the arguments and returns what it printed.
type Runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// Forge is the gh reads for one repository, written owner/name.
type Forge struct {
	repo  string
	owner string
	name  string
	run   Runner
}

// New returns the forge of repo, written owner/name. Any other spelling is refused.
func New(repo string, run Runner) (*Forge, error) {
	owner, name, found := strings.Cut(repo, "/")
	if !found || !model.IsRepoPart(owner) || !model.IsRepoPart(name) {
		return nil, refuse("REPO is not set to owner/name. Export it, or put it in %s.", roundEnvFile)
	}

	return &Forge{repo: repo, owner: owner, name: name, run: run}, nil
}

// NewExec returns a forge that runs the gh binary on PATH.
func NewExec(repo string) (*Forge, error) {
	return New(repo, proc.NewExecRunner("gh", ""))
}

// readPages reads every page of a paginated REST list as one list.
func readPages[T any](ctx context.Context, f *Forge, path string) ([]T, error) {
	done := f.run.Run(ctx, []string{argAPI, argPaginate, "--slurp", "repos/" + f.repo + "/" + path})
	if done.ExitCode != 0 {
		return nil, errNonZero
	}

	pages, err := model.Decode[[]*[]*T](cmdAPI+" "+path, done.Stdout)
	if err != nil {
		return nil, err
	}

	return joinPages(cmdAPI+" "+path, pages)
}

// joinPages joins the pages into one list. A null page or a null item is an answer of the wrong shape, not an empty one.
func joinPages[T any](command string, pages []*[]*T) ([]T, error) {
	items := []T{}

	for _, page := range pages {
		if page == nil {
			return nil, &model.DecodeError{Command: command, Cause: errNullPage}
		}

		for _, item := range *page {
			if item == nil {
				return nil, &model.DecodeError{Command: command, Cause: errNullItem}
			}

			items = append(items, *item)
		}
	}

	return items, nil
}

// Reviews returns every review of a pull request, all pages joined.
func (f *Forge) Reviews(ctx context.Context, pr int) ([]model.Review, error) {
	reviews, err := readPages[model.Review](ctx, f, "pulls/"+strconv.Itoa(pr)+"/reviews")
	if err != nil {
		return nil, refusal(fmt.Sprintf("could not read the reviews of pull request %d", pr), err)
	}

	return reviews, nil
}

// MyLastReview returns the latest review of login that is a verdict. A comment, a dismissed review and a draft are not.
func MyLastReview(reviews []model.Review, login string) (model.Review, bool) {
	for i := len(reviews) - 1; i >= 0; i-- {
		review := reviews[i]
		if review.User.Login == login && !notAVerdict(review.State) {
			return review, true
		}
	}

	return model.Review{}, false
}

func notAVerdict(state model.ReviewState) bool {
	return state == model.ReviewCommented || state == model.ReviewDismissed || state == model.ReviewPending
}

// IntegrationAgrees refuses a round whose base and integration branch are both set and differ.
func IntegrationAgrees(base, integration string) error {
	if base == "" || integration == "" || base == integration {
		return nil
	}

	return refuse("BASE is %s and INTEGRATION_BRANCH is %s. BASE must equal INTEGRATION_BRANCH.\n"+
		"        Both name the branch that pull requests target. Keep the branch that pull requests target, and set both to it in %s.\n"+
		"        Or unset INTEGRATION_BRANCH to turn the check off.", base, integration, roundEnvFile)
}

// PRBody returns the description text of a pull request.
func (f *Forge) PRBody(ctx context.Context, pr int) (string, error) {
	done := f.run.Run(ctx, []string{argPR, "view", strconv.Itoa(pr), "-R", f.repo, argJSON, "body", argJQ, ".body"})
	if done.ExitCode != 0 {
		return "", refuse("cannot read the body of pull request %d in %s, so the Closes lines at the first review are unknown.", pr, f.repo)
	}

	return done.Stdout, nil
}

// PRView returns the named fields of one pull request. A field not named is zero.
func (f *Forge) PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error) {
	list := strings.Join(fields, ",")

	done := f.run.Run(ctx, []string{argPR, "view", strconv.Itoa(pr), argRepo, f.repo, argJSON, list})
	if done.ExitCode != 0 {
		return model.PullRequest{}, refuse("cannot read pull request %d (%s)", pr, list)
	}

	view, err := model.Decode[model.PullRequest](cmdPRView, done.Stdout)
	if err != nil {
		return view, refusal(fmt.Sprintf("cannot read pull request %d (%s)", pr, list), err)
	}

	return view, nil
}

// PRList runs gh pr list with the limit and the arguments, which must name --json. An answer that fills the limit may be cut off and is refused.
func (f *Forge) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	shown := strings.Join(args, " ")

	done := f.run.Run(ctx, append([]string{argPR, "list", argRepo, f.repo, "--limit", strconv.Itoa(limit)}, args...))
	if done.ExitCode != 0 {
		return nil, refuse("cannot list pull requests (%s)", shown)
	}

	rows, err := model.Decode[[]model.PullRequest](cmdPRList, done.Stdout)
	if err != nil {
		return nil, refusal(fmt.Sprintf("cannot parse the pull request list (%s)", shown), err)
	}

	if len(rows) >= limit {
		return nil, refuse("the pull request list returned %d of a limit of %d, so it may be cut off (%s). Raise the limit.", len(rows), limit, shown)
	}

	return rows, nil
}

// PRChecks returns the checks of a pull request as gh judges them. A pull request with no checks gives an empty list.
func (f *Forge) PRChecks(ctx context.Context, pr int) ([]model.Check, error) {
	base := fmt.Sprintf("cannot read the checks of pull request %d", pr)

	done := f.run.Run(ctx, []string{argPR, "checks", strconv.Itoa(pr), argRepo, f.repo, argJSON, "name,bucket,workflow,link"})
	if done.ExitCode == 0 {
		rows, err := model.Decode[[]model.Check](cmdPRChecks, done.Stdout)
		if err != nil {
			return nil, refusal(base, err)
		}

		return rows, nil
	}

	if reportsNoChecks(done.Stderr) {
		return []model.Check{}, nil
	}

	first, _, _ := strings.Cut(done.Stderr, "\n")

	return nil, refuse("%s: %s", base, first)
}

// reportsNoChecks reports whether a line of the error text is the one gh prints for a pull request with no checks.
func reportsNoChecks(stderr string) bool {
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, noChecksPrefix) {
			return true
		}
	}

	return false
}
