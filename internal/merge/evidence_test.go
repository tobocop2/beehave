package merge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	gatedTree = "1111111111111111111111111111111111111111"
	otherTree = "2222222222222222222222222222222222222222"
)

// evidenceWorld is the fake forge that also reads trees and the logs of failed checks.
type evidenceWorld struct {
	*world
	// treeOf is the tree of each commit. blobs are the files of each tree: path to blob id.
	treeOf map[string]string
	blobs  map[string]map[string]string
	// logs is the log of each failed check, by link. logCalls lists the links that were read, and logFails the links that fail.
	logs     map[string]string
	logCalls []string
	logFails map[string]bool
	// listFail is the tree whose file list fails to read.
	listFail string
}

func newEvidence(t *testing.T) *evidenceWorld {
	t.Helper()

	return &evidenceWorld{
		world: newWorld(t), treeOf: map[string]string{tipSHA: gatedTree}, blobs: map[string]map[string]string{},
		logs: map[string]string{}, logFails: map[string]bool{},
	}
}

func (e *evidenceWorld) CommitTree(_ context.Context, commit string) (string, error) {
	e.calls = append(e.calls, "CommitTree "+commit)

	tree, found := e.treeOf[commit]
	if !found || e.fails["CommitTree"] {
		return "", fmt.Errorf("CommitTree: %w", errDown)
	}

	return tree, nil
}

func (e *evidenceWorld) TreeBlobs(_ context.Context, tree string) (map[string]string, error) {
	e.calls = append(e.calls, "TreeBlobs "+tree)

	if e.listFail == tree {
		return nil, fmt.Errorf("TreeBlobs: %w", errDown)
	}

	return e.blobs[tree], nil
}

func (e *evidenceWorld) FailedLog(_ context.Context, link string) (string, error) {
	e.logCalls = append(e.logCalls, link)

	if e.logFails[link] {
		return "", fmt.Errorf("FailedLog: %w", errDown)
	}

	if text, set := e.logs[link]; set {
		return text, nil
	}

	return "log of " + link + "\n", nil
}

func (e *evidenceWorld) runWith(req merge.Request) merge.Result {
	return merge.Run(context.Background(), e, req)
}

func TestTree_ABaseTipWithTheGatedTreePasses(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	req := request()
	req.GatedTree = gatedTree

	result := e.runWith(req)

	assert.Equal(t, merge.Done, result.Stop, all(result))
	assert.Contains(t, result.Lines, "tree: main tip "+tipSHA+" holds the gated tree "+gatedTree)
}

func TestTree_ADifferentTreeStopsTheRunAndListsTheDifferingPaths(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	e.treeOf[tipSHA] = otherTree
	e.blobs[gatedTree] = map[string]string{"same.go": "a", "changed.go": "b", "gone.go": "c"}
	e.blobs[otherTree] = map[string]string{"same.go": "a", "changed.go": "z", "extra.go": "d"}
	req := request()
	req.GatedTree = gatedTree

	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, e.writes, "the merge happened before the check, so the lines still say it")
	assert.Contains(t, all(result), "MERGED #5 at "+sha)
	assert.Equal(t, "STOP: main tip "+tipSHA+" holds tree "+otherTree+", and the gated tree is "+gatedTree+". 3 paths differ:", result.Lines[len(result.Lines)-4])
	assert.Equal(t, []string{"        changed.go", "        extra.go", "        gone.go"}, result.Lines[len(result.Lines)-3:])
}

func TestTree_ADifferentTreeStillStopsWhenThePathsCannotBeListed(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{gatedTree, otherTree} {
		e := newEvidence(t)
		e.treeOf[tipSHA] = otherTree
		e.listFail = failing
		req := request()
		req.GatedTree = gatedTree

		result := e.runWith(req)

		assert.Equal(t, merge.Refused, result.Stop)
		assert.Contains(t, last(result), "the paths that differ could not be listed")
	}
}

func TestTree_ATipWhoseTreeCannotBeReadStops(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	e.fails["CommitTree"] = true
	req := request()
	req.GatedTree = gatedTree

	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "STOP: cannot read the tree of main tip "+tipSHA)
}

func TestTree_ATipThatCannotBeReadAfterTheMergeStops(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	e.tipReads = []tipRead{{sha: tipSHA, found: true}, {sha: tipSHA, found: true}, {found: false}}
	req := request()
	req.GatedTree = gatedTree

	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "STOP: cannot read the tip of main after the merge")
}

func TestTree_AForgeThatReadsNoTreesStopsWhenATreeIsExpected(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req := request()
	req.GatedTree = gatedTree

	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "STOP: this forge cannot read trees")
}

