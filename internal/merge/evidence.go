package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/google/renameio/v2"

	"github.com/tobocop2/obee/internal/model"
)

const (
	logsFolder = "check-logs"
	logsMode   = 0o700
	logMode    = 0o600
	indent     = "        "

	// maxLogReads is the number of check logs that one poll reads. A check that the cap leaves out is read by the next poll.
	maxLogReads = 5
)

// errEmptyLog is the reason for a log that is not saved because the forge returned no text. No file is made, so the next poll reads the log again.
var errEmptyLog = errors.New("the log is empty")

// Trees is what the merge reads to compare trees. A forge that also implements Trees lets the merge prove the base tip after the merge.
// CommitTree returns the tree id of a commit. TreeBlobs returns the files of a tree, path to content id; a list cut short is an error.
type Trees interface {
	CommitTree(ctx context.Context, commit string) (string, error)
	TreeBlobs(ctx context.Context, tree string) (map[string]string, error)
}

// Logs is what the merge reads to save the log of a failed check. FailedLog returns the log of the failed steps of the check run at link.
type Logs interface {
	FailedLog(ctx context.Context, link string) (string, error)
}

// tipTree compares the tree of the base tip after the merge with the tree that was gated. An equal tree passes.
// A different tree stops the run and lists the paths that differ. A request with no gated tree asks for no proof.
// The merge already happened, so a stop here leaves the lines of the merge in front of it.
func (a *attempt) tipTree(tip string) *refusal {
	if a.req.GatedTree == "" {
		return nil
	}

	trees, ok := a.forge.(Trees)
	if !ok || tip == "" {
		return a.noTrees(ok)
	}

	tree, err := trees.CommitTree(a.ctx, tip)
	if err != nil {
		return stopped(fmt.Sprintf("cannot read the tree of %s tip %s: %s", a.req.Base, tip, err))
	}

	return a.compare(trees, tip, tree)
}

// compare passes a tip that holds the gated tree and stops on any other.
func (a *attempt) compare(trees Trees, tip, tree string) *refusal {
	if tree != a.req.GatedTree {
		return a.mismatch(trees, tip, tree)
	}

	a.say(fmt.Sprintf("tree: %s tip %s holds the gated tree %s", a.req.Base, tip, tree))

	return nil
}

// noTrees is the stop for a tree that cannot be compared: the forge reads no trees (forgeReads is false), or the tip is unknown.
func (a *attempt) noTrees(forgeReads bool) *refusal {
	if !forgeReads {
		return stopped("this forge cannot read trees, so the tree of " + a.req.Base + " cannot be compared with the gated tree")
	}

	return stopped("cannot read the tip of " + a.req.Base + " after the merge, so its tree cannot be compared with the gated tree")
}

// mismatch is the stop for a base tip whose tree is not the gated tree. It lists the paths that differ.
func (a *attempt) mismatch(trees Trees, tip, tree string) *refusal {
	head := fmt.Sprintf("%s tip %s holds tree %s, and the gated tree is %s.", a.req.Base, tip, tree, a.req.GatedTree)

	paths, err := differing(a.ctx, trees, a.req.GatedTree, tree)
	if err != nil {
		return stopped(head, indent+"The trees differ, and the paths that differ could not be listed: "+err.Error())
	}

	lines := []string{fmt.Sprintf("STOP: %s %d paths differ:", head, len(paths))}
	for _, path := range paths {
		lines = append(lines, indent+path)
	}

	return &refusal{stop: Refused, reason: TreeProof, lines: lines}
}

func stopped(lines ...string) *refusal {
	lines[0] = "STOP: " + lines[0]

	return &refusal{stop: Refused, reason: TreeProof, lines: lines}
}

// differing lists the paths whose file differs between two trees, or exists in only one. The list is sorted.
func differing(ctx context.Context, trees Trees, want, got string) ([]string, error) {
	wanted, err := trees.TreeBlobs(ctx, want)
	if err != nil {
		return nil, err
	}

	have, err := trees.TreeBlobs(ctx, got)
	if err != nil {
		return nil, err
	}

	paths := slices.Concat(changedFrom(wanted, have), changedFrom(have, wanted))
	slices.Sort(paths)

	return slices.Compact(paths), nil
}

