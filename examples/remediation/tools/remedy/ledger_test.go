package remedy_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/model"
)

var ledgerNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// bodyWorld is a fake forge that holds the body of a pull request and records each body read.
type bodyWorld struct {
	forgeWorld
	body    string
	bodyErr error
	asked   []int
}

func (w *bodyWorld) PRBody(_ context.Context, pr int) (string, error) {
	w.asked = append(w.asked, pr)

	return w.body, w.bodyErr
}

// ledgerDeps are deps with the real ledger file, the fake forge and a fixed time.
func ledgerDeps(world *bodyWorld) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return world, nil },
		Now:   func() time.Time { return ledgerNow },
		Book:  remedy.OpenBook(ledger.Open),
	}
}

// bodyFile writes a pull request body to a file and gives its path.
func bodyFile(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestLedger_AddRecordsALaneAndCountReadsItBack(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	deps := ledgerDeps(&bodyWorld{})

	code, out, errOut := run(t, deps, "ledger", "add", "7", "fix-1", "fix", "--mechanism", "robots", "--model", "opus", "--round", "2", "--state", state)

	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Empty(t, out)

	code, out, _ = run(t, deps, "ledger", "count", "7", "fix|rework|relay", "--state", state)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "1\n", out)

	_, out, _ = run(t, deps, "ledger", "count", "7", "design", "--state", state)

	assert.Equal(t, "0\n", out)
}

func TestLedger_RoundsAndPriorReadTheRowsOfAKey(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	deps := ledgerDeps(&bodyWorld{})

	for _, lane := range []string{"fix-1", "fix-2"} {
		code, _, errOut := run(t, deps, "ledger", "add", "7", lane, "fix", "--mechanism", "robots", "--state", state)
		require.Equal(t, remedy.ExitPass, code, errOut)
	}

	code, out, _ := run(t, deps, "ledger", "rounds", "7", "robots", "--state", state)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "1 open=true\n", out)

	code, out, _ = run(t, deps, "ledger", "prior", "7", "fix-2", "--state", state)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "1\n", out)
}

func TestLedger_TheFirstReviewRecordsTheIssuesOfTheBodyFile(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	deps := ledgerDeps(&bodyWorld{})
	body := bodyFile(t, "Fixes #12 and closes #14.")

	code, out, errOut := run(t, deps, "ledger", "add", "7", "rev-1", "review", "--body-file", body, "--state", state)

	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "12\n14\n", out)

	code, out, _ = run(t, deps, "ledger", "closes", "7", "--state", state)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "12\n14\n", out)
}

func TestLedger_TheFirstReviewReadsTheBodyOfThePullRequestThroughTheForge(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	world := &bodyWorld{body: "Closes #31"}

	code, out, errOut := run(t, ledgerDeps(world), "ledger", "add", "7", "rev-1", "review", "--pr", "7", "--state", state)

	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "31\n", out)
	assert.Equal(t, []int{7}, world.asked)
}

func TestLedger_ARowThatNeedsNoBodyReadsNoPullRequest(t *testing.T) {
	t.Parallel()

	world := &bodyWorld{body: "Closes #31"}

	code, _, errOut := run(t, ledgerDeps(world), "ledger", "add", "7", "fix-1", "fix", "--pr", "7", "--state", t.TempDir())

	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Empty(t, world.asked)
}

func TestLedger_AnOwnerAddRowKeepsTheWordsOfTheOwner(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	deps := ledgerDeps(&bodyWorld{})

	code, _, errOut := run(t, deps, "ledger", "add", "7", "13", "owner-add", "--words", "yes, keep it here", "--state", state)

	assert.Equal(t, remedy.ExitPass, code, errOut)

	text, err := os.ReadFile(filepath.Clean(filepath.Join(state, ledger.FileName)))
	require.NoError(t, err)
	assert.Equal(t, "7\t13\towner-add\t2026-10-28T12:00Z\tyes, keep it here\n", string(text))
}

func TestLedger_ARefusedRowIsExitCodeTwoAndWritesNothing(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"a kind that does not exist":  {"add", "7", "fix-1", "bogus"},
		"a first review with no body": {"add", "7", "rev-1", "review"},
		"both body sources":           {"add", "7", "rev-1", "review", "--pr", "7", "--body-file", "x"},
		"a body file that is missing": {"add", "7", "rev-1", "review", "--body-file", "/missing/body.md"},
		"an owner row with no words":  {"add", "7", "13", "owner-add"},
	} {
		state := t.TempDir()
		world := &bodyWorld{}

		code, out, errOut := run(t, ledgerDeps(world), append(append([]string{"ledger"}, args...), "--state", state)...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "remedy ledger add:", name)
		assert.NoFileExists(t, filepath.Join(state, ledger.FileName), name)
	}
}

func TestLedger_APullRequestThatCannotBeReadIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	args := []string{"ledger", "add", "7", "rev-1", "review", "--pr", "7", "--state", ""}

	failing := ledgerDeps(&bodyWorld{bodyErr: errDown})
	noOrigin := ledgerDeps(&bodyWorld{})
	noOrigin.Git = git(func([]string) model.CommandResult { return model.CommandResult{ExitCode: 1} })
	noForge := ledgerDeps(&bodyWorld{})
	noForge.Forge = func(string) (remedy.Forge, error) { return nil, errDown }

	for name, deps := range map[string]remedy.Deps{"the body read fails": failing, "no origin remote": noOrigin, "the forge does not open": noForge} {
		args[len(args)-1] = t.TempDir()

		code, _, errOut := run(t, deps, args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, errOut, "remedy ledger add:", name)
	}
}

func TestLedger_AFolderThatCannotBeReadIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	state := filepath.Join(file, "state")

	for _, args := range [][]string{
		{"count", "7", "fix"}, {"rounds", "7", "robots"}, {"prior", "7", "fix-1"}, {"closes", "7"},
	} {
		code, out, errOut := run(t, ledgerDeps(&bodyWorld{}), append(append([]string{"ledger"}, args...), "--state", state)...)

		assert.Equal(t, remedy.ExitRefused, code, args[0])
		assert.Empty(t, out, args[0])
		assert.Contains(t, errOut, "remedy ledger "+args[0]+":")
	}
}

func TestLedger_ArgumentsThatAreMissingOrUnknownAreARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no verb":                    {"ledger"},
		"an unknown verb":            {"ledger", "bake", "7", "--state", "/s"},
		"add with two arguments":     {"ledger", "add", "7", "fix-1", "--state", "/s"},
		"count with no kinds":        {"ledger", "count", "7", "--state", "/s"},
		"closes with no key":         {"ledger", "closes", "--state", "/s"},
		"no state folder":            {"ledger", "count", "7", "fix"},
		"an argument after flags":    {"ledger", "count", "7", "fix", "--state", "/s", "extra"},
		"a flag that does not exist": {"ledger", "count", "7", "fix", "--bogus"},
		"a flag of add on count":     {"ledger", "count", "7", "fix", "--state", "/s", "--mechanism", "robots"},
	} {
		code, out, errOut := run(t, ledgerDeps(&bodyWorld{}), args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.NotEmpty(t, errOut, name)
	}
}

func TestLedger_HelpListsTheLedgerAndScopeCommands(t *testing.T) {
	t.Parallel()

	_, out, _ := run(t, remedy.Deps{}, "--help")

	assert.Contains(t, out, "ledger add <key> <lane> <kind>")
	assert.Contains(t, out, "scope <pr>")
}