func TestTree_NoGatedTreeMeansNoTreeRead(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	result := e.runWith(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.False(t, e.called("CommitTree"))
}

func TestTree_TheMergeStillPinsTheHeadItWasGivenAfterARewrite(t *testing.T) {
	t.Parallel()

	rewritten := "dddddddddddddddddddddddddddddddddddddddd"
	e := newEvidence(t)
	e.set(t, map[string]any{"headRefOid": rewritten})
	req := request()
	req.Gated = rewritten
	req.GatedTree = gatedTree

	result := e.runWith(req)

	assert.Equal(t, merge.Done, result.Stop, all(result))
	assert.Equal(t, []string{"MergePR 5 " + rewritten}, e.writes)
}

func failedWorld(t *testing.T, dir string) (*evidenceWorld, merge.Request) {
	t.Helper()

	e := newEvidence(t)
	e.checks = []model.Check{
		{Name: "lint", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/11"},
		{Name: "unit/race", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/12"},
		{Name: "docs", Bucket: model.CheckBucket("cancel"), Link: "https://github.com/o/r/actions/runs/1/job/13"},
		{Name: "build", Bucket: model.CheckPass, Link: "https://github.com/o/r/actions/runs/1/job/14"},
	}
	req := request()
	req.Dir = dir

	return e, req
}

// saved returns the text of a log file under the check-logs folder of dir.
func saved(t *testing.T, dir, name string) string {
	t.Helper()

	text, err := os.ReadFile(filepath.Join(dir, "check-logs", name)) //nolint:gosec // the test reads a file it just made in its own temp folder
	require.NoError(t, err)

	return string(text)
}

func TestFailedLogs_EachFailedCheckLogIsWrittenBeforeTheRunRefuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.logs["https://github.com/o/r/actions/runs/1/job/11"] = "lint says no\n"
	e.logs["https://github.com/o/r/actions/runs/1/job/12"] = "race at line 4\n"
	e.logs["https://github.com/o/r/actions/runs/1/job/13"] = "canceled\n"

	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, e.writes, "a refused run writes nothing to the forge")
	assert.Equal(t, []string{
		"https://github.com/o/r/actions/runs/1/job/11", "https://github.com/o/r/actions/runs/1/job/12", "https://github.com/o/r/actions/runs/1/job/13",
	}, e.logCalls, "a check that failed or was canceled has a log; a check that passed has none")

	assert.Equal(t, "lint says no\n", saved(t, dir, "pr5-11-lint.log"))
	assert.Equal(t, "race at line 4\n", saved(t, dir, "pr5-12-unit_2Frace.log"))
	assert.Equal(t, "canceled\n", saved(t, dir, "pr5-13-docs.log"))

	assert.Contains(t, result.Lines, "check log: lint saved to "+filepath.Join(dir, "check-logs", "pr5-11-lint.log"))
}

func TestFailedLogs_ALogThatAnEarlierPollSavedIsNotReadAgain(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.logs["https://github.com/o/r/actions/runs/1/job/11"] = "first poll\n"

	e.runWith(req)

	e.logs["https://github.com/o/r/actions/runs/1/job/11"] = "second poll\n"
	e.logCalls = nil
	result := e.runWith(req)

	assert.Empty(t, e.logCalls, "the checks failed in the first poll, so their logs are on disk")
	assert.Equal(t, "first poll\n", saved(t, dir, "pr5-11-lint.log"))
	assert.Contains(t, all(result), "check log: lint already saved to "+filepath.Join(dir, "check-logs", "pr5-11-lint.log"))
}

func TestFailedLogs_AtMostFiveLogsAreReadInOnePollAndTheOutputSaysSo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.checks = nil

	for i := range 7 {
		link := fmt.Sprintf("https://github.com/o/r/actions/runs/1/job/%d", 30+i)
		e.checks = append(e.checks, model.Check{Name: fmt.Sprintf("c%d", i), Bucket: model.CheckFail, Link: link})
	}

	result := e.runWith(req)

	assert.Len(t, e.logCalls, 5)
	assert.Contains(t, all(result), "check log: 5 of 7 failed checks read. The cap is 5 logs for each poll, so c5, c6 are not read")

	e.logCalls = nil
	result = e.runWith(req)

	assert.Len(t, e.logCalls, 2, "the next poll reads the checks that the cap left out")
	assert.Contains(t, all(result), "check log: c5 saved to ")
}