// changedFrom lists the paths of from that are absent from other, or hold other content there.
func changedFrom(from, other map[string]string) []string {
	var paths []string

	for path, blob := range from {
		if found, there := other[path]; !there || found != blob {
			paths = append(paths, path)
		}
	}

	return paths
}

// saveLogs writes the log of each failed or canceled check under the state folder, in the poll that first sees the check in that state.
// A check whose log is on disk from an earlier poll is not read again. One poll reads at most maxLogReads logs; the output names the checks that the cap leaves for the next poll.
// A log that cannot be read or written is said, and the run still refuses for the failed check.
func (a *attempt) saveLogs(rows []model.Check) {
	failed := slices.DeleteFunc(slices.Clone(rows), func(row model.Check) bool {
		return row.Bucket != model.CheckFail && row.Bucket != model.CheckCancel
	})
	if len(failed) == 0 {
		return
	}

	logs, ok := a.forge.(Logs)
	if !ok || a.req.Dir == "" {
		a.say("check log: not saved, " + a.noLogs())

		return
	}

	a.readLogs(logs, failed)
}

// readLogs saves the log of each check of failed that has none on disk, up to the cap of one poll, and says which checks the cap leaves out.
func (a *attempt) readLogs(logs Logs, failed []model.Check) {
	todo := a.withoutLog(failed)
	reads := min(len(todo), maxLogReads)

	for _, row := range todo[:reads] {
		path := a.logPath(row)
		if err := a.saveLog(logs, row, path); err != nil {
			a.say(fmt.Sprintf("check log: %s not saved: %s", row.Name, err))
		} else {
			a.say(fmt.Sprintf("check log: %s saved to %s", row.Name, path))
		}
	}

	if left := todo[reads:]; len(left) > 0 {
		a.say(fmt.Sprintf("check log: %d of %d failed checks read. The cap is %d logs for each poll, so %s are not read",
			reads, len(todo), maxLogReads, checkNames(left)))
	}
}

// checkNames joins the names of the checks.
func checkNames(rows []model.Check) string {
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}

	return strings.Join(names, ", ")
}

// withoutLog returns the checks whose log is not on disk, and says which logs an earlier poll saved.
func (a *attempt) withoutLog(failed []model.Check) []model.Check {
	var todo []model.Check

	for _, row := range failed {
		if path := a.logPath(row); fileExists(path) {
			a.say(fmt.Sprintf("check log: %s already saved to %s", row.Name, path))
		} else {
			todo = append(todo, row)
		}
	}

	return todo
}

// fileExists is true when path is a regular file: the log of an earlier poll.
func fileExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// noLogs says why a log is not saved: no state folder, or else a forge that reads no logs.
func (a *attempt) noLogs() string {
	if a.req.Dir == "" {
		return "the state folder is not set"
	}

	return "this forge cannot read the log of a check"
}

// logPath is the file of the log of one check: it is named for the pull request, the job and the check. The job number keeps two checks of one name apart.
func (a *attempt) logPath(row model.Check) string {
	name := "pr" + strconv.Itoa(a.req.PR) + "-" + model.SafeName(row.Link[strings.LastIndex(row.Link, "/")+1:]) + "-" + model.SafeName(row.Name) + ".log"

	return filepath.Join(a.req.Dir, logsFolder, name)
}

// saveLog reads the log of one check and writes it to path.
func (a *attempt) saveLog(logs Logs, row model.Check, path string) error {
	text, err := logs.FailedLog(a.ctx, row.Link)
	if err != nil {
		return err
	}

	if text == "" {
		return errEmptyLog
	}

	if err := os.MkdirAll(filepath.Dir(path), logsMode); err != nil {
		return err
	}

	return writeWhole(path, text)
}

// writeWhole writes text to a temporary file in the folder of path and renames it to path, so a file at path is never partial.
func writeWhole(path, text string) error {
	return renameio.WriteFile(path, []byte(text), logMode, renameio.WithTempDir(filepath.Dir(path)))
}
