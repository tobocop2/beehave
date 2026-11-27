package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/cli"
	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/ownprogram"
)

const ownHead = "0123456789012345678901234567890123456789"

// The hidden characters of the display tests, as UTF-8 bytes, so the test source holds none of them.
const (
	lineSeparator      = "\xe2\x80\xa8" // U+2028
	paragraphSeparator = "\xe2\x80\xa9" // U+2029
	rlo                = "\xe2\x80\xae" // U+202E, right-to-left override
	lri                = "\xe2\x81\xa6" // U+2066, left-to-right isolate
	pdi                = "\xe2\x81\xa9" // U+2069, pop directional isolate
	zwsp               = "\xe2\x80\x8b" // U+200B, zero-width space
	zwj                = "\xe2\x80\x8d" // U+200D, zero-width joiner
	bom                = "\xef\xbb\xbf" // U+FEFF, zero-width no-break space
	shy                = "\xc2\xad"     // U+00AD, soft hyphen
)

// ownRig is the gate command over a fake forge: the pull request it reads, the paths it changes and the statuses that the post received.
type ownRig struct {
	*flowRig

	branch  string
	changed []string
	passed  bool
	posted  []model.CommitStatus
	judged  int
	build   error

	base      string
	diff      string
	answer    string
	notTTY    bool
	listed    []model.Item
	listedErr error
}

func newOwnRig(t *testing.T) *ownRig {
	t.Helper()

	return &ownRig{flowRig: newRig(t), branch: "owner/gate-change", passed: true, base: "main", diff: "diff --git a/Makefile b/Makefile\n", answer: "7\n"}
}

func (o *ownRig) deps(_ context.Context, _ config.Config, _ flow.Options, _ string) (ownprogram.Deps, error) {
	if o.build != nil {
		return ownprogram.Deps{}, o.build
	}

	head, branch, base := ownHead, o.branch, o.base

	return ownprogram.Deps{
		Pull: func(context.Context, int) (model.PullRequest, error) {
			return model.PullRequest{HeadRefOid: &head, HeadRefName: &branch, BaseRefName: &base}, nil
		},
		OpenItems: func(context.Context) ([]model.Item, error) { return o.listed, o.listedErr },
		Tip:       func(context.Context, string) (string, error) { return "abc", nil },
		Changed:   func(context.Context, string, string) ([]string, error) { return o.changed, nil },
		Diff:      func(context.Context, string, string) (string, error) { return o.diff, nil },
		Judge: func(context.Context, string) (ownprogram.Verdict, error) {
			o.judged++

			return ownprogram.Verdict{Head: ownHead, Key: "k", Passed: o.passed, Report: "the lint step failed"}, nil
		},
		Post: func(_ context.Context, statuses []model.CommitStatus) (string, error) {
			o.posted = append(o.posted, statuses...)

			return "", nil
		},
	}, nil
}

func (o *ownRig) run(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	info := cli.BuildInfo{
		Registry: registry(), Version: "v9", Flow: o.factory(), OwnProgram: o.deps,
		Console: func() (io.Reader, bool) { return strings.NewReader(o.answer), !o.notTTY },
	}
	code := cli.Execute(ctx, append(args, "--config", o.dir), info, o.env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestGate_OwnProgramJudgesThePullRequestAndPostsTheStatusOnItsHead(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "pull request 7: the gate passed on "+ownHead)
	assert.Equal(t, []model.CommitStatus{{SHA: ownHead, Context: "beehest/gate", State: model.StatusSuccess, Description: "gate program changed, run by owner command, gate key k"}}, rig.posted)
}

func TestGate_OwnProgramRefusesAPullRequestOnTheBranchOfAnItemOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "r1", Item: "12", Project: "p"}, {ID: "r2", Item: "99", Project: "other"}, {ID: "r3", Project: "p"}}
	rig.branch = "12"

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "the pull request is on the branch of an item")
	assert.Zero(t, rig.judged)
	assert.Empty(t, rig.posted)
}

func TestGate_OwnProgramTakesTheBranchOfAnItemThatItsStoreCannotPlaceInAProject(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "r1", Item: "12"}}
	rig.branch = "12"

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7", "p")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "the pull request is on the branch of an item")
}

