package remedy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/model"
)

// integrationWorld is a forge whose pull request has the title and body of an integration pull request.
type integrationWorld struct {
	forgeWorld
	title, body string
}

func (w *integrationWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{Title: w.title, Body: w.body}, nil
}

const integrationBody = "## Pull requests merged into this branch (2 pull requests)\n\n| PR | Change |\n|---|---|\n| #10 | a |\n| #11 | b |\n\n- Closes #5\n"

func recountDeps(world *integrationWorld) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return world, nil },
	}
}

func TestRecount_PrintsTheCountsOfTheRowsAndPassesWhenTheTitleAndHeadingAgree(t *testing.T) {
	t.Parallel()

	world := &integrationWorld{title: "Remediation round IV: 2 reviewed pull requests, 1 issues", body: integrationBody}

	code, out, errOut := run(t, recountDeps(world), "recount", "--pr", "9")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "rows=2 issues=1\n", out)
	assert.Empty(t, errOut)
}

func TestRecount_ATitleThatSaysAnotherNumberExitsOne(t *testing.T) {
	t.Parallel()

	world := &integrationWorld{title: "Remediation round IV: 1 reviewed pull request, 1 issues", body: integrationBody}

	code, out, _ := run(t, recountDeps(world), "recount", "--pr", "9")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "title says 1 reviewed pull request")
}

func TestRecount_ACountThatDidNotChangeSinceTheWriteExitsOne(t *testing.T) {
	t.Parallel()

	world := &integrationWorld{title: "a person's title", body: integrationBody}

	code, out, _ := run(t, recountDeps(world), "recount", "--pr", "9", "--was-rows", "2")
	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "the row count is still 2")

	code, _, _ = run(t, recountDeps(world), "recount", "--pr", "9", "--was-rows", "1", "--was-issues", "0")
	assert.Equal(t, remedy.ExitPass, code)
}

func TestRecount_ANegativeWasValueIsARefusalNotAnAssertThatIsOff(t *testing.T) {
	t.Parallel()

	world := &integrationWorld{title: "a person's title", body: integrationBody}

	for _, flag := range []string{"--was-rows", "--was-issues"} {
		code, _, errOut := run(t, recountDeps(world), "recount", "--pr", "9", flag, "-1")
		assert.Equal(t, remedy.ExitRefused, code, flag)
		assert.Contains(t, errOut, "usage: remedy recount", flag)
	}
}

func TestRecount_ABadFlagOrAMissingPullRequestIsARefusal(t *testing.T) {
	t.Parallel()

	code, _, errOut := run(t, recountDeps(&integrationWorld{}), "recount")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "usage: remedy recount")

	code, _, _ = run(t, remedy.Deps{Git: origin("nonsense", nothing)}, "recount", "--pr", "9")
	assert.Equal(t, remedy.ExitRefused, code)

	deps := recountDeps(&integrationWorld{})
	deps.Forge = func(string) (remedy.Forge, error) { return nil, assert.AnError }
	code, _, _ = run(t, deps, "recount", "--pr", "9")
	assert.Equal(t, remedy.ExitRefused, code, "a forge that cannot open is a refusal")
}
