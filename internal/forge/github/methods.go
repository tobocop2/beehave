package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	cmdSearch  = "gh search"
	cmdRepo    = "gh repo view"
	cmdRefRead = "gh api git/ref"
	notFound   = "HTTP 404"
)

// defaultAnswer is the answer of gh repo view --json defaultBranchRef. A null ref means the repository has no default branch.
type defaultAnswer struct {
	Ref *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

func (a defaultAnswer) name() string {
	if a.Ref == nil {
		return ""
	}

	return a.Ref.Name
}

// refAnswer is the answer of the git ref read.
type refAnswer struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// Comments returns every conversation comment of an issue or pull request, all pages joined.
func (f *Forge) Comments(ctx context.Context, number int) ([]model.Comment, error) {
	rows, err := readPages[model.Comment](ctx, f, "issues/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return nil, forge.Refusal(fmt.Sprintf("could not read the comments of #%d", number), err)
	}

	return rows, nil
}

// ReviewComments returns every inline comment of a pull request, all pages joined.
func (f *Forge) ReviewComments(ctx context.Context, pr int) ([]model.Comment, error) {
	rows, err := readPages[model.Comment](ctx, f, "pulls/"+strconv.Itoa(pr)+"/comments")
	if err != nil {
		return nil, forge.Refusal(fmt.Sprintf("could not read the review comments of pull request %d", pr), err)
	}

	return rows, nil
}

// Search returns the numbers of the open items that involve a user and changed since a date. An answer that fills the limit may be cut off and is refused.
func (f *Forge) Search(ctx context.Context, req model.SearchRequest) ([]int, error) {
	kind := string(req.Kind)

	done := f.run.Run(ctx, searchArgs(f.repo, req))
	if done.ExitCode != 0 {
		return nil, forge.Refuse("cannot search %s", kind)
	}

	rows, err := model.Decode[[]model.PullRequest](cmdSearch, done.Stdout)
	if err != nil {
		return nil, forge.Refusal("cannot search "+kind, err)
	}

	if len(rows) >= req.Limit {
		return nil, forge.Refuse("the %s search returned %d of a limit of %d, so it may be cut off. Raise the limit.", kind, len(rows), req.Limit)
	}

	return numbersOf(rows), nil
}

func numbersOf(rows []model.PullRequest) []int {
	numbers := make([]int, len(rows))
	for i, row := range rows {
		numbers[i] = row.Number
	}

	return numbers
}

func searchArgs(repo string, req model.SearchRequest) []string {
	return []string{
		"search", string(req.Kind), argRepo, repo, "--involves", req.Involves, "--state", "open",
		"--updated", ">=" + req.Since, argLimit, strconv.Itoa(req.Limit), argJSON, "number",
	}
}

// DefaultBranch returns the name of the default branch of the repository. A repository that names none is a refusal.
func (f *Forge) DefaultBranch(ctx context.Context) (string, error) {
	const base = "cannot read the default branch"

	done := f.run.Run(ctx, []string{"repo", argView, f.repo, argJSON, "defaultBranchRef"})
	if done.ExitCode != 0 {
		return "", forge.Refuse("%s of %s", base, f.repo)
	}

	answer, err := model.Decode[defaultAnswer](cmdRepo, done.Stdout)
	if err != nil {
		return "", forge.Refusal(base, err)
	}

	if answer.name() == "" {
		return "", forge.Refuse("%s: %s names none", base, f.repo)
	}

	return answer.name(), nil
}

// BranchRef returns the tip of a branch on the remote. A branch that does not exist is found false with no error; any other failed read is an error.
func (f *Forge) BranchRef(ctx context.Context, branch string) (sha string, found bool, err error) {
	base := "cannot read the branch " + branch

	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/git/ref/heads/" + escapeSegments(branch)})
	if done.ExitCode != 0 {
		if f.refGone(ctx, done) {
			return "", false, nil
		}

		return "", false, forge.Refuse("%s", base)
	}

	sha, err = refTip(done.Stdout, base)

	return sha, err == nil, err
}

// refTip reads the commit from the answer of the ref read.
func refTip(stdout, base string) (string, error) {
	answer, err := model.Decode[refAnswer](cmdRefRead, stdout)
	if err != nil {
		return "", forge.Refusal(base, err)
	}

	if answer.Object.SHA == "" {
		return "", forge.Refuse("%s: the answer holds no commit", base)
	}

	return answer.Object.SHA, nil
}

// escapeSegments escapes each segment of a branch name for a URL path and keeps the slashes between them.
func escapeSegments(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}

// refGone reports whether a failed ref read means the branch is gone. A 404 is the same answer for a missing branch and a missing repository,
// so on a 404 the repository is read: if it reads, the branch is gone.
func (f *Forge) refGone(ctx context.Context, done model.CommandResult) bool {
	if !strings.Contains(done.Stderr, notFound) {
		return false
	}

	return f.run.Run(ctx, []string{argAPI, "repos/" + f.repo, argJQ, ".full_name"}).ExitCode == 0
}

// PostComment adds a comment to a pull request. The body goes as one argument, so no word of it reads as a flag.
func (f *Forge) PostComment(ctx context.Context, pr int, body string) error {
	done := f.run.Run(ctx, []string{argPR, "comment", strconv.Itoa(pr), argRepo, f.repo, argBody, body})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot comment on pull request %d", pr)
	}

	return nil
}

// SetDraft converts a pull request to a draft, or marks it ready for review.
func (f *Forge) SetDraft(ctx context.Context, pr int, draft bool) error {
	args := []string{argPR, "ready", strconv.Itoa(pr), argRepo, f.repo}
	if draft {
		args = append(args, "--undo")
	}

	done := f.run.Run(ctx, args)
	if done.ExitCode != 0 && draft {
		return forge.Refuse("cannot convert pull request %d to draft", pr)
	}

	if done.ExitCode != 0 {
		return forge.Refuse("cannot mark pull request %d ready", pr)
	}

	return nil
}
