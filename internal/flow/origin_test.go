package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

var (
	errUndeclared = errors.New(`the project declares no build host with credentials = "none"`)
	errLocal      = errors.New("the project has no build host")
)

// originReads is a preflight reader that answers who wrote the item. The issue comes from the first answer. Each read of the pull request, by ReadPull
// or by Preflight, takes the next answer, and the last one repeats, so a lookup can find another pull request on a later call.
// As the reader of the build does, it reads the author of the pull request only for a caller that asks for it, and authors lists those reads.
type originReads struct {
	*fakePreflight

	mu      sync.Mutex
	answers []originAnswer
	asked   []string
	authors []string
	pulls   int
}

// originAnswer is one answer of the forge: the issue and the pull request of the item with their authors, and the error of each read.
// authorErr fails the read of the author of the pull request alone, so only a caller that asks for the author sees it.
type originAnswer struct {
	origin    model.Origin
	issueErr  error
	pullErr   error
	authorErr error
}

func (o *originReads) IssueAuthor(_ context.Context, _, item string) (model.Authored, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.asked = append(o.asked, "issue "+item)

	return o.answers[0].origin.Issue, o.answers[0].issueErr
}

// nextPull takes the answer of the next read of the pull request: the pull request that the lookup finds, and the error of the read.
// A caller that asks for no author gets the number alone, and no read of the author is made or fails.
func (o *originReads) nextPull(kind, item string, author bool) (model.Authored, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.asked = append(o.asked, kind+" "+item)
	o.pulls++

	answer := o.answers[min(o.pulls, len(o.answers))-1]
	if !author {
		return model.Authored{Number: answer.origin.Pull.Number}, answer.pullErr
	}

	o.authors = append(o.authors, kind+" "+item)

	return answer.origin.Pull, cmp.Or(answer.pullErr, answer.authorErr)
}

func (o *originReads) ReadPull(_ context.Context, _, item string, author bool) (model.PullFacts, error) {
	pull, err := o.nextPull("pull", item, author)

	return model.PullFacts{Number: pull.Number, Association: pull.Association}, err
}

func (o *originReads) Preflight(ctx context.Context, project, item string, req preflight.Request, author bool) (preflight.Result, error) {
	pull, pullErr := o.nextPull("preflight", item, author)

	result, err := o.fakePreflight.Preflight(ctx, project, item, req, author)
	result.Pull = pull

	return result, cmp.Or(pullErr, err)
}

func (o *originReads) items() []string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return append([]string(nil), o.asked...)
}

// authorReads lists the reads that asked who wrote the pull request.
func (o *originReads) authorReads() []string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return append([]string(nil), o.authors...)
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
	if association == "READ_FAILS" {
		err = errFake
	}

	if source == "issue" {
		return originAnswer{origin: model.Origin{Issue: model.Authored{Number: 12, Association: parsed}}, issueErr: err}
	}

	return originAnswer{origin: model.Origin{Issue: model.Authored{Number: 12, Association: model.AssociationOwner}, Pull: model.Authored{Number: testPull, Association: parsed}}, pullErr: err}
}

// lookupFinds is the answer of a forge whose lookup finds the pull request with the number, written by an account with the association.
func lookupFinds(number int, association model.Association) originAnswer {
	return originAnswer{origin: model.Origin{Pull: model.Authored{Number: number, Association: association}}}
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
			want: []string{fmt.Sprintf("its pull request #%d comes from another account (NONE)", testPull), "Fix the build host: " + errUndeclared.Error() + ", then run"},
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

func TestStart_AProjectWithNoForgeReadsTheIssueAndNoPullRequest(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		association string
		mode        model.SandboxMode
		started     bool
	}{
		"a foreign issue with the sandbox off":                {association: "CONTRIBUTOR", mode: model.SandboxOff},
		"a foreign issue in the sandbox with no checked host": {association: "NONE", mode: model.SandboxOn},
		"an issue that cannot be read":                        {association: "READ_FAILS", mode: model.SandboxOff},
		"the owner's issue":                                   {association: "OWNER", mode: model.SandboxOff, started: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("issue", test.association)}}
			h.preflight = reads
			h.hosts = checkedHosts{check: errUndeclared}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("pass\n")), RunSandbox(test.mode))

			assert.Equal(t, []string{"issue " + testItem}, reads.items(), "a project with no forge reads the issue and no pull request")

			if test.started {
				require.NoError(t, err)
				assert.Contains(t, h.life.seen(), "claim:"+testItem)

				return
			}

			require.ErrorContains(t, err, "item "+testItem+" was not started")
			assert.NotContains(t, h.life.seen(), "claim:"+testItem)
		})
	}
}

