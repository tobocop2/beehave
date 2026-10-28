package brief_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

const rulesBase = "## Maintainer preflight\n- keep-this-rule: run the preflight.\n\n" +
	"## CI checks and their base\n\n- Run: `poly fmt --check`\n- Base: the pull request base\n"

func loadRules(t *testing.T, text string) (brief.Rules, error) {
	t.Helper()

	return brief.LoadRules(writeFile(t, "rules.md", text))
}

func TestLoadRules_ReturnsThePathAndTheTextByteForByte(t *testing.T) {
	t.Parallel()

	text := strings.Replace(rulesBase, "keep-this-rule", "keep\xffthis-rule", 1)
	path := writeFile(t, "rules.md", text)

	rules, err := brief.LoadRules(path)

	require.NoError(t, err)
	assert.Equal(t, path, rules.Path)
	assert.Equal(t, text, rules.Text)
}

func TestLoadRules_RefusesARulesFileWithNoPreflightSection(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## CI checks and their base\n- Run: x\n- Base: y\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no '## Maintainer preflight' section")
	assert.Contains(t, err.Error(), "write 'none measured yet' under the heading")
}

func TestLoadRules_RefusesARulesFileWithNoCISection(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## Maintainer preflight\nnone measured yet\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no '## CI checks and their base' section with a '- Run:' line and a '- Base:' line")
}

func TestLoadRules_TheBaseLineMustSitInsideTheCISection(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## Maintainer preflight\nnone\n## CI checks and their base\n- Run: task lint\n"+
		"## Other\n- Base: main\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no '## CI checks and their base' section")
}

func TestLoadRules_TheRunLineAndTheBaseLineNeedAValue(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## Maintainer preflight\nnone\n## CI checks and their base\n- Run: \n- Base: main\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no '## CI checks and their base' section")

	_, err = loadRules(t, "## Maintainer preflight\nnone\n## CI checks and their base\n- Run: task lint\n- Base:\n")

	require.Error(t, err)
}

func TestLoadRules_AcceptsARunLineAndABaseLine(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## Maintainer preflight\nnone\n## CI checks and their base\n- Run: task lint\n"+
		"- Base: the pull request base\n")

	require.NoError(t, err)
}

func TestLoadRules_ALoneCarriageReturnIsNotALineEnd(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, "## Maintainer preflight\r- r\r\r## CI checks and their base\r- Run: `x`\r- Base: y\r")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no '## CI checks and their base' section")
}

func TestLoadRules_ACRLFFileLoads(t *testing.T) {
	t.Parallel()

	rules, err := loadRules(t, strings.ReplaceAll(rulesBase, "\n", "\r\n")+"## Standing rules\r\n- Take: build-host\r\n")

	require.NoError(t, err)
	assert.Equal(t, brief.Taken{brief.BlockBuildHost}, rules.Taken)
}

func TestLoadRules_RefusesAFileThatIsNotThere(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "rules.md")

	_, err := brief.LoadRules(missing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no brief rules at "+missing)
	assert.Contains(t, err.Error(), "Put the rules of the project in that file.")
}

func TestLoadRules_RefusesAFolder(t *testing.T) {
	t.Parallel()

	_, err := brief.LoadRules(t.TempDir())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no brief rules at ")
}

func TestLoadRules_RefusesAFileWithANulByte(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "rules.md", rulesBase+"\x00")

	_, err := brief.LoadRules(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), path+" holds a NUL byte. Rewrite it as plain text.")
}

func TestLoadRules_TakesAllBlocksWhenTheFileNamesNone(t *testing.T) {
	t.Parallel()

	rules, err := loadRules(t, rulesBase)

	require.NoError(t, err)
	assert.Equal(t, brief.Taken(brief.Blocks()), rules.Taken)
}

func TestLoadRules_TakesTheBlocksThatTheStandingSectionNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, section string
		want          brief.Taken
	}{
		{"one", "- Take: build-host", brief.Taken{brief.BlockBuildHost}},
		{"two", "- Take: union compiled", brief.Taken{brief.BlockUnion, brief.BlockCompiled}},
		{"tabs-and-spaces", "- Take:  build-host \t union ", brief.Taken{brief.BlockBuildHost, brief.BlockUnion}},
		{"none", "- Take: none", brief.Taken{}},
		{"first-take-wins", "- Take: compiled\n- Take: union", brief.Taken{brief.BlockCompiled}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rules, err := loadRules(t, rulesBase+"## Standing rules\n\n"+tc.section+"\n")

			require.NoError(t, err)
			assert.Equal(t, tc.want, rules.Taken)
		})
	}
}

func TestLoadRules_ATakeLineOutsideTheStandingSectionIsIgnored(t *testing.T) {
	t.Parallel()

	rules, err := loadRules(t, rulesBase+"- Take: none\n## Standing rules\n## Later\n- Take: none\n")

	require.NoError(t, err)
	assert.Equal(t, brief.Taken(brief.Blocks()), rules.Taken)
}

func TestLoadRules_RefusesABlockThatDoesNotExist(t *testing.T) {
	t.Parallel()

	for _, take := range []string{"build-host cargo", "reunion", "build-hosts", "Union"} {
		_, err := loadRules(t, rulesBase+"## Standing rules\n- Take: "+take+"\n")

		require.Error(t, err, take)
		assert.Contains(t, err.Error(), "which does not exist. The blocks are build-host, compiled and union.", take)
	}

	_, err := loadRules(t, rulesBase+"## Standing rules\n- Take: build-host cargo\n")

	assert.Contains(t, err.Error(), "takes the standing block 'cargo'")
}

