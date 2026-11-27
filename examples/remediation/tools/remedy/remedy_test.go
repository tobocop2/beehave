package remedy_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehest/internal/model"
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

	assert.Equal(t, []string{"admit", "size", "ledger", "recount", "handoff", "authors", "spend", "report"}, remedy.Names())
}

func TestExecute_ADeletedSubcommandIsUnknown(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"scope", "papers", "audit", "job"} {
		code, out, errOut := run(t, remedy.Deps{}, name)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, `unknown command "`+name+`"`, name)
	}
}
