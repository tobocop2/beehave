package forge

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const (
	bigPullRequest = 100
	openListLimit  = 1000
	week           = 7 * 24 * time.Hour
	sinceLayout    = "2006-01-02T15:04:05Z"
	historyFilter  = `.[] | "\(.sha[0:9]) \(.commit.message | split("\n")[0])"`
)

// Overlap looks for open pull requests and new base commits that touch the files of a pull request or a file list.
func (f *Forge) Overlap(ctx context.Context, req model.OverlapRequest) (model.Overlap, error) {
	names, since, err := f.target(ctx, req)
	if err != nil {
		return model.Overlap{}, err
	}

	found := model.Overlap{}
	if err := f.openPullRequests(ctx, names, req.PR, &found); err != nil {
		return found, err
	}

	if err := f.baseCommits(ctx, req.Base, names, since, &found); err != nil {
		return found, err
	}

	found.Lines = append(found.Lines, fmt.Sprintf("overlap: %d finding(s) over %d file(s)", found.Found, len(names)))

	return found, nil
}

// target returns the sorted file names to compare and the time to read base commits from.
func (f *Forge) target(ctx context.Context, req model.OverlapRequest) ([]string, string, error) {
	if err := IntegrationAgrees(req.Base, req.Integration); err != nil {
		return nil, "", err
	}

	if req.PR == "" && req.Files == "" {
		return nil, "", refuse("usage: obee check overlap <pr> | --files <list> [--since <ISO date>]")
	}

	if req.PR != "" && !model.IsDigits(req.PR) {
		return nil, "", refuse("the pull request number must be digits, got %q", req.PR)
	}

	names, since, err := f.source(ctx, req)
	if err == nil && len(names) == 0 {
		err = refuse("no files to compare. An empty list overlaps with nothing and proves nothing.")
	}

	return names, since, err
}

func (f *Forge) source(ctx context.Context, req model.OverlapRequest) ([]string, string, error) {
	if req.PR != "" {
		return f.pullRequestFiles(ctx, req)
	}

	info, err := os.Stat(req.Files)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", refuse("no file list at %s", req.Files)
	}

	text, err := os.ReadFile(req.Files)
	if err != nil {
		return nil, "", refuse("no file list at %s", req.Files)
	}

	since := req.Since
	if since == "" {
		since = req.Now.UTC().Add(-week).Format(sinceLayout)
	}

	return uniqueNames(string(text)), since, nil
}

// uniqueNames splits text at newlines only and returns the non-empty lines, sorted and without repeats.
func uniqueNames(text string) []string {
	set := map[string]struct{}{}

	for line := range strings.SplitSeq(text, "\n") {
		if line != "" {
			set[line] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(set))
}

func (f *Forge) pullRequestFiles(ctx context.Context, req model.OverlapRequest) ([]string, string, error) {
	pull := "repos/" + f.repo + "/pulls/" + req.PR

	files, err := f.text(ctx, []string{argAPI, argPaginate, pull + "/files", argJQ, ".[].filename"}, "cannot read the files of #"+req.PR)
	if err != nil {
		return nil, "", err
	}

	if files == "" {
		return nil, "", refuse("no files to compare. #%s has no files, and an empty list proves nothing.", req.PR)
	}

	head, err := f.text(ctx, []string{argAPI, pull, argJQ, ".head.sha"}, "cannot read the head of #"+req.PR)
	if err != nil {
		return nil, "", err
	}

	since, err := f.branchPoint(ctx, req, head)

	return uniqueNames(files), since, err
}

// branchPoint returns the time the head branched from the base.
func (f *Forge) branchPoint(ctx context.Context, req model.OverlapRequest, head string) (string, error) {
	compare := "repos/" + f.repo + "/compare/" + req.Base + "..." + head
	failure := fmt.Sprintf("cannot find where #%s branched from %s", req.PR, req.Base)

	return f.text(ctx, []string{argAPI, compare, argJQ, ".merge_base_commit.commit.committer.date"}, failure)
}

// text returns the output of one gh call without its trailing newlines. A failed call is a refusal with the message.
func (f *Forge) text(ctx context.Context, args []string, failure string) (string, error) {
	done := f.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return "", refuse("%s", failure)
	}

	return strings.TrimRight(done.Stdout, "\n"), nil
}

func (f *Forge) openPullRequests(ctx context.Context, names []string, skip string, found *model.Overlap) error {
	rows, err := f.PRList(ctx, openListLimit, "--state", "open", argJSON, "number,author,title,files")
	if err != nil {
		return &Error{Code: CodeRefused, Message: "cannot list the open pull requests: " + err.Error(), Err: err}
	}

	mine := map[string]struct{}{}
	for _, name := range names {
		mine[name] = struct{}{}
	}

	for _, row := range rows {
		if strconv.Itoa(row.Number) != skip {
			addFinding(found, describeOpen(row, mine))
		}
	}

	return nil
}

// describeOpen returns the line for an open pull request that shares a file or is too big to compare, or an empty string.
func describeOpen(row model.PullRequest, mine map[string]struct{}) string {
	shared := []string{}

	for _, file := range row.Files {
		if _, ok := mine[file.Path]; ok {
			shared = append(shared, file.Path)
		}
	}

	slices.Sort(shared)

	switch {
	case len(shared) > 0:
		return fmt.Sprintf("OPEN PR #%d by %s shares: %s (%s)", row.Number, model.OrNull(row.Author.Login), strings.Join(slices.Compact(shared), ", "), row.Title)
	case len(row.Files) >= bigPullRequest:
		return fmt.Sprintf("UNCHECKED PR #%d by %s has 100 or more files; compare it by hand (%s)", row.Number, model.OrNull(row.Author.Login), row.Title)
	}

	return ""
}

func addFinding(o *model.Overlap, line string) {
	if line != "" {
		o.Lines = append(o.Lines, line)
		o.Found++
	}
}

func (f *Forge) baseCommits(ctx context.Context, base string, names []string, since string, found *model.Overlap) error {
	for _, name := range names {
		hits, err := f.text(ctx, f.historyArgs(base, name, since), fmt.Sprintf("cannot read the history of %s on %s", name, base))
		if err != nil {
			return err
		}

		if hits == "" {
			continue
		}

		for hit := range strings.SplitSeq(hits, "\n") {
			found.Lines = append(found.Lines, fmt.Sprintf("ON %s since %s, %s: %s", base, since, name, hit))
		}

		found.Found++
	}

	return nil
}

func (f *Forge) historyArgs(base, name, since string) []string {
	return []string{
		argAPI, "-X", "GET", "repos/" + f.repo + "/commits",
		flagRaw, "sha=" + base, flagRaw, "path=" + name, flagRaw, "since=" + since, argJQ, historyFilter,
	}
}
