package brief_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
	"github.com/tobocop2/obee/internal/ledger"
)

const itemStart = "# Lane `[LANE]`"

func part(t *testing.T, name brief.Part) string {
	t.Helper()

	text, err := brief.Text(name)
	require.NoError(t, err)

	return text
}

func specOf(kind brief.Kind) brief.Spec {
	return brief.Spec{
		Kind:  kind,
		Model: brief.Model{Name: "sonnet"},
		Lane:  brief.Lane{Acceptance: "/s/accept.sh", BaseResult: "/s/base.txt"},
		Rules: "PROJECT-RULES\n",
		Taken: brief.Taken(brief.Blocks()),
	}
}

func emit(t *testing.T, spec brief.Spec) string {
	t.Helper()

	text, err := brief.Emit(spec)
	require.NoError(t, err)

	return text
}

func TestEmit_EveryKindNamesABuildVersusBuySearch(t *testing.T) {
	t.Parallel()

	for _, kind := range brief.Kinds() {
		assert.Contains(t, emit(t, specOf(kind)), "Build-versus-buy search.", kind)
	}
}

func TestEmit_TheReviewerTemplateCitesTheReferenceAndChecksStoredStateAndDeadCode(t *testing.T) {
	t.Parallel()

	review := emit(t, specOf(brief.KindRev))

	for _, line := range []string{"Cite the reference the ruling names.", "Check the state the change stores.", "Check for dead code."} {
		assert.Contains(t, review, line)

		for _, kind := range []brief.Kind{brief.KindImpl, brief.KindDesign, brief.KindPapers} {
			assert.NotContains(t, emit(t, specOf(kind)), line, kind)
		}
	}
}

func TestEmit_EveryKindStartsWithTheSkeletonAndEndsWithTheLaneNames(t *testing.T) {
	t.Parallel()

	for _, kind := range brief.Kinds() {
		text := emit(t, specOf(kind))

		assert.True(t, strings.HasPrefix(text, part(t, brief.PartSkeleton)), kind)
		assert.True(t, strings.HasSuffix(text, part(t, brief.PartLaneNames)), kind)
		assert.Contains(t, text, part(t, brief.PartClosingRules), kind)
		assert.Contains(t, text, part(t, brief.PartProjectRulesHeading)+"PROJECT-RULES\n", kind)
	}
}

func TestEmit_OnlyTheKindsThatWriteGetTheBeforeWritingRules(t *testing.T) {
	t.Parallel()

	before := part(t, brief.PartBeforeWriteRules)

	assert.Contains(t, emit(t, specOf(brief.KindImpl)), before)
	assert.Contains(t, emit(t, specOf(brief.KindDesign)), before)
	assert.Contains(t, emit(t, specOf(brief.KindPapers)), before)
	assert.NotContains(t, emit(t, specOf(brief.KindRev)), before)
}

func TestEmit_EachKindGetsItsOwnRules(t *testing.T) {
	t.Parallel()

	acceptance := "## Acceptance: the script decides, before the code and after it\n\n" + part(t, brief.PartAcceptanceRules)
	callSite := part(t, brief.PartCallSiteRules)
	review := part(t, brief.PartReviewRules)

	impl, rev := emit(t, specOf(brief.KindImpl)), emit(t, specOf(brief.KindRev))
	design, papers := emit(t, specOf(brief.KindDesign)), emit(t, specOf(brief.KindPapers))

	assert.Contains(t, impl, acceptance)
	assert.Contains(t, impl, callSite)
	assert.NotContains(t, impl, review)
	assert.Contains(t, rev, review)
	assert.NotContains(t, rev, acceptance)
	assert.NotContains(t, rev, callSite)
	assert.Contains(t, design, part(t, brief.PartDesignRules))
	assert.NotContains(t, design, acceptance)
	assert.Contains(t, papers, part(t, brief.PartPapersRules))
	assert.NotContains(t, papers, callSite)
}

func TestEmit_OnlyAnImplBriefNamesTheAcceptanceOfTheItem(t *testing.T) {
	t.Parallel()

	impl := emit(t, specOf(brief.KindImpl))

	assert.Contains(t, impl, "## Acceptance of this item\n\n- Acceptance script: `/s/accept.sh`\n- Result at the base: `/s/base.txt`\n")
	assert.Contains(t, impl, "- Expected behavior: `[EXPECTED]`\n")

	for _, kind := range []brief.Kind{brief.KindRev, brief.KindDesign, brief.KindPapers} {
		assert.NotContains(t, emit(t, specOf(kind)), "- Acceptance script:", kind)
	}

	spec := specOf(brief.KindImpl)
	spec.Lane.Expected = "/s/rulings/item.md"

	assert.Contains(t, emit(t, spec), "- Expected behavior: `/s/rulings/item.md`\n")
}

