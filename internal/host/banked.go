package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	unbornCommit   = "0000000000000000000000000000000000000000"
	bankedRefSpace = "refs/heads/obee-banked/"
	shortCommit    = 12
	tipsPerCall    = 1000
	commitType     = "commit"
	tagType        = "tag"
	refFields      = 5
	// refFormat prints, tab separated: the object type, the object name, the type and name one level down (empty for a commit), the ref name.
	refFormat = "--format=%(objecttype)%09%(objectname)%09%(*objecttype)%09%(*objectname)%09%(refname)"
)

// Unbanked is one commit on the host that no remote with a host part holds.
type Unbanked struct {
	Repo   string
	Ref    string
	Commit string
}

// Banked lists the work in repos that is not on a remote. Every branch tip, tag, stash entry and worktree HEAD must be an
// ancestor of a head or a tag that `git ls-remote` reports on a remote with a host part, or on one of the extra URLs.
// A remote that cannot be read holds nothing. A git command that cannot read the repository is an error.
func Banked(ctx context.Context, git proc.Runner, repos []string, extra ...string) ([]Unbanked, error) {
	var missing []Unbanked

	for _, repo := range repos {
		items, err := unbankedIn(ctx, git, repo, extra)
		if err != nil {
			return nil, err
		}

		missing = append(missing, items...)
	}

	return missing, nil
}

func unbankedIn(ctx context.Context, git proc.Runner, repo string, extra []string) ([]Unbanked, error) {
	local, err := localCommits(ctx, git, repo)
	if err != nil {
		return nil, err
	}

	remote, err := remoteCommits(ctx, git, repo, extra)
	if err != nil {
		return nil, err
	}

	var missing []Unbanked

	for _, commit := range local {
		if !isAncestorOfAny(ctx, git, repo, commit.Commit, remote) {
			commit.Repo = repo
			missing = append(missing, commit)
		}
	}

	return missing, nil
}

// Bank makes sure every commit in repos is on a remote. It pushes what is missing to private, which must have a host part, and checks again.
// With no usable private remote, or when a push does not bank the work, it refuses.
//
// Each missing commit goes to its own branch, `obee-banked/<12 characters of the commit>`, on the private remote. The push is never a force push.
// All repositories of the host share that one namespace. The branches stay until someone deletes them, and a CI system on the remote can see them.
// A call that fails after some pushes leaves those branches in place. A new call pushes the same commit to the same name, so a retry is safe.
// Only commits are banked: uncommitted and untracked files, and the index, are lost when the host goes.
func Bank(ctx context.Context, git proc.Runner, repos []string, private string) error {
	missing, err := Banked(ctx, git, repos)
	if err != nil || len(missing) == 0 {
		return err
	}

	if !model.RemoteHasHost(private) {
		return fmt.Errorf("%w: %s. Set `host.private_remote` to a remote with a host part, or push the work", ErrUnbanked, describe(missing))
	}

	if err = push(ctx, git, missing, private); err != nil {
		return err
	}

	missing, err = Banked(ctx, git, repos, private)
	if err != nil || len(missing) == 0 {
		return err
	}

	return fmt.Errorf("%w: %s", ErrUnbanked, describe(missing))
}

// push sends each commit to a branch of the private remote that no work uses.
func push(ctx context.Context, git proc.Runner, missing []Unbanked, private string) error {
	for _, item := range missing {
		spec := item.Commit + ":" + bankedRefSpace + item.Commit[:min(shortCommit, len(item.Commit))]

		if result := git.Run(ctx, []string{"-C", item.Repo, "push", "--", private, spec}); result.ExitCode != 0 {
			return fmt.Errorf("%w: push of %s from %s failed: %s", ErrUnbanked, item.Ref, item.Repo, strings.TrimSpace(result.Stderr))
		}
	}

	return nil
}

func describe(missing []Unbanked) string {
	parts := make([]string, len(missing))
	for i, item := range missing {
		parts[i] = item.Repo + " " + item.Ref
	}

	return strings.Join(parts, ", ")
}

// localCommits lists the commits of the branches, tags, stash entries and worktrees of one repository.
func localCommits(ctx context.Context, git proc.Runner, repo string) ([]Unbanked, error) {
	var found []Unbanked

	seen := map[string]bool{}

	for _, read := range []func(context.Context, proc.Runner, string) ([]Unbanked, error){refCommits, stashCommits, worktreeCommits} {
		items, err := read(ctx, git, repo)
		if err != nil {
			return nil, err
		}

		for _, item := range items {
			if !seen[item.Commit] {
				seen[item.Commit] = true
				found = append(found, item)
			}
		}
	}

	return found, nil
}

func run(ctx context.Context, git proc.Runner, repo string, args ...string) (string, error) {
	result := git.Run(ctx, append([]string{"-C", repo}, args...))
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git %s in %s: exit %d: %s", args[0], repo, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result.Stdout, nil
}

