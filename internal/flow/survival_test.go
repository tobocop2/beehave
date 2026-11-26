package flow

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// survivalAdd is one line that a merge of the generator added to a file.
type survivalAdd struct{ subject, file, line string }

// survivalGen builds merges in a real repository and keeps the ledger of what it later removed: the lines that a later commit dropped and the files that it deleted.
// The ledger is the oracle of the survival tests. It never reads the output of the library.
type survivalGen struct {
	rig     *batchRig
	base    string
	order   []string
	adds    []survivalAdd
	lost    map[string]map[string][]string // subject -> file -> lines that a later commit removed
	deleted map[string][]string            // subject -> files that a later commit deleted
}

// newSurvivalGen makes a repository with the base commit and runs the git commands of the library in it.
func newSurvivalGen(t *testing.T) *survivalGen {
	t.Helper()

	return survivalOver(t, libRig(t))
}

// survivalOver makes the generator write into the repository of the rig, from the commit that main holds now.
func survivalOver(t *testing.T, rig *batchRig) *survivalGen {
	t.Helper()

	return &survivalGen{
		rig: rig, base: rig.repo.must(t, "rev-parse", "main"),
		lost: map[string]map[string][]string{}, deleted: map[string][]string{},
	}
}

// read returns the lines of the file on main, or none when the file is absent.
func (g *survivalGen) read(t *testing.T, file string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(g.rig.repo.dir, file))
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// write sets the file on the checked out branch to the lines and stages it.
func (g *survivalGen) write(t *testing.T, file string, lines []string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(g.rig.repo.dir, file), []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	g.rig.repo.must(t, "add", file)
}

// merge adds the lines to the end of the file on a branch off main and merges the branch with a merge commit whose subject is given. A blank line stays in the file and in no ledger.
func (g *survivalGen) merge(t *testing.T, subject, file string, add ...string) {
	t.Helper()

	repo := g.rig.repo
	repo.must(t, "switch", "-q", "main")

	branch := fmt.Sprintf("b-%d", len(g.order))
	repo.must(t, "switch", "-q", "-c", branch)
	g.write(t, file, append(g.read(t, file), add...))
	repo.must(t, "commit", "-q", "-m", "work of "+subject)
	repo.must(t, "switch", "-q", "main")
	repo.must(t, "merge", "-q", "--no-ff", "-m", subject, branch)

	g.order = append(g.order, subject)

	for _, line := range add {
		if strings.TrimSpace(line) != "" {
			g.adds = append(g.adds, survivalAdd{subject, file, line})
		}
	}
}

// drop removes each exact line from the file on main in one plain commit, and records each line that is not blank against the merge that added it.
func (g *survivalGen) drop(t *testing.T, file string, lines ...string) {
	t.Helper()

	var kept []string

	for _, line := range g.read(t, file) {
		if !slices.Contains(lines, line) {
			kept = append(kept, line)
		}
	}

	g.write(t, file, kept)
	g.rig.repo.must(t, "commit", "-q", "-m", "drop lines of "+file)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		idx := slices.IndexFunc(g.adds, func(a survivalAdd) bool { return a.file == file && a.line == line })
		require.GreaterOrEqual(t, idx, 0, "the generator never added %q to %s", line, file)

		subject := g.adds[idx].subject
		if g.lost[subject] == nil {
			g.lost[subject] = map[string][]string{}
		}

		g.lost[subject][file] = append(g.lost[subject][file], line)
	}
}

// remove deletes the file on main in one plain commit and records it against each merge that added lines to it.
func (g *survivalGen) remove(t *testing.T, file string) {
	t.Helper()

	g.rig.repo.must(t, "rm", "-q", file)
	g.rig.repo.must(t, "commit", "-q", "-m", "remove "+file)

	for _, add := range g.adds {
		if add.file == file && !slices.Contains(g.deleted[add.subject], file) {
			g.deleted[add.subject] = append(g.deleted[add.subject], file)
		}
	}
}

// expected is the report line that the ledger gives: one entry for each merge in the order of the merges, as subject=file:count:lines/deleted.
func (g *survivalGen) expected() string {
	entries := []string{fmt.Sprint(len(g.order))}

	for _, subject := range g.order {
		var lost []string

		for _, file := range slices.Sorted(maps.Keys(g.lost[subject])) {
			lines := g.lost[subject][file]
			lost = append(lost, fmt.Sprintf("%s:%d:%s", file, len(lines), strings.Join(lines, ";")))
		}

		entries = append(entries, subject+"="+strings.Join(lost, ",")+"/"+strings.Join(g.deleted[subject], ","))
	}

	return "report " + strings.Join(entries, " ")
}

// survey runs merge_survival over base..tip and returns the one report line of the call: the count of the merges, then one entry for each merge.
func (g *survivalGen) survey(t *testing.T, base, tip string) string {
	t.Helper()

	text := `load("@beehave//survival.star", "merge_survival")

def main(item):
    out = merge_survival(name = "s", base = "` + base + `", tip = "` + tip + `")
    command(name = "report", run = ["report", str(len(out))] + [e.subject + "=" + ",".join([l.file + ":" + str(l.count) + ":" + ";".join(l.lines) for l in e.lost]) + "/" + ",".join(e.deleted) for e in out])
`
	require.NoError(t, g.rig.libWalk(t, text))

	return g.rig.libReport(t)
}

// tip is the head of main.
func (g *survivalGen) tip(t *testing.T) string { return g.rig.repo.must(t, "rev-parse", "main") }