func TestFailedLogs_TwoChecksWithOneNameKeepTheirOwnLogs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.checks = []model.Check{
		{Name: "test", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/21"},
		{Name: "test", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/22"},
	}
	e.logs["https://github.com/o/r/actions/runs/1/job/21"] = "first\n"
	e.logs["https://github.com/o/r/actions/runs/1/job/22"] = "second\n"

	e.runWith(req)

	assert.Equal(t, "first\n", saved(t, dir, "pr5-21-test.log"))
	assert.Equal(t, "second\n", saved(t, dir, "pr5-22-test.log"))
}

func TestFailedLogs_TwoCheckNamesThatDifferOnlyInASeparatorKeepTwoLogFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.checks = []model.Check{
		{Name: "a/b", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/31"},
		{Name: "a_b", Bucket: model.CheckFail, Link: "https://github.com/o/r/actions/runs/1/job/31"},
	}
	e.logs["https://github.com/o/r/actions/runs/1/job/31"] = "same job\n"

	e.runWith(req)

	assert.Equal(t, "same job\n", saved(t, dir, "pr5-31-a_2Fb.log"))
	assert.Equal(t, "same job\n", saved(t, dir, "pr5-31-a_5Fb.log"))
}

func TestFailedLogs_AnEmptyLogIsNotWrittenAndTheNextPollReadsItAgain(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	link := "https://github.com/o/r/actions/runs/1/job/11"
	e.logs[link] = ""

	result := e.runWith(req)

	assert.Contains(t, all(result), "check log: lint not saved: the log is empty")
	assert.NoFileExists(t, filepath.Join(dir, "check-logs", "pr5-11-lint.log"))

	e.logs[link] = "now there is text\n"
	e.logCalls = nil
	e.runWith(req)

	assert.Contains(t, e.logCalls, link, "an empty log leaves no file, so the next poll reads it")
	assert.Equal(t, "now there is text\n", saved(t, dir, "pr5-11-lint.log"))
}

func TestFailedLogs_AWrittenLogLeavesNoTemporaryFileInTheFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.logs["https://github.com/o/r/actions/runs/1/job/11"] = "text\n"

	e.runWith(req)

	names, err := filepath.Glob(filepath.Join(dir, "check-logs", "*"))
	require.NoError(t, err)

	for _, name := range names {
		assert.Equal(t, ".log", filepath.Ext(name), "only finished logs are in the folder")
	}
}

func TestFailedLogs_ALogThatCannotBeRenamedIntoPlaceLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "check-logs", "pr5-11-lint.log"), 0o700))

	e, req := failedWorld(t, dir)
	result := e.runWith(req)

	assert.Contains(t, all(result), "check log: lint not saved: ")

	names, err := filepath.Glob(filepath.Join(dir, "check-logs", ".tmp-*"))
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestFailedLogs_AFolderThatCannotTakeANewFileSaysTheLogIsNotSaved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "check-logs"), 0o500))

	e, req := failedWorld(t, dir)
	result := e.runWith(req)

	assert.Contains(t, all(result), "check log: lint not saved: ")
}

func TestFailedLogs_ALogThatCannotBeReadIsSaidAndTheRunStillRefuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e, req := failedWorld(t, dir)
	e.logFails["https://github.com/o/r/actions/runs/1/job/11"] = true

	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, all(result), "check log: lint not saved: FailedLog: down")
	assert.Contains(t, all(result), "check log: unit/race saved to ")
	assert.Contains(t, all(result), "REFUSE: checks are not green: lint=fail,unit/race=fail,docs=cancel")
}

func TestFailedLogs_ALogThatCannotBeWrittenIsSaid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "check-logs"), []byte("a file, not a folder"), 0o600))

	e, req := failedWorld(t, dir)
	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, all(result), "check log: lint not saved: ")
}

func TestFailedLogs_NoStateFolderMeansTheLogIsNotSavedAndSaysSo(t *testing.T) {
	t.Parallel()

	e, req := failedWorld(t, "")
	result := e.runWith(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, e.logCalls)
	assert.Contains(t, all(result), "check log: not saved, the state folder is not set")
}

func TestFailedLogs_AForgeThatReadsNoLogsSaysSo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.checks = []model.Check{{Name: "lint", Bucket: model.CheckFail}}
	req := request()
	req.Dir = t.TempDir()

	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, all(result), "check log: not saved, this forge cannot read the log of a check")
}

func TestFailedLogs_AGreenRunReadsNoLog(t *testing.T) {
	t.Parallel()

	e := newEvidence(t)
	req := request()
	req.Dir = t.TempDir()

	assert.Equal(t, merge.Done, e.runWith(req).Stop)
	assert.Empty(t, e.logCalls)
}
