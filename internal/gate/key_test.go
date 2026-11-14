package gate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func keyOf(t *testing.T, state gate.State, shell *fakeShell, p model.Project) gate.Key {
	t.Helper()

	key, err := build(shell, &fakeTree{states: []gate.State{state}}, &fakeRemote{}).Key(context.Background(), gate.Request{Project: p, Dir: "/w"})
	require.NoError(t, err)

	return key
}

func TestKey_TheSameTreeCommandAndPinsGiveTheSameKeyEvenOnAnotherCommit(t *testing.T) {
	t.Parallel()

	first := keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{}, project("local"))
	second := keyOf(t, gate.State{Head: headB, Tree: "t1", Clean: true}, &fakeShell{}, project("local"))

	assert.NotEmpty(t, first.ID)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, headB, second.Head)
	assert.Equal(t, "t1", second.Tree)
}

func TestKey_ADifferentTreeCommandReportHostOrPinFileGivesAnotherKey(t *testing.T) {
	t.Parallel()

	base := project("local")
	base.PinFile = ".pins"
	state := gate.State{Head: headA, Tree: "t1", Clean: true}
	pinned := &fakeShell{output: "go 1.27.3\n"}
	want := keyOf(t, state, pinned, base).ID

	command, report, host, file := base, base, base, base
	command.Gate = "make other"
	report.TestReport = "out/*.xml"
	host.BuildHost = "cluster"
	file.PinFile = ".other"

	assert.NotEqual(t, want, keyOf(t, gate.State{Head: headA, Tree: "t2", Clean: true}, pinned, base).ID)
	assert.NotEqual(t, want, keyOf(t, state, &fakeShell{output: "go 1.27.4\n"}, base).ID)

	for _, other := range []model.Project{command, report, host, file} {
		assert.NotEqual(t, want, keyOf(t, state, pinned, other).ID)
	}
}

func TestKey_ATreeWithChangesOrAnUnreadablePinFileGivesNoKey(t *testing.T) {
	t.Parallel()

	assert.Empty(t, keyOf(t, gate.State{Head: headA, Tree: "t1"}, &fakeShell{}, project("local")).ID)

	pinned := project("local")
	pinned.PinFile = ".pins"

	assert.Empty(t, keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{code: 128}, pinned).ID)
}

func TestKey_ATreeThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	_, err := build(&fakeShell{}, &fakeTree{err: errors.New("no git")}, &fakeRemote{}).Key(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})

	require.ErrorContains(t, err, "no git")
}

// versions answers a `git show` with the pin file and any other command with the version text of the installed tool.
func versions(pins, installed string) func(context.Context, string, string) (string, int) {
	return func(_ context.Context, _, command string) (string, int) {
		if strings.HasPrefix(command, "git show") {
			return pins, 0
		}

		return installed, 0
	}
}

func keyWith(t *testing.T, shell func(context.Context, string, string) (string, int), p model.Project) gate.Key {
	t.Helper()

	state := gate.State{Head: headA, Tree: "t1", Clean: true}
	remote := &fakeRemote{}

	key, err := gate.New(shell, (&fakeTree{states: []gate.State{state}}).read, func(model.Project) (gate.Remote, error) { return remote, nil }).Key(context.Background(), gate.Request{Project: p, Dir: "/w"})
	require.NoError(t, err)

	return key
}

func TestKey_AnotherInstalledToolVersionGivesAnotherKey(t *testing.T) {
	t.Parallel()

	pinned := project("local")
	pinned.PinFile = ".pins"

	linux := keyWith(t, versions("go 1.27.3\n", "go1.27.3 linux\n"), pinned)
	darwin := keyWith(t, versions("go 1.27.3\n", "go1.27.3 darwin\n"), pinned)

	assert.NotEmpty(t, linux.ID)
	assert.NotEmpty(t, darwin.ID)
	assert.NotEqual(t, linux.ID, darwin.ID)
}

func TestKey_AToolThatIsNotAtItsPinnedVersionGivesNoKeySoTheGateRunsAndVoids(t *testing.T) {
	t.Parallel()

	pinned := project("local")
	pinned.PinFile = ".pins"

	assert.Empty(t, keyWith(t, versions("go 1.27.3\n", "go1.27.4\n"), pinned).ID)
}

func TestKey_APinFileThatCannotBeParsedGivesNoKey(t *testing.T) {
	t.Parallel()

	pinned := project("local")
	pinned.PinFile = ".pins"

	assert.Empty(t, keyWith(t, versions("go\n", "go1.27.3\n"), pinned).ID)
}

func TestKey_ABuildHostGateDoesNotReadInstalledTools(t *testing.T) {
	t.Parallel()

	pinned := project("cluster")
	pinned.PinFile = ".pins"

	asked := false
	shell := func(_ context.Context, _, command string) (string, int) {
		asked = asked || !strings.HasPrefix(command, "git show")

		return "go 1.27.3\n", 0
	}

	assert.NotEmpty(t, keyWith(t, shell, pinned).ID)
	assert.False(t, asked)
}

func TestKey_ADifferentProjectNameRepoOrHostDefinitionGivesAnotherKey(t *testing.T) {
	t.Parallel()

	base := project("cluster")
	base.Host = &model.HostConfig{Provider: "skypilot", Block: skyBlock("p.star:5:9", "n2")}
	want := keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{}, base).ID

	name, repo, host, none := base, base, base, base
	name.Name = "other"
	repo.Repo = "owner/other"
	host.Host = &model.HostConfig{Provider: "skypilot", Block: skyBlock("p.star:5:9", "n4")}
	none.Host = nil

	for _, other := range []model.Project{name, repo, host, none} {
		assert.NotEqual(t, want, keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{}, other).ID)
	}
}

func TestKey_TheHostBlockIsPartOfTheKeyByItsKeysAndNotByItsPosition(t *testing.T) {
	t.Parallel()

	base := project("cluster")
	base.Host = &model.HostConfig{Provider: "skypilot", Block: skyBlock("p.star:5:9", "n2")}
	want := keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{}, base).ID

	moved := base
	moved.Host = &model.HostConfig{Provider: "skypilot", Block: skyBlock("p.star:40:2", "n2")}

	assert.Equal(t, want, keyOf(t, gate.State{Head: headA, Tree: "t1", Clean: true}, &fakeShell{}, moved).ID, "the same block at another line is the same host")
}

// skyBlock returns a skypilot block as the loader holds it, with the position of its call.
func skyBlock(at, instanceType string) *model.Block {
	return model.NewBlock(at, []model.BlockEntry{model.TextEntry("cloud", "gcp"), model.TextEntry("instance_type", instanceType)})
}
