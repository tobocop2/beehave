package github

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const scopesMarker = "Token scopes:"

// workflowScope is the scope of a token that can change the workflow files of a repository.
const workflowScope = "workflow"

var (
	commitID    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	quotedScope = regexp.MustCompile(`'([^']+)'`)
)

// workflowRefusal is the text of GitHub's refusal of a push that changes a workflow file without the workflow permission.
const workflowRefusal = "create or update workflow"

// refused returns the refusal of a failed write. It keeps the first line of what gh printed as the detail, and marks a workflow permission refusal.
func refused(done model.CommandResult, format string, args ...any) *forge.Error {
	refusal := forge.Refuse(format, args...)
	refusal.Detail = firstLine(done.Stderr)

	if strings.Contains(done.Stderr, workflowRefusal) {
		refusal.Err = forge.ErrWorkflowPermission
	}

	return refusal
}

// firstLine returns the first non-blank line of the text, trimmed.
func firstLine(text string) string {
	for line := range strings.Lines(text) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}

	return ""
}

// MergePR merges a pull request with a merge commit, and only if its head is still sha.
// A pin that is not a full 40-character commit id is refused before gh runs, because gh merges unpinned when the pin is empty.
func (f *Forge) MergePR(ctx context.Context, pr int, sha string) error {
	if !commitID.MatchString(sha) {
		return forge.Refuse("cannot merge pull request %d without the full commit id of its head", pr)
	}

	done := f.run.Run(ctx, []string{argPR, argMerge, strconv.Itoa(pr), argRepo, f.repo, "--merge", "--match-head-commit", sha})
	if done.ExitCode != 0 {
		return refused(done, "cannot merge pull request %d", pr)
	}

	return nil
}

// AutoMergePR asks GitHub to merge a pull request with a merge commit once its requirements pass, and only if its head is still sha.
// GitHub holds the queued merge and drops it when the head moves. The call returns when GitHub accepts the request; the pull request may merge later.
// The pin rule of MergePR applies.
func (f *Forge) AutoMergePR(ctx context.Context, pr int, sha string) error {
	if !commitID.MatchString(sha) {
		return forge.Refuse("cannot queue the merge of pull request %d without the full commit id of its head", pr)
	}

	done := f.run.Run(ctx, []string{argPR, argMerge, strconv.Itoa(pr), argRepo, f.repo, "--merge", "--auto", "--match-head-commit", sha})
	if done.ExitCode != 0 {
		return refused(done, "cannot queue the merge of pull request %d", pr)
	}

	return nil
}

// DisableAutoMergePR turns off the queued merge of a pull request. It succeeds when none is armed.
// GitHub checks the expected head only when it enables the queued merge, and keeps it armed after a push by a user with write access,
// so the caller turns it off when the head is not the gated one.
func (f *Forge) DisableAutoMergePR(ctx context.Context, pr int) error {
	done := f.run.Run(ctx, []string{argPR, argMerge, strconv.Itoa(pr), argRepo, f.repo, "--disable-auto"})
	if done.ExitCode != 0 {
		return refused(done, "cannot turn off the queued merge of pull request %d", pr)
	}

	return nil
}

// CanWriteCI answers from the scope line of the active account: yes when it holds the workflow scope, no when it does not.
// gh prints the line only for a token that has scopes, so no line with a clean exit is unknown, and no line with a failed exit is a refusal.
func (f *Forge) CanWriteCI(ctx context.Context) (model.CIWrite, error) {
	done := f.run.Run(ctx, []string{"auth", "status", "--active"})

	// gh prints the status to stderr when it exits with an error, so both streams are read.
	for line := range strings.Lines(done.Stdout + "\n" + done.Stderr) {
		if _, list, found := strings.Cut(line, scopesMarker); found {
			return writeOf(scopeNames(list)), nil
		}
	}

	if done.ExitCode != 0 {
		return model.CIWriteUnknown, refused(done, "gh auth status exited with code %d", done.ExitCode)
	}

	return model.CIWriteUnknown, nil
}

// writeOf is the answer of a list of scopes: yes when it holds the workflow scope.
func writeOf(scopes []string) model.CIWrite {
	if slices.Contains(scopes, workflowScope) {
		return model.CIWriteYes
	}

	return model.CIWriteNo
}

// Login returns the login of the active account: the account the review poll reads for. An empty answer is a refusal.
// It is a method of this backend only; the forge contract does not list it.
func (f *Forge) Login(ctx context.Context) (string, error) {
	login, err := f.text(ctx, []string{argAPI, "user", argJQ, ".login"}, "cannot read the login of the active account")
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(login) == "" {
		return "", forge.Refuse("the active account has no login")
	}

	return strings.TrimSpace(login), nil
}

// scopeNames returns the quoted words of a scope list. The text none has no quotes and gives no scope.
func scopeNames(list string) []string {
	var names []string

	for _, match := range quotedScope.FindAllStringSubmatch(list, -1) {
		names = append(names, match[1])
	}

	return names
}

// CreateBranch makes a branch at the commit. An existing branch is an error from the forge.
func (f *Forge) CreateBranch(ctx context.Context, branch, sha string) error {
	done := f.run.Run(ctx, []string{argAPI, "-X", argPost, "repos/" + f.repo + "/git/refs", "-f", "ref=refs/heads/" + branch, "-f", "sha=" + sha})
	if done.ExitCode != 0 {
		return refused(done, "cannot create the branch %s", branch)
	}

	return nil
}

// SetBase retargets a pull request onto another branch.
func (f *Forge) SetBase(ctx context.Context, pr int, base string) error {
	done := f.run.Run(ctx, []string{argPR, "edit", strconv.Itoa(pr), argRepo, f.repo, "--base", base})
	if done.ExitCode != 0 {
		return refused(done, "cannot set the base of pull request %d to %s", pr, base)
	}

	return nil
}

// notAuthor returns the refusal of a body write because the pull request is not the work of the owner.
func notAuthor(format string, args ...any) *model.NotAuthorError {
	return &model.NotAuthorError{Reason: fmt.Sprintf(format, args...)}
}

// requireAuthor refuses unless the author of the pull request is the login of the token that the forge command line uses.
// An author or a token login that cannot be read refuses too, so a failed read never lets a write through.
func (f *Forge) requireAuthor(ctx context.Context, pr int) error {
	view, err := f.PRView(ctx, pr, "author")
	if err != nil {
		return notAuthor("cannot read its author: %v", err)
	}

	self := f.run.Run(ctx, []string{argAPI, "user", "--jq", ".login"})
	login := strings.TrimSpace(self.Stdout)

	if self.ExitCode != 0 || login == "" {
		return notAuthor("cannot read the login of the token, so its author %q cannot be compared", view.Author.Login)
	}

	if !strings.EqualFold(view.Author.Login, login) {
		return notAuthor("its author is %q, not the owner", view.Author.Login)
	}

	return nil
}

// SetBody replaces the body of a pull request that the owner wrote: it is the only write of a body, so no caller can skip the author check.
// The body goes as one argument, so no word of it reads as a flag.
func (f *Forge) SetBody(ctx context.Context, pr int, body string) error {
	if err := f.requireAuthor(ctx, pr); err != nil {
		return err
	}

	done := f.run.Run(ctx, []string{argPR, "edit", strconv.Itoa(pr), argRepo, f.repo, argBody, body})
	if done.ExitCode != 0 {
		return refused(done, "cannot set the body of pull request %d", pr)
	}

	return nil
}
