package flow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

var (
	errUndeclared = errors.New(`the project declares no build host with credentials = "none"`)
	errLocal      = errors.New("the project has no build host")
)

// originReads is a preflight reader that answers who wrote the item: the answers are taken in turn, and the last one repeats.
type originReads struct {
	*fakePreflight

	mu      sync.Mutex
	answers []originAnswer
	asked   []string
}

type originAnswer struct {
	origin model.Origin
	err    error
}

func (o *originReads) Origin(_ context.Context, _, item string) (model.Origin, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.asked = append(o.asked, item)
	answer := o.answers[min(len(o.asked), len(o.answers))-1]

	return answer.origin, answer.err
}

func (o *originReads) items() []string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return append([]string(nil), o.asked...)
}

// checkedHosts is a host keeper whose credential check gives check.
type checkedHosts struct {
	failingHosts

	check error
}

func (c checkedHosts) CheckCredentials(context.Context, string) error { return c.check }

// forgeFacts gives every project a forge, so the run reads who wrote its item.
func forgeFacts(string) model.Project { return model.Project{Forge: "github"} }

// originOf is the answer of the forge for a cell: the association of the issue or of the pull request of the item. UNKNOWN is a value
// the forge does not list, which the reader refuses, and READ_FAILS is a read that fails.
func originOf(source, association string) originAnswer {
	parsed, err := model.ParseAssociation(association)

	switch {
	case association == "READ_FAILS":
		return originAnswer{err: errFake}
	case err != nil:
		return originAnswer{err: err}
	case source == "issue":
		return originAnswer{origin: model.Origin{Issue: model.Authored{Number: 12, Association: parsed}}}
	}

	return originAnswer{origin: model.Origin{Issue: model.Authored{Number: 12, Association: model.AssociationOwner}, Pull: model.Authored{Number: testPull, Association: parsed}}}
}

// foreignCell is one start of the table of a foreign item: who wrote which part, the sandbox mode of the run, and the answer of the host check.
type foreignCell struct {
	source, association, mode, host string
}

func (c foreignCell) String() string {
	return fmt.Sprintf("%s %s %s %s", c.source, c.association, c.mode, c.host)
}

// started says whether the run of the cell starts: the owner wrote the item, or the run is in the sandbox on a host that passes the check.
func (c foreignCell) started() bool {
	return c.association == "OWNER" || (c.mode == "on" && c.host == "none-declared")
}

// foreignCells are the 120 cells: two parts, the eight values of the forge with an unknown value and a failed read, two modes, and three hosts.
func foreignCells() []foreignCell {
	var cells []foreignCell

	for _, source := range []string{"issue", "pull"} {
		for _, association := range []string{"OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "MANNEQUIN", "NONE", "UNKNOWN", "READ_FAILS"} {
			for _, mode := range []string{"on", "off"} {
				for _, host := range []string{"none-declared", "undeclared", "local"} {
					cells = append(cells, foreignCell{source: source, association: association, mode: mode, host: host})
				}
			}
		}
	}

	return cells
}

// runCell runs an agent flow for the cell and checks that it started with a claim and an agent, or was refused before both.
func runCell(t *testing.T, cell foreignCell) {
	t.Helper()

	rig := newAgentRig(t)
	rig.sandbox = map[string]model.SandboxMode{"on": model.SandboxOn, "off": model.SandboxOff}[cell.mode]
	rig.h.facts = forgeFacts
	rig.h.preflight = &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf(cell.source, cell.association)}}
	rig.h.hosts = checkedHosts{check: map[string]error{"none-declared": nil, "undeclared": errUndeclared, "local": errLocal}[cell.host]}
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(""), "x")

	got := map[bool]string{true: "started", false: "refused"}[err == nil]
	t.Logf("CELL %s %s", cell, got)

	if cell.started() {
		require.NoError(t, err)
		assert.Contains(t, rig.h.life.seen(), "claim:"+testItem)
		assert.Len(t, rig.h.agents.seen(), 1)

		return
	}

	require.ErrorContains(t, err, "item "+testItem+" was not started")
	require.ErrorContains(t, err, `A foreign item runs only in the sandbox on a build host with credentials = "none"`)
	require.ErrorContains(t, err, "beehave run "+testItem)
	assert.NotContains(t, rig.h.life.seen(), "claim:"+testItem, "the refusal comes before the claim")
	assert.Empty(t, rig.h.agents.seen(), "no agent step starts")
}

func TestStart_AForeignItem(t *testing.T) {
	t.Parallel()

	cells := foreignCells()
	require.Len(t, cells, 120)

	for _, cell := range cells {
		t.Run(cell.String(), func(t *testing.T) {
			t.Parallel()

			runCell(t, cell)
		})
	}
}

