package remedy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/model"
)

// firstReview records the first review of a key whose pull request body closed the issues of the text.
func firstReview(t *testing.T, state, key, text string) {
	t.Helper()

	code, _, errOut := run(t, ledgerDeps(&bodyWorld{}), "ledger", "add", key, "rev-1", "review", "--body-file", bodyFile(t, text), "--state", state)

	require.Equal(t, 0, code, errOut)
}

func TestScope_APullRequestWithNoFirstReviewPasses(t *testing.T) {
	t.Parallel()

	world := &bodyWorld{body: "Closes #5"}

	code, out, errOut := run(t, ledgerDeps(world), "scope", "7", "--state", t.TempDir())

	assert.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "OK: the ledger holds no review row for 7")
	assert.Empty(t, world.asked)
}

func TestScope_ANewClosedIssueIsExitCodeTwoAndNamesTheCommandForTheOwnerRow(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "7", "Closes #12")

	world := &bodyWorld{body: "Closes #12 and closes #13"}

	code, out, _ := run(t, ledgerDeps(world), "scope", "7", "--state", state)

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "REFUSED: pull request 7 closes #13 now")
	assert.Contains(t, out, "remedy ledger add 7 <issue> owner-add --state <folder> --words")
	assert.Equal(t, []int{7}, world.asked)
}

func TestScope_AnOwnerRowLetsTheIssueStay(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "7", "Closes #12")

	deps := ledgerDeps(&bodyWorld{body: "Closes #12 and closes #13"})

	code, _, errOut := run(t, deps, "ledger", "add", "7", "13", "owner-add", "--words", "keep it", "--state", state)
	require.Equal(t, 0, code, errOut)

	code, out, errOut := run(t, deps, "scope", "7", "--state", state)

	assert.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "OK: pull request 7 closes no issue that is new")
}

func TestScope_TheKeyFlagNamesTheLedgerKeyOfAnItemWhosePullRequestHasAnotherNumber(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "h2m", "Closes #12")

	world := &bodyWorld{body: "Closes #13"}

	code, out, _ := run(t, ledgerDeps(world), "scope", "792", "--key", "h2m", "--state", state)

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "REFUSED: pull request 792 closes #13 now")
	assert.Equal(t, []int{792}, world.asked)
}

func TestScope_AFailedReadIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "7", "Closes #12")

	failing := ledgerDeps(&bodyWorld{bodyErr: errDown})
	noOrigin := ledgerDeps(&bodyWorld{})
	noOrigin.Git = git(func([]string) model.CommandResult { return model.CommandResult{ExitCode: 1} })
	noForge := ledgerDeps(&bodyWorld{})
	noForge.Forge = func(string) (remedy.Forge, error) { return nil, errDown }

	for name, deps := range map[string]remedy.Deps{"the body read fails": failing, "no origin remote": noOrigin, "the forge does not open": noForge} {
		code, out, errOut := run(t, deps, "scope", "7", "--state", state)

		assert.Equal(t, 2, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "remedy scope:", name)
	}
}

// closingWorld is a forge that lists closing references. Its body closes nothing, so only the references can refuse.
type closingWorld struct {
	bodyWorld
	closing    []int
	closingErr error
	closingFor []int
	base       string
}

func TestLedgerAdd_TheFirstReviewRecordsTheSameIssuesTheScopeCheckReads(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	world := &closingWorld{closing: []int{12, 13}, base: "main", bodyWorld: bodyWorld{body: "Closes #12"}}

	code, out, errOut := run(t, closingDeps(world), "ledger", "add", "7", "rev-1", "review", "--pr", "7", "--state", state)

	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "12")
	assert.Contains(t, out, "13", "the sidebar-linked issue is recorded")

	code, out, errOut = run(t, closingDeps(world), "scope", "7", "--state", state)

	assert.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "OK:")
}

func TestLedgerAdd_AFirstReviewOfAPullRequestIntoAnotherBranchRecordsTheBodyKeywords(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	world := &closingWorld{base: "integration/r7", bodyWorld: bodyWorld{body: "Closes #12"}}

	code, out, errOut := run(t, closingDeps(world), "ledger", "add", "7", "rev-1", "review", "--pr", "7", "--state", state)

	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "12")
	assert.Equal(t, []int{7}, world.closingFor, "one read of the forge, which tells the base branch")
}

// ClosingRefs answers like the forge: the issues, the base branch of the pull request and the default branch "main" in one read.
func (w *closingWorld) ClosingRefs(_ context.Context, pr int) (model.ClosingRefs, error) {
	w.closingFor = append(w.closingFor, pr)

	return model.ClosingRefs{BaseBranch: w.base, DefaultBranch: "main", Issues: w.closing}, w.closingErr
}

func closingDeps(world *closingWorld) remedy.Deps {
	deps := ledgerDeps(&world.bodyWorld)
	deps.Forge = func(string) (remedy.Forge, error) { return world, nil }

	return deps
}

func TestScope_AForgeThatListsClosingReferencesIsAskedForThemNotForTheBody(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "7", "Closes #12")

	world := &closingWorld{closing: []int{12, 13}, base: "main"}

	code, out, _ := run(t, closingDeps(world), "scope", "7", "--state", state)

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "REFUSED: pull request 7 closes #13 now")
	assert.Equal(t, []int{7}, world.closingFor)
	assert.Empty(t, world.asked, "the body is not read")
}

func TestScope_AFailedReadOfTheClosingReferencesIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	firstReview(t, state, "7", "Closes #12")

	world := &closingWorld{closingErr: errDown, base: "main"}

	code, out, errOut := run(t, closingDeps(world), "scope", "7", "--state", state)

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "cannot read the issues that pull request 7 closes")
}

func TestScope_APullRequestThatIsNotANumberIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, ledgerDeps(&bodyWorld{}), "scope", "seven", "--state", t.TempDir())

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "digits only")
}

func TestScope_ArgumentsThatAreMissingOrUnknownAreARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no pull request":            {"scope"},
		"a flag where the number is": {"scope", "--state", "/s"},
		"no state folder":            {"scope", "7"},
		"an argument after flags":    {"scope", "7", "--state", "/s", "extra"},
		"a flag that does not exist": {"scope", "7", "--bogus"},
	} {
		code, out, errOut := run(t, ledgerDeps(&bodyWorld{}), args...)

		assert.Equal(t, 2, code, name)
		assert.Empty(t, out, name)
		assert.NotEmpty(t, errOut, name)
	}
}
