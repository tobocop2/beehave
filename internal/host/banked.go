package host

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

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

// DefaultRepoBudget is the time that the scan gives to one repository when no option sets another.
const DefaultRepoBudget = 20 * time.Second

// unreadableRef names the entry of a repository that the scan could not read.
const unreadableRef = "(unreadable)"

// Unbanked is one commit on the host that no remote with a host part holds.
// An entry with a Reason and no Commit stands for a repository that the scan could not read: it may hold work, so it counts as unbanked.
type Unbanked struct {
	Repo   string
	Ref    string
	Commit string
	Reason string
}

// ScanOption sets how the bank scan runs.
type ScanOption func(*scanSettings)

type scanSettings struct {
	budget time.Duration
}

// WithRepoBudget sets the time that the scan gives to each repository, shared by all its git calls. A value of zero or less keeps DefaultRepoBudget.
func WithRepoBudget(budget time.Duration) ScanOption {
	return func(s *scanSettings) {
		if budget > 0 {
			s.budget = budget
		}
	}
}

func newScan(opts []ScanOption) scanSettings {
	s := scanSettings{budget: DefaultRepoBudget}
	for _, opt := range opts {
		opt(&s)
	}

	return s
}

// Banked lists the work in repos that is not on a remote. Every branch tip, tag, stash entry and worktree HEAD must be an
// ancestor of a head or a tag that `git ls-remote` reports on a remote with a host part, or on one of the extra URLs.
// A remote that cannot be read holds nothing. A repository whose git call fails, or whose time budget ends, is listed as one unbanked entry, and the scan goes on.
// The error is the error of ctx: a canceled scan has no answer.
func Banked(ctx context.Context, git proc.Runner, repos []string, extra ...string) ([]Unbanked, error) {
	return banked(ctx, git, DefaultRepoBudget, repos, extra)
}

func banked(ctx context.Context, git proc.Runner, budget time.Duration, repos, extra []string) ([]Unbanked, error) {
	var missing []Unbanked

	for _, repo := range repos {
		missing = append(missing, scanRepo(ctx, git, repo, extra, budget)...)

		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	return missing, nil
}

// scanRepo reads one repository inside its own budget. A failed call or an ended budget leaves one unreadable entry in place of the repository.
func scanRepo(ctx context.Context, git proc.Runner, repo string, extra []string, budget time.Duration) []Unbanked {
	repoCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	items, err := unbankedIn(repoCtx, git, repo, extra)

	switch {
	case repoCtx.Err() != nil:
		err = fmt.Errorf("the time budget of %s ended", budget)
	case err == nil:
		return items
	}

	return []Unbanked{{Repo: repo, Ref: unreadableRef, Reason: err.Error()}}
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
//
// A repository that cannot be read, or whose time budget ends, is never pushed: it stays unbanked, so Bank refuses. The repositories after it are still banked.
// The check after the pushes reads only the repositories that were pushed from, so a hung repository costs its budget once.
// Each repository also gets one budget for all its pushes: a hung push ends the call as a refusal.
// The budget ends the local wait only. A git process that runs on a remote host through ssh may go on after the wait ends.
func Bank(ctx context.Context, git proc.Runner, repos []string, private string, opts ...ScanOption) error {
	scan := newScan(opts)

	missing, err := banked(ctx, git, scan.budget, repos, nil)
	if err != nil || len(missing) == 0 {
		return err
	}

	if !model.RemoteHasHost(private) {
		return fmt.Errorf("%w: %s. Set `host.private_remote` to a remote with a host part, or push the work", ErrUnbanked, describe(missing))
	}

	if missing, err = pushAndCheck(ctx, git, scan.budget, missing, private); err != nil || len(missing) == 0 {
		return err
	}

	return fmt.Errorf("%w: %s", ErrUnbanked, describe(missing))
}

// pushAndCheck pushes the commits of the readable repositories, reads those repositories again, and returns what is still missing, with the unreadable entries.
func pushAndCheck(ctx context.Context, git proc.Runner, budget time.Duration, missing []Unbanked, private string) ([]Unbanked, error) {
	readable, unreadable := splitUnreadable(missing)

	if err := push(ctx, git, budget, readable, private); err != nil {
		return nil, err
	}

	again, err := banked(ctx, git, budget, repoNames(readable), []string{private})

	return slices.Concat(unreadable, again), err
}

// splitUnreadable separates the commits of the readable repositories from the entries of the repositories that could not be read.
func splitUnreadable(missing []Unbanked) (readable, unreadable []Unbanked) {
	for _, item := range missing {
		if item.Reason != "" {
			unreadable = append(unreadable, item)
		} else {
			readable = append(readable, item)
		}
	}

	return readable, unreadable
}

// repoNames lists the repositories of the items once each, in order.
func repoNames(items []Unbanked) []string {
	var names []string

	for _, item := range items {
		if !slices.Contains(names, item.Repo) {
			names = append(names, item.Repo)
		}
	}

	return names
}

// push sends each commit to a branch of the private remote that no work uses. Each repository gets one time budget for all its pushes.
func push(ctx context.Context, git proc.Runner, budget time.Duration, missing []Unbanked, private string) error {
	for _, repo := range repoNames(missing) {
		mine := slices.DeleteFunc(slices.Clone(missing), func(item Unbanked) bool { return item.Repo != repo })

		if err := pushRepo(ctx, git, budget, repo, mine, private); err != nil {
			return err
		}
	}

	return nil
}

func pushRepo(ctx context.Context, git proc.Runner, budget time.Duration, repo string, missing []Unbanked, private string) error {
	repoCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	for _, item := range missing {
		spec := item.Commit + ":" + bankedRefSpace + item.Commit[:min(shortCommit, len(item.Commit))]

		if result := git.Run(repoCtx, []string{"-C", repo, "push", "--", private, spec}); result.ExitCode != 0 {
			return fmt.Errorf("%w: push of %s from %s failed: %s", ErrUnbanked, item.Ref, repo, pushFailure(ctx, repoCtx, budget, result))
		}
	}

	return nil
}

// pushFailure says why a push failed: the budget of the repository ended, or git reported the error.
func pushFailure(ctx, repoCtx context.Context, budget time.Duration, result model.CommandResult) string {
	if ctx.Err() == nil && repoCtx.Err() != nil {
		return fmt.Sprintf("the time budget of %s ended", budget)
	}

	return strings.TrimSpace(result.Stderr)
}

func describe(missing []Unbanked) string {
	parts := make([]string, len(missing))
	for i, item := range missing {
		parts[i] = item.Repo + " " + item.Ref

		if item.Reason != "" {
			parts[i] += ": " + item.Reason
		}
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