func TestLoadRules_RefusesATakeLineWithNoBlock(t *testing.T) {
	t.Parallel()

	_, err := loadRules(t, rulesBase+"## Standing rules\n- Take:\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has '- Take:' with no block under '## Standing rules'")
	assert.Contains(t, err.Error(), "Name the blocks: build-host, compiled, union, or none.")
}

func TestTaken_HasTheWholeNameOnly(t *testing.T) {
	t.Parallel()

	taken := brief.Taken{brief.BlockUnion}

	assert.True(t, taken.Has(brief.BlockUnion))
	assert.False(t, taken.Has(brief.BlockBuildHost))
	assert.False(t, brief.Taken{}.Has(brief.BlockUnion))
}

func TestBlock_ABlockOutsideTheSetHasNoPart(t *testing.T) {
	t.Parallel()

	assert.Empty(t, brief.Block("cargo").Part())
}

func TestParseBlock_NamesTheClosedSet(t *testing.T) {
	t.Parallel()

	for _, block := range brief.Blocks() {
		got, err := brief.ParseBlock(string(block))
		require.NoError(t, err)
		assert.Equal(t, block, got)
	}

	_, err := brief.ParseBlock("reunion")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "'reunion' is not a standing block")
}

func TestRules_LeanCutsTheTextForAKind(t *testing.T) {
	t.Parallel()

	rules, err := loadRules(t, rulesBase+"## Rev only\n<!-- for: rev -->\nREV-ONLY\n")
	require.NoError(t, err)

	impl, err := rules.Lean(brief.KindImpl)
	require.NoError(t, err)
	assert.NotContains(t, impl, "REV-ONLY")

	rev, err := rules.Lean(brief.KindRev)
	require.NoError(t, err)
	assert.Contains(t, rev, "REV-ONLY")
}

func TestRules_LeanNamesTheRulesFileInARefusal(t *testing.T) {
	t.Parallel()

	rules, err := loadRules(t, rulesBase+"<!--why\nno end\n")
	require.NoError(t, err)

	_, err = rules.Lean(brief.KindImpl)

	require.Error(t, err)
	assert.Contains(t, err.Error(), rules.Path+":")
}

// command finds the program name before a subcommand of a command line written in backticks.
var command = regexp.MustCompile("`(\\S+) (?:check|brief|host) ")

func TestText_EveryPartIsAFileOfTheEmbeddedFolder(t *testing.T) {
	t.Parallel()

	parts := brief.Parts()
	require.Len(t, parts, 19)

	for _, part := range parts {
		text, err := brief.Text(part)

		require.NoError(t, err, part)
		assert.NotEmpty(t, text, part)

		for _, found := range command.FindAllStringSubmatch(text, -1) {
			assert.Equal(t, "obee", found[1], part)
		}
	}
}

func TestText_APartThatDoesNotExistIsAnError(t *testing.T) {
	t.Parallel()

	_, err := brief.Text("no_such_rules")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no_such_rules")
}

func TestText_EachStandingBlockHasItsOwnPart(t *testing.T) {
	t.Parallel()

	want := map[brief.Block]string{
		brief.BlockBuildHost: "On a remote build host, a job runs",
		brief.BlockCompiled:  "A BINARY FIXTURE MUST BE ASSERTED INVALID UTF-8",
		brief.BlockUnion:     "A union identity is not complete",
	}

	for _, block := range brief.Blocks() {
		text, err := brief.Text(block.Part())

		require.NoError(t, err, block)
		assert.Contains(t, text, want[block], block)
	}
}

func TestText_NamesEveryObeeCommandAndNoOldToolNameOrScriptPath(t *testing.T) {
	t.Parallel()

	commands := []struct {
		part    brief.Part
		command string
	}{
		{brief.PartItemSkeleton, "`obee brief check <this brief>`"},
		{brief.PartDesignRules, "`obee check papers <base>..<head>`"},
		{brief.PartPapersRules, "`obee check papers <base>..<head>`"},
		{brief.PartGateStepRules, "`obee check job --list"},
		{brief.PartGateStepRules, "`obee check job --lane <lane> --script <host path> <script>"},
		{brief.PartClosingRules, "`obee check report --head <sha> <report>`"},
		{brief.PartClosingRules, "`obee check handoff`"},
		{brief.PartBuildHostRules, "`obee host submit <lane> --script <script> --log <log> --marker <MARKER>`"},
		{brief.PartBuildHostRules, "`obee host clean <lane> <path>...`"},
		{brief.PartBuildHostRules, "`obee check transcript`"},
		{brief.PartLongJobsRules, "`obee host submit`"},
	}

	for _, tc := range commands {
		text, err := brief.Text(tc.part)

		require.NoError(t, err, tc.part)
		assert.Contains(t, text, tc.command, tc.part)
	}

	for _, part := range brief.Parts() {
		text, err := brief.Text(part)

		require.NoError(t, err, part)

		for _, old := range []string{"broodbee", "scripts/", "host-job", "setsid nohup"} {
			assert.NotContains(t, text, old, part)
		}
	}
}

func TestText_TheSkeletonAndTheLaneNamesKeepTheirSlots(t *testing.T) {
	t.Parallel()

	skeleton, err := brief.Text(brief.PartSkeleton)
	require.NoError(t, err)
	assert.Equal(t, "`<S>` is `[SCRATCHPAD]`. Read the **CORE section** of `<S>/lane-rules.md` whole before writing anything.\n\n", skeleton)

	names, err := brief.Text(brief.PartLaneNames)
	require.NoError(t, err)
	assert.Contains(t, names, "- Handoff file: `<S>/handoffs/[NAME].md`")
}