func TestGate_OwnProgramLeavesTheBranchOfAnItemOfAnotherProjectFree(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "r2", Item: "99", Project: "other"}}
	rig.branch = "99"

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 0, code, stderr)
}

func TestGate_OwnProgramRefusesAChangeOutsideGateProgram(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.changed = []string{"internal/gate/gate.go"}

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "outside gate_program: internal/gate/gate.go")
	assert.Empty(t, rig.posted)
}

func TestGate_OwnProgramPrintsTheReportOfAFailedGateAndExitsWithOne(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.passed = false

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.NotContains(t, stdout, "the gate passed")
	assert.Contains(t, stderr, "the gate failed on "+ownHead)
	assert.Contains(t, stderr, "the lint step failed")
	require.Len(t, rig.posted, 1)
	assert.Equal(t, model.StatusFailure, rig.posted[0].State)
}

func TestGate_RunsOnlyOnATerminalAndTouchesNothingElseFirst(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.notTTY = true
	rig.clientOf = func() error { return errors.New("client opened") }
	rig.build = errors.New("effects built")

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "runs only on a terminal")
	assert.NotContains(t, stderr, "client opened")
	assert.NotContains(t, stderr, "effects built")
	assert.Empty(t, stdout)
	assert.Zero(t, rig.judged)
	assert.Empty(t, rig.posted)
}

func TestGate_PrintsTheDiffAndRunsTheGateOnlyAfterTheOwnerTypesTheNumber(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "diff --git a/Makefile b/Makefile")
	assert.Contains(t, stdout, "Type 7 to run the gate")
	assert.Less(t, strings.Index(stdout, "diff --git"), strings.Index(stdout, "the gate passed"))
	assert.Equal(t, 1, rig.judged)
}

func TestGate_AnAnswerThatIsNotTheNumberOrNoAnswerRunsNothing(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]string{"another number": "8\n", "yes": "yes\n", "empty line": "\n", "end of input": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newOwnRig(t)
			rig.answer = answer

			code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "the owner did not confirm the diff")
			assert.Zero(t, rig.judged)
			assert.Empty(t, rig.posted)
		})
	}
}

func TestGate_TheDiffOnTheTerminalHasNoControlCharacters(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.diff = "+ok\n\x1b[2J\x1b[Hhidden\r+tab\there" + lineSeparator + "end\n"

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	require.Equal(t, 0, code, stderr)
	assert.NotContains(t, stdout, "\x1b")
	assert.NotContains(t, stdout, "\r")
	assert.NotContains(t, stdout, lineSeparator)
	assert.Contains(t, stdout, "+ok\n<U+001B>[2J<U+001B>[Hhidden<U+000D>+tab\there<U+2028>end\n")
}

func TestGate_TheDiffOnTheTerminalShowsBidiOverridesAndZeroWidthCharactersAsEscapes(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	// The Trojan Source pattern: a right-to-left override and an isolate reorder the line on the terminal, a zero-width space hides inside a word.
	rig.diff = "+if access != \"user" + rlo + " " + lri + "// admin" + pdi + " " + lri + "\" {\n+ex" + zwsp + "it 0\n+" + bom + "soft" + shy + "hyphen" + zwj + paragraphSeparator + "\n"

	code, stdout, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	require.Equal(t, 0, code, stderr)

	for _, hidden := range []string{rlo, lri, pdi, zwsp, bom, shy, zwj, paragraphSeparator} {
		assert.NotContains(t, stdout, hidden)
	}

	assert.Contains(t, stdout, "+if access != \"user<U+202E> <U+2066>// admin<U+2069> <U+2066>\" {\n")
	assert.Contains(t, stdout, "+ex<U+200B>it 0\n")
	assert.Contains(t, stdout, "+<U+FEFF>soft<U+00AD>hyphen<U+200D><U+2029>\n")
}

func TestGate_RefusesTheBranchOfAnOpenItemThatNoRunNamesAndTheFormsOfTheBranchName(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"bh-12", "BH-12", "refs/heads/bh-12", "bh-12-gate"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()

			rig := newOwnRig(t)
			rig.listed = []model.Item{{ID: "bh-12"}}
			rig.branch = branch

			code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "the pull request is on the branch of an item")
			assert.Zero(t, rig.judged)
		})
	}
}