func TestEmit_TheHeaderNamesTheModelAndTheTierRow(t *testing.T) {
	t.Parallel()

	plain := emit(t, specOf(brief.KindImpl))

	assert.Contains(t, plain, itemStart+": [ONE LINE SAYING WHAT AND WHY]\n\n**Dispatch model: `sonnet`.** Orchestrator: pass it as the `model` of the dispatch. A dispatch with no\nmodel inherits yours.\n\n")

	spec := specOf(brief.KindImpl)
	spec.Model = brief.Model{Name: "opus", Tier: brief.TierStuck}

	assert.Contains(t, emit(t, spec), "**Dispatch model: `opus`.** Tier row: `stuck`. Orchestrator: pass it")
}

func TestEmit_TheBaseBranchLineNamesTheBranchOfTheItem(t *testing.T) {
	t.Parallel()

	spec := specOf(brief.KindImpl)
	spec.Lane.Branch = "integration/x"

	text := emit(t, spec)

	assert.Contains(t, text, "**Base branch of your pull request: `integration/x`.** Branch from it and open the pull request against it, never against the default branch.\n\n## Acceptance of this item")
	assert.NotContains(t, emit(t, specOf(brief.KindImpl)), "Base branch of your pull request")

	spec.Kind = brief.KindDesign

	assert.NotContains(t, emit(t, spec), "Base branch of your pull request")
}

func TestEmit_TheReadFirstListGetsOneLineForEachPathAndABlankLine(t *testing.T) {
	t.Parallel()

	spec := specOf(brief.KindRev)
	spec.ReadFirst = []string{"/a/one.md", "", "/a/two.md"}

	text := emit(t, spec)

	assert.Contains(t, text, part(t, brief.PartItemSkeleton)+"- `/a/one.md`\n- `/a/two.md`\n\n"+part(t, brief.PartLaneNames))

	spec.ReadFirst = nil
	spec.Lane.ReadFirst = "/a/empty.txt"

	assert.Contains(t, emit(t, spec), part(t, brief.PartItemSkeleton)+"\n"+part(t, brief.PartLaneNames))
	assert.Contains(t, emit(t, specOf(brief.KindRev)), part(t, brief.PartItemSkeleton)+part(t, brief.PartLaneNames))
}

func TestEmit_EveryBriefCarriesTheContextCapText(t *testing.T) {
	t.Parallel()

	for _, kind := range brief.Kinds() {
		text := emit(t, specOf(kind))

		assert.Contains(t, text, "## Your context cap: a trigger, not a hard stop", kind)
		assert.Contains(t, text, "Past 150000 tokens, finish the step you are in,", kind)
	}

	spec := specOf(brief.KindImpl)
	spec.Lane.Cap = 90000

	assert.Contains(t, emit(t, spec), "Past 90000 tokens, finish the step you are in,")
}

func TestEmit_TheStandingBlocksFollowTheProfileAndTheKind(t *testing.T) {
	t.Parallel()

	host, compiled, union := part(t, brief.PartBuildHostRules), part(t, brief.PartCompiledRules), part(t, brief.PartUnionRules)

	all := emit(t, specOf(brief.KindImpl))

	assert.Contains(t, all, host)
	assert.Contains(t, all, compiled)
	assert.NotContains(t, all, union)

	spec := specOf(brief.KindImpl)
	spec.Lane.Union = true

	assert.Contains(t, emit(t, spec), union)

	spec = specOf(brief.KindRev)
	spec.Model = brief.Model{Name: "opus", Tier: brief.TierUnion}

	assert.Contains(t, emit(t, spec), union)

	spec.Taken = brief.Taken{}

	none := emit(t, spec)

	assert.NotContains(t, none, union)
	assert.NotContains(t, none, host)
	assert.NotContains(t, none, compiled)
	assert.Contains(t, none, part(t, brief.PartLongJobsRules))

	assert.NotContains(t, emit(t, specOf(brief.KindDesign)), compiled)
	assert.NotContains(t, emit(t, specOf(brief.KindPapers)), part(t, brief.PartGateStepRules))
	assert.Contains(t, emit(t, specOf(brief.KindRev)), part(t, brief.PartGateStepRules))
}

