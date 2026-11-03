package github

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/forge"
)

const scopesMarker = "Token scopes:"

var (
	commitID    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	quotedScope = regexp.MustCompile(`'([^']+)'`)
)

// MergePR merges a pull request with a merge commit, and only if its head is still sha.
// A pin that is not a full 40-character commit id is refused before gh runs, because gh merges unpinned when the pin is empty.
func (f *Forge) MergePR(ctx context.Context, pr int, sha string) error {
	if !commitID.MatchString(sha) {
		return forge.Refuse("cannot merge pull request %d without the full commit id of its head", pr)
	}

	done := f.run.Run(ctx, []string{argPR, "merge", strconv.Itoa(pr), argRepo, f.repo, "--merge", "--match-head-commit", sha})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot merge pull request %d", pr)
	}

	return nil
}

// TokenScopes returns the scopes of the token of the active account. An answer with no scope line is a refusal: the scopes are unknown, not empty.
func (f *Forge) TokenScopes(ctx context.Context) ([]string, error) {
	done := f.run.Run(ctx, []string{"auth", "status", "--active"})

	// gh prints the status to stderr when it exits with an error, so both streams are read.
	for line := range strings.Lines(done.Stdout + "\n" + done.Stderr) {
		if _, list, found := strings.Cut(line, scopesMarker); found {
			return scopeNames(list), nil
		}
	}

	return nil, forge.Refuse("cannot read the token scopes of gh")
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
	done := f.run.Run(ctx, []string{argAPI, "-X", "POST", "repos/" + f.repo + "/git/refs", "-f", "ref=refs/heads/" + branch, "-f", "sha=" + sha})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot create the branch %s", branch)
	}

	return nil
}

// SetBase retargets a pull request onto another branch.
func (f *Forge) SetBase(ctx context.Context, pr int, base string) error {
	done := f.run.Run(ctx, []string{argPR, "edit", strconv.Itoa(pr), argRepo, f.repo, "--base", base})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot set the base of pull request %d to %s", pr, base)
	}

	return nil
}

// AddAssignee assigns a pull request or an issue to a login. The forge keeps one entry for a login, so the same call twice changes nothing.
func (f *Forge) AddAssignee(ctx context.Context, number int, login string) error {
	done := f.run.Run(ctx, []string{argAPI, "-X", "POST", "repos/" + f.repo + "/issues/" + strconv.Itoa(number) + "/assignees", "-f", "assignees[]=" + login})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot assign #%d to %s", number, login)
	}

	return nil
}

// SetBody replaces the body of a pull request. The body goes as one argument, so no word of it reads as a flag.
func (f *Forge) SetBody(ctx context.Context, pr int, body string) error {
	done := f.run.Run(ctx, []string{argPR, "edit", strconv.Itoa(pr), argRepo, f.repo, "--body", body})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot set the body of pull request %d", pr)
	}

	return nil
}