func TestStart_TheRefusalOfAForeignItemSaysWhichPartIsForeignAndWhatToChange(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		answer originAnswer
		mode   model.SandboxMode
		check  error
		want   []string
	}{
		"a foreign issue in a run with the sandbox off": {
			answer: originOf("issue", "CONTRIBUTOR"), mode: model.SandboxOff,
			want: []string{"its issue #12 comes from another account (CONTRIBUTOR)", `Remove sandbox = "off" from the project`},
		},
		"a foreign pull request on a host that fails its check": {
			answer: originOf("pull", "NONE"), mode: model.SandboxOn, check: errUndeclared,
			want: []string{fmt.Sprintf("its pull request #%d comes from another account (NONE)", testPull), `Declare the build host with credentials = "none" (` + errUndeclared.Error() + ")"},
		},
		"a read that fails": {
			answer: originOf("issue", "READ_FAILS"), mode: model.SandboxOff,
			want: []string{"beehave cannot read who wrote it (" + errFake.Error() + ")"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			h.preflight = &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{test.answer}}
			h.hosts = checkedHosts{check: test.check}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("pass\n")), RunSandbox(test.mode))

			for _, want := range test.want {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestStart_ABuildThatCannotReadOrCheckRefusesAForeignItem(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		preflight bool
		hosts     bool
		mode      model.SandboxMode
		want      string
	}{
		"no reader of the forge, in a run with the sandbox off": {hosts: true, mode: model.SandboxOff, want: "beehave cannot read who wrote it (" + errNoPreflight.Error() + ")"},
		"no host keeper": {preflight: true, mode: model.SandboxOn, want: "Use a build that can check the build host"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts

			if test.preflight {
				h.preflight = &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("issue", "NONE")}}
			}

			if test.hosts {
				h.hosts = checkedHosts{}
			}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("pass\n")), RunSandbox(test.mode))

			require.ErrorContains(t, err, test.want)
			assert.NotContains(t, h.life.seen(), "claim:"+testItem)
		})
	}
}

func TestStart_AProjectWithNoForgeReadsNoAuthor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("issue", "NONE")}}
	h.preflight = reads

	_, err := walkBody(t, h, "pass\n")

	require.NoError(t, err)
	assert.Empty(t, reads.items(), "a project with no forge has no author to read")
	assert.Contains(t, h.life.seen(), "claim:"+testItem)
}

func TestStart_TheReadOfWhoWroteTheItemIsARecordedStepBeforeTheClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = forgeFacts
	h.preflight = &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("issue", "OWNER")}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("pass\n")), modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(steps), 2)
	assert.Equal(t, originStepName, steps[0].StepName, "the read comes first, so a refused item takes no claim")
	assert.Equal(t, "claim", steps[1].StepName)
}

func TestStart_ALaterReadOfThePullRequestOfTheItemRefusesAnItemThatTurnedForeign(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"pr_read":   "pr_read()\n",
		"preflight": "preflight()\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("pull", "OWNER"), originOf("pull", "CONTRIBUTOR")}}
			h.preflight = reads

			_, err := walkBody(t, h, body)

			require.ErrorContains(t, err, "item "+testItem+" stopped")
			require.ErrorContains(t, err, fmt.Sprintf("its pull request #%d comes from another account (CONTRIBUTOR)", testPull))
			assert.Equal(t, []string{testItem, testItem}, reads.items(), "the start and the later read each ask who wrote the item")
		})
	}
}

func TestStart_ALaterReadOfAnotherItemDoesNotAskWhoWroteTheItemOfTheRun(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"pr_read":   "pr_read(id = \"other\")\n",
		"preflight": "preflight(id = \"other\")\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("pull", "OWNER"), originOf("pull", "CONTRIBUTOR")}}
			h.preflight = reads

			_, err := walkBody(t, h, body)

			require.NoError(t, err)
			assert.Equal(t, []string{testItem}, reads.items(), "only the start asks: the read names another item")
		})
	}
}

func TestStart_ALaterReadOfTheItemOfTheRunByItsIdAsksAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = forgeFacts
	reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("pull", "OWNER")}}
	h.preflight = reads

	_, err := walkBody(t, h, "pr_read(id = \""+testItem+"\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{testItem, testItem}, reads.items())
}

func TestDryPreflight_FindsAnItemThatTheOwnerWrote(t *testing.T) {
	t.Parallel()

	origin, err := dryPreflight{}.Origin(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.False(t, origin.Issue.Foreign())
	assert.False(t, origin.Pull.Foreign())
}