func TestEmit_BriefsOfOneKindShareEveryByteUpToTheFirstItemLine(t *testing.T) {
	t.Parallel()

	for _, kind := range brief.Kinds() {
		first := specOf(kind)
		second := specOf(kind)
		second.Model = brief.Model{Name: "haiku"}
		second.ReadFirst = []string{"/a/other.md"}
		second.Lane = brief.Lane{Acceptance: "/z/other.sh", BaseResult: "/z/other.txt", Expected: "/z/e.md", Branch: "other"}

		a, b := emit(t, first), emit(t, second)

		cutA, cutB := strings.Index(a, itemStart), strings.Index(b, itemStart)

		require.Positive(t, cutA, kind)
		require.Positive(t, cutB, kind)
		assert.Equal(t, a[:cutA], b[:cutB], kind)
		assert.NotEqual(t, a[cutA:], b[cutB:], kind)
		assert.Equal(t, 1, strings.Count(a, itemStart), kind)
	}
}

func TestEmit_TheItemLinesComeAfterTheProjectRules(t *testing.T) {
	t.Parallel()

	text := emit(t, specOf(brief.KindImpl))

	assert.Greater(t, strings.Index(text, itemStart), strings.Index(text, "PROJECT-RULES"))
	assert.Greater(t, strings.Index(text, "PROJECT-RULES"), strings.Index(text, part(t, brief.PartLongJobsRules)))
}

func TestEmit_RulesReachTheBriefByteForByte(t *testing.T) {
	t.Parallel()

	spec := specOf(brief.KindRev)
	spec.Rules = "keep\xffthis\r\n"

	assert.Contains(t, emit(t, spec), "keep\xffthis\r\n")
}

// generation

type generated struct {
	dir   string
	req   brief.Request
	book  *ledger.Ledger
	state string
}

func newGeneration(t *testing.T) generated {
	t.Helper()

	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.md")
	require.NoError(t, os.WriteFile(rules, []byte(rulesBase), 0o600))

	return generated{
		dir:   dir,
		book:  ledger.Open(dir),
		state: dir,
		req: brief.Request{
			Kind:      brief.KindImpl,
			Name:      "fix-1",
			Model:     "sonnet",
			RulesPath: rules,
			Lane:      brief.Lane{Key: "1", Acceptance: "none: x", BaseResult: "none: x", Mechanism: "m", Body: noBody},
			At:        when,
		},
	}
}

func (g generated) run() (brief.Generated, error) {
	return brief.Generate(g.req, brief.Deps{Book: g.book, Handoff: passes(nil, false)})
}

func (g generated) count(t *testing.T, key, kinds string) int {
	t.Helper()

	n, err := g.book.Count(key, kinds)
	require.NoError(t, err)

	return n
}

func TestGenerate_WritesTheFilledBriefAndTheRowOfTheLane(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.SlotsFile = writeFile(t, "slots.txt", "LANE=fix-1\nNAME=fix-1\n")

	got, err := g.run()

	require.NoError(t, err)
	assert.Contains(t, got.Text, "# Lane `fix-1`: [ONE LINE SAYING WHAT AND WHY]")
	assert.Contains(t, got.Text, "Report file: `<S>/reports/fix-1.md`")
	assert.Contains(t, got.Text, "keep-this-rule: run the preflight.")
	assert.Equal(t, []string{"pr=1 lane=fix-1 kind=fix fix lanes=1"}, got.Ledger)
	assert.Equal(t, 1, g.count(t, "1", ""))
}

func TestGenerate_AMechanismReachesTheRow(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.Mechanism = "robots-classifier"

	_, err := g.run()

	require.NoError(t, err)

	rounds, err := g.book.Rounds("1", "robots-classifier")
	require.NoError(t, err)
	assert.Equal(t, 1, rounds.Count)
}

func TestGenerate_ARelayLaneWritesARelayRow(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.Kind = brief.LaneRelay
	g.req.Lane.Mechanism = ""

	_, err := g.run()

	require.NoError(t, err)
	assert.Equal(t, 1, g.count(t, "1", "relay"))
}

func TestGenerate_ADesignLaneWritesADesignRowAndNoFixLane(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Kind = brief.KindDesign
	g.req.Model = "opus:stuck"

	got, err := g.run()

	require.NoError(t, err)
	assert.Contains(t, got.Text, "Tier row: `stuck`.")
	assert.Equal(t, 1, g.count(t, "1", "design"))
	assert.Equal(t, 0, g.count(t, "1", ""))
}

