package remedy_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	tip    = "1111111111111111111111111111111111111111"
	oldRef = "staging/remediation-V"
	newRef = "staging/remediation-VI"
)

var errDown = errors.New("down")

// git is a fake git. It answers each call from the function.
type git func(args []string) model.CommandResult

func (g git) Run(_ context.Context, args []string) model.CommandResult { return g(args) }

// origin answers the origin remote read with the address and every other call with other.
func origin(address string, other git) git {
	return func(args []string) model.CommandResult {
		if strings.Join(args, " ") == "remote get-url origin" {
			return model.CommandResult{Stdout: address + "\n"}
		}

		return other(args)
	}
}

func nothing([]string) model.CommandResult { return model.CommandResult{} }

// forgeWorld is a fake forge for one round cut: the integration pull request is in state, the trunk is at tip, and every write is recorded.
type forgeWorld struct {
	state  string
	writes []string
}

func (f *forgeWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{State: &f.state}, nil
}

func (*forgeWorld) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return nil, nil
}

func (*forgeWorld) BranchRef(_ context.Context, branch string) (string, bool, error) {
	return tip, branch == "main", nil
}

func (f *forgeWorld) CreateBranch(_ context.Context, branch, _ string) error {
	f.writes = append(f.writes, "create "+branch)

	return nil
}

func (*forgeWorld) SetBase(context.Context, int, string) error { return nil }

func (*forgeWorld) ReviewComments(context.Context, int) ([]model.Comment, error) { return nil, nil }

func (*forgeWorld) PostComment(context.Context, int, string) error { return nil }

func (*forgeWorld) PRBody(context.Context, int) (string, error) { return "", nil }

// run executes one command line and returns the exit code with the two outputs.
func run(t *testing.T, deps remedy.Deps, args ...string) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer

	code := remedy.Execute(context.Background(), args, deps, &out, &errOut)

	return code, out.String(), errOut.String()
}

func TestExecute_WithoutACommandRefusesAndShowsTheUsage(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{})

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "usage: remedy <command>")
}

func TestExecute_HelpListsEveryCommandOfTheTableAndPasses(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--help", "-h"} {
		code, out, _ := run(t, remedy.Deps{}, flag)

		assert.Equal(t, remedy.ExitPass, code)

		for _, name := range remedy.Names() {
			assert.Contains(t, out, "  "+name+" ")
		}
	}
}

func TestExecute_UnknownCommandRefuses(t *testing.T) {
	t.Parallel()

	code, _, errOut := run(t, remedy.Deps{}, "bake")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, `unknown command "bake"`)
}

func TestNames_AreTheCommandsOfTheTable(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"audit", "take", "admit", "size", "ledger", "recount", "scope", "board", "papers", "handoff", "authors", "spend", "job", "report"}, remedy.Names())
}

// history answers the reads of an audit. A call is answered by its arguments joined with spaces.
func history(answers map[string]model.CommandResult) git {
	return func(args []string) model.CommandResult {
		if answer, ok := answers[strings.Join(args, " ")]; ok {
			return answer
		}

		return model.CommandResult{Stdout: "x\n"}
	}
}

func auditDeps(t *testing.T, answers map[string]model.CommandResult) remedy.Deps {
	t.Helper()

	return remedy.Deps{Git: history(answers), Dir: t.TempDir()}
}

func TestAudit_AuthoredWithNoMergesPassesAndPrintsTheTotals(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{"rev-list --merges base..tip": {}})

	code, out, _ := run(t, deps, "audit", "authored", "base..tip")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "TOTAL_AUTHORED_PATHS=0\nAUD_AUTHORED_DONE\n", out)
}

func TestAudit_PresentWithAMergeThatLeftTheBranchIsADefect(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{
		"rev-parse tip":                   {Stdout: tip + "\n"},
		"rev-list --merges base..tip":     {Stdout: "m1\n"},
		"merge-base --is-ancestor m1 tip": {ExitCode: 1},
		"log --format=%s -1 m1":           {Stdout: "Merge pull request #7 from side\n"},
	})

	code, out, _ := run(t, deps, "audit", "present", "tip", "base")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "AUDIT_TIP="+tip)
	assert.Contains(t, out, "#7 MERGE_NOT_ANCESTOR_OF_TIP m1  <-- FINDING: the merge left the branch")
}

func TestAudit_PresentWithNoMergesPasses(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{
		"rev-parse tip":               {Stdout: tip + "\n"},
		"rev-list --merges base..tip": {},
	})

	code, out, _ := run(t, deps, "audit", "present", "tip", "base")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "MERGES_ENUMERATED=0")
	assert.Contains(t, out, "AUD19_PRESENT_DONE")
}

func TestAudit_ReadThatFailsIsARefusal(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{"rev-parse tip": {ExitCode: 128}})

	code, out, errOut := run(t, deps, "audit", "present", "tip", "base")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy audit: git rev-parse tip failed")
}

func TestAudit_FolderThatIsNotThereIsARefusal(t *testing.T) {
	t.Parallel()

	deps := remedy.Deps{Git: git(nothing), Dir: t.TempDir() + "/gone"}

	code, _, errOut := run(t, deps, "audit", "authored", "base..tip")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "cannot enter")
}

func TestAudit_ArgumentsThatNameNoReportAreARefusal(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"audit"},
		{"audit", "authored"},
		{"audit", "authored", "a", "b"},
		{"audit", "present", "tip"},
		{"audit", "everything", "tip", "base"},
	} {
		code, out, errOut := run(t, auditDeps(t, nil), args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.Contains(t, errOut, "usage: remedy audit", args)
	}
}

func TestAudit_MarkerInsideAPathIsNotAFinding(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{
		"rev-list --merges base..tip":                      {Stdout: "m1\n"},
		"diff-tree -z -c -r --name-only --no-commit-id m1": {Stdout: "SIGNAL: a.txt\x00dir/<-- FINDING: b.txt\x00"},
	})

	code, out, _ := run(t, deps, "audit", "authored", "base..tip")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "AUTHORED (identical in both parents, changed by the merge): SIGNAL: a.txt")
	assert.Contains(t, out, "dir/<-- FINDING: b.txt")
}

func TestAudit_PresentWithAddedLinesTheTipLostIsADefect(t *testing.T) {
	t.Parallel()

	deps := auditDeps(t, map[string]model.CommandResult{
		"rev-parse tip":                  {Stdout: tip + "\n"},
		"rev-list --merges base..tip":    {Stdout: "m1\n"},
		"diff --name-only -z x m1":       {Stdout: "f.txt\x00"},
		"log --oneline m1..tip -- f.txt": {Stdout: "c1 later change\n"},
		"diff x m1 -- f.txt":             {Stdout: "+++ b/f.txt\n+added\n"},
		"show tip:f.txt":                 {Stdout: "other\n"},
	})

	code, out, _ := run(t, deps, "audit", "present", "tip", "base")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "SIGNAL: 1 of 1 added lines gone, file still present: f.txt")
}