func TestStart_ABuildWithNoReaderStartsAProjectWithNoForge(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.preflight = nil

	_, err := walkBody(t, h, "pass\n")

	require.NoError(t, err)
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
			assert.Len(t, reads.items(), 3, "the start reads the issue and the pull request, and the later read reads the pull request once")
		})
	}
}

func TestStart_TheCheckOfALaterReadJudgesThePullRequestThatTheSameReadFound(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body, kind string
	}{
		"pr_read":   {body: "pr_read()\n", kind: "pull"},
		"preflight": {body: "preflight()\n", kind: "preflight"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			// The lookup finds the owner's pull request #31 at the start, and a foreign one, #40, at the later read.
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{lookupFinds(31, model.AssociationOwner), lookupFinds(40, model.AssociationContributor)}}
			h.preflight = reads

			_, err := walkBody(t, h, test.body)

			require.ErrorContains(t, err, "item "+testItem+" stopped: its pull request #40 comes from another account (CONTRIBUTOR)")
			assert.Equal(t, []string{"issue " + testItem, "pull " + testItem, test.kind + " " + testItem}, reads.items(),
				"each read finds the pull request once, so no second lookup can name another one")
			assert.Equal(t, []string{"pull " + testItem, test.kind + " " + testItem}, reads.authorReads(),
				"each read of the item of the run asks for the author once, on that one lookup")
		})
	}
}

func TestStart_ALaterReadOfAnotherItemReadsNoAuthorAndCannotFailOnOne(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body, kind string
	}{
		"pr_read":   {body: "pr_read(id = \"other\")\n", kind: "pull"},
		"preflight": {body: "preflight(id = \"other\")\n", kind: "preflight"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			// The other item has a foreign pull request, #88, and the read of its author fails.
			other := lookupFinds(88, model.AssociationContributor)
			other.authorErr = errFake
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{lookupFinds(31, model.AssociationOwner), other}}
			h.preflight = reads

			_, err := walkBody(t, h, test.body)

			require.NoError(t, err, "a failed read of the author of another item's pull request cannot end the run, because no such read is made")
			assert.Equal(t, []string{"issue " + testItem, "pull " + testItem, test.kind + " other"}, reads.items())
			assert.Equal(t, []string{"pull " + testItem}, reads.authorReads(), "only the start asks for an author: the one of the item of the run")
		})
	}
}

func TestStart_AFailedAuthorReadAtALaterReadOfTheItemOfTheRunEndsTheRun(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body, kind string
	}{
		"pr_read":   {body: "pr_read()\n", kind: "pull"},
		"preflight": {body: "preflight()\n", kind: "preflight"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.facts = forgeFacts
			later := lookupFinds(31, model.AssociationOwner)
			later.authorErr = errFake
			reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{lookupFinds(31, model.AssociationOwner), later}}
			h.preflight = reads

			_, err := walkBody(t, h, test.body)

			require.ErrorContains(t, err, errFake.Error())
			assert.Equal(t, []string{"pull " + testItem, test.kind + " " + testItem}, reads.authorReads())
		})
	}
}

func TestStart_ALaterReadOfAnotherItemJudgesNoAuthor(t *testing.T) {
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

			require.NoError(t, err, "the read of another item judges no author")
		})
	}
}

func TestStart_ALaterReadOfTheItemOfTheRunByItsIdJudgesItsAuthor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = forgeFacts
	reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("pull", "OWNER"), originOf("pull", "CONTRIBUTOR")}}
	h.preflight = reads

	_, err := walkBody(t, h, "pr_read(id = \""+testItem+"\")\n")

	require.ErrorContains(t, err, "item "+testItem+" stopped")
	assert.Equal(t, []string{"pull " + testItem, "pull " + testItem}, reads.authorReads(), "the read by the id of the item of the run asks for its author")
}

func TestStart_ALaterReadInAProjectWithNoForgeJudgesNoPullRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The start of a project with no forge reads no pull request, so the preflight takes the first answer: a foreign pull request.
	reads := &originReads{fakePreflight: &fakePreflight{}, answers: []originAnswer{originOf("pull", "CONTRIBUTOR")}}
	h.preflight = reads

	_, err := walkBody(t, h, "preflight()\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"issue " + testItem, "preflight " + testItem}, reads.items())
	assert.Empty(t, reads.authorReads(), "a project with no forge judges no pull request, so no read asks for an author")
}

func TestDryPreflight_FindsNoIssue(t *testing.T) {
	t.Parallel()

	issue, err := dryPreflight{}.IssueAuthor(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.Zero(t, issue)
}
