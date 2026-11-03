// Package github implements forge.Forge over the gh command line: pull requests, reviews and checks.
package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	argAPI         = "api"
	argPR          = "pr"
	argMerge       = "merge"
	argView        = "view"
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
)

var (
	errNonZero  = errors.New("the command failed")
	errNullPage = errors.New("a page is null")
	errNullItem = errors.New("an item is null")
)

// Forge is the gh reads for one repository, written owner/name.
type Forge struct {
	repo  string
	owner string
	name  string
	run   proc.Runner
}

// New returns the forge of repo, written owner/name. Any other spelling is refused.
func New(repo string, run proc.Runner) (*Forge, error) {
	owner, name, found := strings.Cut(repo, "/")
	if !found || !model.IsRepoPart(owner) || !model.IsRepoPart(name) {
		return nil, forge.Refuse("The repository must be written owner/name.")
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
		return nil, forge.Refusal(fmt.Sprintf("could not read the reviews of pull request %d", pr), err)
	}

	return reviews, nil
}

// integrationAgrees refuses a round whose base and integration branch are both set and differ.
func integrationAgrees(base, integration string) error {
	if base == "" || integration == "" || base == integration {
		return nil
	}

	return forge.Refuse("The base branch is %s and the integration branch is %s. They must be the same branch.\n"+
		"        Both name the branch that pull requests target. Set both to that branch.\n"+
		"        Or leave the integration branch empty to turn the check off.", base, integration)
}

// PRBody returns the description text of a pull request.
func (f *Forge) PRBody(ctx context.Context, pr int) (string, error) {
	done := f.run.Run(ctx, []string{argPR, argView, strconv.Itoa(pr), "-R", f.repo, argJSON, "body", argJQ, ".body"})
	if done.ExitCode != 0 {
		return "", forge.Refuse("cannot read the body of pull request %d in %s, so the Closes lines at the first review are unknown.", pr, f.repo)
	}

	return done.Stdout, nil
}

// PRView returns the named fields of one pull request. A field not named is zero.
func (f *Forge) PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error) {
	list := strings.Join(fields, ",")

	done := f.run.Run(ctx, []string{argPR, argView, strconv.Itoa(pr), argRepo, f.repo, argJSON, list})
	if done.ExitCode != 0 {
		return model.PullRequest{}, forge.Refuse("cannot read pull request %d (%s)", pr, list)
	}

	view, err := model.Decode[model.PullRequest](cmdPRView, done.Stdout)
	if err != nil {
		return view, forge.Refusal(fmt.Sprintf("cannot read pull request %d (%s)", pr, list), err)
	}

	return view, nil
}

// CloneURL is the HTTPS address of the repository.
func (f *Forge) CloneURL() string { return "https://github.com/" + f.repo + ".git" }

// ItemFields declares the fields that Item returns.
func (*Forge) ItemFields() []string { return ItemFields() }

// ItemFields returns the fields that a GitHub forge declares for an item.
func ItemFields() []string { return []string{"branch", "head_sha", "pr", "author"} }

// Item returns the branch, head commit, number and author of a pull request. A field the forge does not send is empty.
func (f *Forge) Item(ctx context.Context, pr int) (map[string]string, error) {
	view, err := f.PRView(ctx, pr, "number", "headRefName", "headRefOid", "author")
	if err != nil {
		return nil, err
	}

	return map[string]string{
		"branch":   deref(view.HeadRefName),
		"head_sha": deref(view.HeadRefOid),
		"pr":       strconv.Itoa(view.Number),
		"author":   view.Author.Login,
	}, nil
}

func deref(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

// countsAnswer is the answer of the pull request read of the REST API. A nil count means the API sent null or no field.
type countsAnswer struct {
	Commits      *int `json:"commits"`
	ChangedFiles *int `json:"changed_files"`
}

// PullCounts returns the forge's own count of the commits and the changed files of a pull request. A count that is not sent is an error.
func (f *Forge) PullCounts(ctx context.Context, pr int) (commits, files int, err error) {
	base := fmt.Sprintf("cannot read the counts of pull request %d", pr)

	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/pulls/" + strconv.Itoa(pr)})
	if done.ExitCode != 0 {
		return 0, 0, forge.Refuse("%s", base)
	}

	answer, err := model.Decode[countsAnswer](cmdAPI, done.Stdout)
	if err != nil {
		return 0, 0, forge.Refusal(base, err)
	}

	if answer.Commits == nil || answer.ChangedFiles == nil {
		return 0, 0, forge.Refuse("%s: the answer has no count", base)
	}

	return *answer.Commits, *answer.ChangedFiles, nil
}

// commitRow is one commit of the REST list of a pull request. A null committer account is a committer with no linked login.
type commitRow struct {
	Committer *struct {
		Login string `json:"login"`
	} `json:"committer"`
	Commit struct {
		Committer struct {
			Email string `json:"email"`
		} `json:"committer"`
	} `json:"commit"`
}

// Committers returns the committer of each commit of a pull request, in the order of the commits.
// The commit list of gh pr view holds the authors only, so this reads the REST list.
func (f *Forge) Committers(ctx context.Context, pr int) ([]model.Committer, error) {
	rows, err := readPages[commitRow](ctx, f, "pulls/"+strconv.Itoa(pr)+"/commits")
	if err != nil {
		return nil, forge.Refusal(fmt.Sprintf("could not read the committers of pull request %d", pr), err)
	}

	committers := make([]model.Committer, len(rows))

	for i, row := range rows {
		committers[i].Email = row.Commit.Committer.Email

		if row.Committer != nil {
			committers[i].Login = row.Committer.Login
		}
	}

	return committers, nil
}

// PRList runs gh pr list with the limit and the arguments, which must name --json. An answer that fills the limit may be cut off and is refused.
func (f *Forge) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	shown := strings.Join(args, " ")

	done := f.run.Run(ctx, append([]string{argPR, "list", argRepo, f.repo, "--limit", strconv.Itoa(limit)}, args...))
	if done.ExitCode != 0 {
		return nil, forge.Refuse("cannot list pull requests (%s)", shown)
	}

	rows, err := model.Decode[[]model.PullRequest](cmdPRList, done.Stdout)
	if err != nil {
		return nil, forge.Refusal(fmt.Sprintf("cannot parse the pull request list (%s)", shown), err)
	}

	if len(rows) >= limit {
		return nil, forge.Refuse("the pull request list returned %d of a limit of %d, so it may be cut off (%s). Raise the limit.", len(rows), limit, shown)
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
			return nil, forge.Refusal(base, err)
		}

		return rows, nil
	}

	if reportsNoChecks(done.Stderr) {
		return []model.Check{}, nil
	}

	first, _, _ := strings.Cut(done.Stderr, "\n")

	return nil, forge.Refuse("%s: %s", base, first)
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