func TestSurvival_ALineThatALaterCommitRemovedIsListedWithItsFileAndMerge(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-one", "a.txt", "a1", "", "a2", "a3")
	g.merge(t, "land-two", "a.txt", "a4", "a5")
	g.merge(t, "land-three", "b.txt", "b1", "b2")
	g.drop(t, "a.txt", "a2", "a5", "")
	g.drop(t, "b.txt", "b1", "b2")

	got := g.survey(t, g.base, g.tip(t))

	assert.Equal(t, g.expected(), got)
	assert.Equal(t, "report 3 land-one=a.txt:1:a2/ land-two=a.txt:1:a5/ land-three=b.txt:2:b1;b2/", got, "a hand-written copy of the ledger: the blank line of land-one that the later commit removed is no lost line")
}

func TestSurvival_ALineThatALaterCommitChangedIsListedAsLost(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-one", "c.txt", "c1", "c2")
	g.write(t, "c.txt", []string{"c1", "c2 changed"})
	g.rig.repo.must(t, "commit", "-q", "-m", "change c2")
	g.lost["land-one"] = map[string][]string{"c.txt": {"c2"}}

	assert.Equal(t, g.expected(), g.survey(t, g.base, g.tip(t)))
}

func TestSurvival_EveryMergeOfTheRangeIsReadAndNotOnlyThePullRequestMerges(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "Merge pull request #7 from o/seven", "p.txt", "p1", "p2")
	g.merge(t, "land the friday batch", "q.txt", "q1", "q2")
	g.drop(t, "p.txt", "p1")
	g.drop(t, "q.txt", "q2")

	got := g.survey(t, g.base, g.tip(t))

	assert.Equal(t, g.expected(), got)
	assert.Contains(t, got, "land the friday batch=q.txt:1:q2/", "a merge whose subject is no pull request merge is listed")
	assert.Contains(t, got, "Merge pull request #7 from o/seven=p.txt:1:p1/")
}

func TestSurvival_AMergeWhoseLinesAllSurviveIsListedWithNothingLost(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-one", "a.txt", "a1", "a2")
	g.merge(t, "land-two", "b.txt", "b1")
	g.merge(t, "land-three", "c.txt", "c1", "c2")
	g.drop(t, "c.txt", "c2")

	got := g.survey(t, g.base, g.tip(t))

	assert.Equal(t, g.expected(), got)
	assert.Contains(t, got, "land-one=/ land-two=/ land-three=c.txt:1:c2/", "the two merges that lost nothing are listed with empty lists")
}

func TestSurvival_AFileDeletedAtTheTipIsListedApartFromAGoneLine(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-gone", "d.txt", "d1", "d2")
	g.merge(t, "land-kept", "k.txt", "k1", "k2")
	g.drop(t, "k.txt", "k1")
	g.remove(t, "d.txt")

	got := g.survey(t, g.base, g.tip(t))

	assert.Equal(t, g.expected(), got)
	assert.Equal(t, "report 2 land-gone=/d.txt land-kept=k.txt:1:k1/", got, "the retired file is in deleted only, and the kept file lists its gone line")
}

func TestSurvival_AnEmptyRangeGivesAnEmptyList(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-one", "a.txt", "a1")
	g.drop(t, "a.txt", "a1")

	assert.Equal(t, "report 0", g.survey(t, g.tip(t), g.tip(t)))
}

func TestSurvival_ARangeThatStartsAfterAMergeDoesNotReadIt(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.merge(t, "land-before", "a.txt", "a1")
	g.drop(t, "a.txt", "a1")
	from := g.tip(t)
	g.merge(t, "land-inside", "b.txt", "b1")
	g.drop(t, "b.txt", "b1")

	got := g.survey(t, from, g.tip(t))

	assert.Equal(t, "report 1 land-inside=b.txt:1:b1/", got)
}

func TestSurvival_ThePlainCommitsOfTheRangeAreNotRead(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	g.write(t, "plain.txt", []string{"x1"})
	g.rig.repo.must(t, "commit", "-q", "-m", "a plain commit that adds a line")
	g.remove(t, "plain.txt")

	assert.Equal(t, "report 0", g.survey(t, g.base, g.tip(t)), "only a merge is read, and this range holds none")
}

func TestSurvival_TheLinesOfAFileAreCappedAndTheCountStaysWhole(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)

	var lines []string

	for i := range 12 {
		lines = append(lines, fmt.Sprintf("many%02d", i))
	}

	g.merge(t, "land-many", "m.txt", lines...)
	g.drop(t, "m.txt", lines...)

	got := g.survey(t, g.base, g.tip(t))

	assert.Equal(t, "report 1 land-many=m.txt:12:"+strings.Join(lines[:10], ";")+"/", got)
}

func TestSurvival_ACallWithAnEmptyNameBaseOrTipIsRefused(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`name = "", base = "a", tip = "b"`, `name = "s", base = "", tip = "b"`, `name = "s", base = "a", tip = ""`} {
		g := newSurvivalGen(t)
		err := g.rig.libWalk(t, `load("@beehave//survival.star", "merge_survival")

def main(item):
    merge_survival(`+call+`)
`)

		require.Error(t, err, call)
		assert.Contains(t, err.Error(), "merge_survival needs a `name`, a `base` and a `tip` that are not empty", call)
	}
}

func TestSurvival_ABaseThatGitDoesNotKnowEndsTheCallWithTheOutputOfGit(t *testing.T) {
	t.Parallel()

	g := newSurvivalGen(t)
	err := g.rig.libWalk(t, `load("@beehave//survival.star", "merge_survival")

def main(item):
    merge_survival(name = "s", base = "no-such-ref", tip = "main")
`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "survival s: git rev-list failed with exit code")
	assert.Contains(t, err.Error(), "no-such-ref")
}