func refCommits(ctx context.Context, git proc.Runner, repo string) ([]Unbanked, error) {
	out, err := run(ctx, git, repo, "for-each-ref", refFormat, "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}

	var found []Unbanked

	for _, line := range lines(out) {
		item, err := refCommit(ctx, git, repo, strings.Split(line, "\t"))
		if err != nil {
			return nil, err
		}

		if item.Commit != "" {
			found = append(found, item)
		}
	}

	return found, nil
}

// refCommit reads one for-each-ref line, split into fields. A ref that names a tree or a blob, or a tag that peels to one, holds no commit: the result has none.
// A line with the wrong number of fields is an error: a ref that cannot be read may hold work, so the host is not called banked.
func refCommit(ctx context.Context, git proc.Runner, repo string, fields []string) (Unbanked, error) {
	if len(fields) != refFields {
		return Unbanked{}, fmt.Errorf("git for-each-ref in %s: cannot read the line %q", repo, strings.Join(fields, "\t"))
	}

	kind, object, peeledKind, peeledObject, ref := fields[0], fields[1], fields[2], fields[3], fields[4]

	switch {
	case kind == commitType:
		return Unbanked{Ref: ref, Commit: object}, nil
	case kind == tagType && peeledKind == commitType:
		return Unbanked{Ref: ref, Commit: peeledObject}, nil
	case kind == tagType && peeledKind == tagType:
		return peelToCommit(ctx, git, repo, ref)
	default:
		return Unbanked{}, nil
	}
}

// peelToCommit asks git for the commit of a tag of a tag. Git 2.43 peels one level in a for-each-ref field; only ^{commit} peels every level.
// With --quiet, exit code 1 means the ref does not peel to a commit; any other failure is a real error.
func peelToCommit(ctx context.Context, git proc.Runner, repo, ref string) (Unbanked, error) {
	peeled := git.Run(ctx, []string{"-C", repo, "rev-parse", "--verify", "--quiet", ref + "^{commit}"})
	if peeled.ExitCode > 1 {
		return Unbanked{}, fmt.Errorf("git rev-parse in %s: exit %d: %s", repo, peeled.ExitCode, strings.TrimSpace(peeled.Stderr))
	}

	return Unbanked{Ref: ref, Commit: strings.TrimSpace(peeled.Stdout)}, nil
}

func stashCommits(ctx context.Context, git proc.Runner, repo string) ([]Unbanked, error) {
	out, err := run(ctx, git, repo, "for-each-ref", "--format=%(refname)", "refs/stash")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil, err
	}

	out, err = run(ctx, git, repo, "reflog", "show", "--format=%H", "refs/stash")
	if err != nil {
		return nil, err
	}

	var found []Unbanked

	for i, commit := range lines(out) {
		found = append(found, Unbanked{Ref: fmt.Sprintf("stash@{%d}", i), Commit: commit})
	}

	return found, nil
}

func worktreeCommits(ctx context.Context, git proc.Runner, repo string) ([]Unbanked, error) {
	out, err := run(ctx, git, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var found []Unbanked

	for _, line := range lines(out) {
		if commit, isHead := strings.CutPrefix(line, "HEAD "); isHead && commit != unbornCommit {
			found = append(found, Unbanked{Ref: "worktree HEAD", Commit: commit})
		}
	}

	return found, nil
}

// remoteCommits lists the commits that the remotes with a host part report as heads or tags.
func remoteCommits(ctx context.Context, git proc.Runner, repo string, extra []string) ([]string, error) {
	out, err := configuredRemotes(ctx, git, repo)
	if err != nil {
		return nil, err
	}

	var commits []string

	for _, remote := range append(out, extra...) {
		if model.RemoteHasHost(remote) {
			commits = append(commits, listRemote(ctx, git, repo, remote)...)
		}
	}

	return commits, nil
}

// listRemote lists the commits at the heads and tags of one remote. A remote that cannot be read has none.
func listRemote(ctx context.Context, git proc.Runner, repo, remote string) []string {
	result := git.Run(ctx, []string{"-C", repo, "ls-remote", "--heads", "--tags", "--", remote})
	if result.ExitCode != 0 {
		return nil
	}

	var commits []string

	for _, line := range lines(result.Stdout) {
		commit, _, _ := strings.Cut(line, "\t")
		commits = append(commits, commit)
	}

	return commits
}

// configuredRemotes lists the URLs of the remotes of a repository. A repository with no remote has none; exit code 1 means no match.
func configuredRemotes(ctx context.Context, git proc.Runner, repo string) ([]string, error) {
	result := git.Run(ctx, []string{"-C", repo, "config", "--get-regexp", `^remote\..*\.url$`})
	if result.ExitCode > 1 {
		return nil, fmt.Errorf("git config in %s: exit %d: %s", repo, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	var urls []string

	for _, line := range lines(result.Stdout) {
		_, remote, _ := strings.Cut(line, " ")
		urls = append(urls, remote)
	}

	return urls, nil
}

// isAncestorOfAny reports whether commit is an ancestor of one of the remote commits. A git error counts as no.
// One `git rev-list` call covers tipsPerCall tips: it prints the commit when no tip in the call reaches it.
// --ignore-missing skips a remote tip that the clone never fetched, so such a tip cannot hide a banked commit.
func isAncestorOfAny(ctx context.Context, git proc.Runner, repo, commit string, remote []string) bool {
	for start := 0; start < len(remote); start += tipsPerCall {
		args := append([]string{"-C", repo, "rev-list", "--ignore-missing", "-n1", commit, "--not"}, remote[start:min(start+tipsPerCall, len(remote))]...)

		if result := git.Run(ctx, args); result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "" {
			return true
		}
	}

	return false
}

func lines(text string) []string {
	var found []string

	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			found = append(found, line)
		}
	}

	return found
}