func TestGenerate_AReviewOfAPullRequestWritesTheReviewRowAndTheClosesLine(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Kind = brief.KindRev
	g.req.Lane = brief.Lane{ReviewPR: "7", Body: func() (string, error) { return "Closes #12\n", nil }}

	got, err := g.run()

	require.NoError(t, err)
	assert.Equal(t, []string{
		"first review of 7: recorded the issues it closes: 12",
		"pr=7 lane=fix-1 kind=review fix lanes=0",
	}, got.Ledger)
	assert.Equal(t, 1, g.count(t, "7", "review"))
}

func TestGenerate_AReviewKeyedByTheLaneKeyWhenItHasOne(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Kind = brief.KindRev
	g.req.Lane = brief.Lane{Key: "item-9", ReviewPR: "7", Body: noBody}

	_, err := g.run()

	require.NoError(t, err)
	assert.Equal(t, 1, g.count(t, "item-9", "review"))
}

func TestGenerate_AReviewWithNoPullRequestWritesNoRow(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Kind = brief.KindRev
	g.req.Lane = brief.Lane{}

	got, err := g.run()

	require.NoError(t, err)
	assert.Empty(t, got.Ledger)
	assert.NoFileExists(t, filepath.Join(g.state, ledger.FileName))
}

func TestGenerate_AFirstReviewWithNoBodyReaderIsARefusal(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Kind = brief.KindRev
	g.req.Lane = brief.Lane{ReviewPR: "7"}

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the first review of '7' needs the pull request body.")
}

func TestGenerate_ARefusedBriefWritesNoRow(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.Acceptance = ""

	_, err := g.run()

	require.Error(t, err)
	assert.Equal(t, 0, g.count(t, "1", ""))
}

func TestGenerate_TheOrderOfTheRefusalsIsTheRulesTheModelTheSlotsTheCapThenTheLane(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.Acceptance = ""
	g.req.Lane.Cap = -1
	g.req.SlotsFile = "/no/slots.txt"
	g.req.Model = "inherit"
	g.req.RulesPath = "/no/rules.md"

	for _, want := range []string{"no brief rules at /no/rules.md", "no model for lane fix-1", "no slots file at /no/slots.txt", "the lane context cap is -1", "has no acceptance; give a path with the acceptance field"} {
		_, err := g.run()

		require.Error(t, err)
		assert.Contains(t, err.Error(), want)

		switch {
		case strings.Contains(want, "rules"):
			g.req.RulesPath = filepath.Join(g.dir, "rules.md")
		case strings.Contains(want, "model"):
			g.req.Model = "sonnet"
		case strings.Contains(want, "slots"):
			g.req.SlotsFile = ""
		case strings.Contains(want, "cap"):
			g.req.Lane.Cap = 0
		}
	}
}

func TestGenerate_ARulesFileTheCutRefusesIsARefusal(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.RulesPath = writeFile(t, "rules.md", rulesBase+"\n## Bad\n<!-- for: nonsense -->\n")

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), g.req.RulesPath)
}

func TestGenerate_ReadFirstMustBeAFileAndItsListIsSized(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.ReadFirst = "/no/list.txt"

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the read-first list at /no/list.txt is not a file. Write it with one path on each line.")

	listed := writeFile(t, "big.md", strings.Repeat("x", brief.ReadLimit))
	g.req.Lane.ReadFirst = writeFile(t, "list.txt", listed+"\n\n")

	_, err = g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the required reading is ")
	assert.Equal(t, 0, g.count(t, "1", ""))
}

func TestGenerate_AListedPathThatIsNoFileIsARefusal(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.ReadFirst = writeFile(t, "list.txt", "/no/such/file.md\n")

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the required reading names /no/such/file.md, which is not a file here")
}

func TestGenerate_AListThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.ReadFirst = writeFile(t, "list.txt", "a\x00b")

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds a NUL byte")
}

func TestGenerate_TheBriefItselfCountsAgainstTheReadLimit(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.SlotsFile = writeFile(t, "slots.txt", "LANE="+strings.Repeat("y", brief.ReadLimit)+"\n")

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cut the list")
}

func TestGenerate_ABookThatCannotAddIsAnError(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Lane.Mechanism = "m"

	_, err := brief.Generate(g.req, brief.Deps{Book: brokenBook{boom: assert.AnError, fails: "add"}, Handoff: passes(nil, false)})

	require.ErrorIs(t, err, assert.AnError)
}

func TestGenerate_AnUnknownModelNeedsATierRow(t *testing.T) {
	t.Parallel()

	g := newGeneration(t)
	g.req.Model = "opus"

	_, err := g.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lane fix-1 asks for opus with no tier row")
}