func TestGate_AFailedReadOfTheItemListOfTheTrackerStopsTheCommand(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.listedErr = errors.New("list failed")

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "list failed")
	assert.Zero(t, rig.judged)
	assert.Empty(t, rig.posted)
}

func TestGate_ABuildWithNoItemListStillRefusesTheBranchOfAnItemThatARunNames(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "r1", Item: "12", Project: "p"}}
	rig.branch = "12"

	var stdout, stderr bytes.Buffer

	info := cli.BuildInfo{
		Registry: registry(), Flow: rig.factory(), OwnProgram: rig.deps,
		Console: func() (io.Reader, bool) { return strings.NewReader("7\n"), true },
	}
	code := cli.Execute(t.Context(), []string{"gate", "--own-program", "7", "--config", rig.dir}, info, rig.env, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "the pull request is on the branch of an item")
}

func TestGate_TheConsoleOfTheProcessIsNotATerminalUnderTheTestRunner(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	var stdout, stderr bytes.Buffer

	info := cli.BuildInfo{Registry: registry(), Flow: rig.factory(), OwnProgram: rig.deps}
	code := cli.Execute(t.Context(), []string{"gate", "--own-program", "7", "--config", rig.dir}, info, rig.env, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "runs only on a terminal")
	assert.Zero(t, rig.judged)
}

func TestGate_RefusesAPullRequestIntoABranchThatIsNotTheBaseOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.base = "evil"

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "does not target the base branch of the project")
	assert.Zero(t, rig.judged)
	assert.Empty(t, rig.posted)
}

func TestGate_WithoutTheOwnProgramFlagTheCommandRunsNothing(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	code, _, stderr := rig.run(t.Context(), "gate", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "runs only with --own-program")
	assert.Zero(t, rig.judged)
}

func TestGate_ArgumentsThatAreNotAPullRequestAndAProjectAreRefused(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"none":         {"gate", "--own-program"},
		"too many":     {"gate", "--own-program", "7", "p", "q"},
		"not a number": {"gate", "--own-program", "seven"},
		"zero":         {"gate", "--own-program", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newOwnRig(t)

			code, _, stderr := rig.run(t.Context(), args...)

			assert.Equal(t, 1, code)
			assert.NotEmpty(t, stderr)
			assert.Zero(t, rig.judged)
		})
	}
}

func TestGate_AnUnknownProjectAFailedStoreAndAFailedBuildStopTheCommand(t *testing.T) {
	t.Parallel()

	errFake := errors.New("fake failure")

	for name, edit := range map[string]func(*ownRig){
		"client":  func(o *ownRig) { o.clientOf = func() error { return errFake } },
		"status":  func(o *ownRig) { o.steering.err = errFake },
		"build":   func(o *ownRig) { o.build = errFake },
		"project": func(*ownRig) {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newOwnRig(t)
			edit(rig)

			args := []string{"gate", "--own-program", "7"}
			if name == "project" {
				args = append(args, "nope")
			}

			code, _, stderr := rig.run(t.Context(), args...)

			assert.Equal(t, 1, code)
			assert.NotEmpty(t, stderr)
			assert.Zero(t, rig.judged)
		})
	}
}

func TestGate_AConfigThatDoesNotLoadStopsTheCommand(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.dir = t.TempDir() + "/missing"

	code, _, _ := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.NotEqual(t, 0, code)
}

func TestGate_AClientThatFailsToCloseFailsTheCommand(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)
	rig.steering.closeErr = errors.New("close failed")

	code, _, stderr := rig.run(t.Context(), "gate", "--own-program", "7")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "close failed")
}

func TestGate_ABuildWithoutTheOwnerCommandHasNoGateCommand(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	var stdout, stderr bytes.Buffer

	code := cli.Execute(t.Context(), []string{"gate", "--own-program", "7", "--config", rig.dir}, cli.BuildInfo{Registry: registry(), Flow: rig.factory()}, rig.env, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "unknown flag: --own-program", "the gate group of a build without the owner command has the hold commands and no own-program flag")
}
