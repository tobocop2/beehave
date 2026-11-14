package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

const localProjectConfig = "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"

var errNoItem = store.Refuse("no item fix-login in project p. Add it: beehave item add")

// fakeItems is the item list of one local project. It records what the commands do.
type fakeItems struct {
	added    []string
	items    []model.Item
	addErr   error
	itemErr  error
	listErr  error
	closed   int
	projects []string
}

func (f *fakeItems) Add(_ context.Context, id, title, body string) error {
	f.added = append(f.added, id+"|"+title+"|"+body)

	return f.addErr
}

func (f *fakeItems) Item(context.Context, string) (map[string]string, error) {
	return nil, f.itemErr
}

func (f *fakeItems) OpenItems(context.Context) ([]model.Item, error) { return f.items, f.listErr }

func (f *fakeItems) Close() error {
	f.closed++

	return nil
}

// itemRig is a config folder with one local project p and a fake item list.
type itemRig struct {
	*flowRig

	items    *fakeItems
	openErr  error
	opened   int
	bodyFile string
}

func newItemRig(t *testing.T) *itemRig {
	t.Helper()

	rig := &itemRig{flowRig: newRig(t), items: &fakeItems{}, bodyFile: filepath.Join(t.TempDir(), "body.md")}
	require.NoError(t, os.WriteFile(rig.bodyFile, []byte("Fix the login form.\n"), 0o600))
	writeConfig(t, rig.dir, "projects/p.star", localProjectConfig)

	return rig
}

func (rig *itemRig) run(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	info := cli.BuildInfo{
		Version:  "v9",
		Registry: registry(),
		Flow:     rig.factory(),
		Items: map[model.TrackerName]cli.ItemOpener{model.TrackerLocal: func(_ context.Context, project model.Project) (cli.ItemStore, error) {
			rig.opened++
			rig.items.projects = append(rig.items.projects, project.Name)

			if rig.openErr != nil {
				return nil, rig.openErr
			}

			return rig.items, nil
		}},
	}
	code := cli.Execute(ctx, append(args, "--config", rig.dir), info, rig.env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestItemAdd_FilesAnOpenItemWithTheBodyOfTheFile(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)

	code, stdout, stderr := rig.run(t.Context(), "item", "add", "p", "fix-login", "--title", "Fix login", "--body-file", rig.bodyFile)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "added item fix-login to project p\n", stdout)
	assert.Equal(t, []string{"fix-login|Fix login|Fix the login form.\n"}, rig.items.added)
	assert.Equal(t, []string{"p"}, rig.items.projects)
	assert.Equal(t, 1, rig.items.closed)
}

func TestItemAdd_RefusesAProjectWhoseTrackerIsNotLocalAndOpensNoStore(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	writeConfig(t, rig.dir, "projects/q.star", validProject)

	code, _, stderr := rig.run(t.Context(), "item", "add", "q", "fix-login", "--title", "t", "--body-file", rig.bodyFile)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "project q has the github tracker. beehave item works only on a project with a tracker that holds a local list")
	assert.Zero(t, rig.opened)

	code, _, stderr = rig.run(t.Context(), "item", "list", "q")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "project q has the github tracker")
	assert.Zero(t, rig.opened)
}

func TestItemAdd_RefusesWhatItCannotFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		prepare func(*itemRig)
		want    string
	}{
		{"unknown project", []string{"zzz", "fix-login"}, nil, "no project \"zzz\" in the config"},
		{"bad item id", []string{"p", "fix login"}, nil, "use an item id"},
		{"missing body file", []string{"p", "fix-login", "--body-file", "/nowhere/body.md"}, nil, "read the body file"},
		{"store cannot open", []string{"p", "fix-login"}, func(r *itemRig) { r.openErr = errFake }, "fake failure"},
		{"add fails", []string{"p", "fix-login"}, func(r *itemRig) { r.items.addErr = errFake }, "fake failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newItemRig(t)
			if test.prepare != nil {
				test.prepare(rig)
			}

			args := append([]string{"item", "add"}, test.args...)
			if !slices.Contains(args, "--body-file") {
				args = append(args, "--body-file", rig.bodyFile)
			}

			code, stdout, stderr := rig.run(t.Context(), append(args, "--title", "t")...)

			assert.Equal(t, 1, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, test.want)
		})
	}
}

func TestItem_AConfigThatDoesNotLoadIsRefusedBeforeAnyStoreOpens(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	writeConfig(t, rig.dir, "projects/bad.star", "project(unknown_key = 1)\n")

	code, _, stderr := rig.run(t.Context(), "item", "list", "p")

	assert.Equal(t, 2, code, "2 is the exit code of a config with problems")
	assert.NotEmpty(t, stderr)
	assert.Zero(t, rig.opened)
}

func TestItemAdd_NeedsATitleAndABodyFile(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)

	code, _, stderr := rig.run(t.Context(), "item", "add", "p", "fix-login", "--body-file", rig.bodyFile)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "title")

	code, _, stderr = rig.run(t.Context(), "item", "add", "p", "fix-login", "--title", "t")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "body-file")
	assert.Zero(t, rig.opened)
}

func TestItemList_ShowsTheOpenItemsOneToALine(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	rig.items.items = []model.Item{{ID: "fix-login", Title: "Fix login"}, {ID: "7", Title: "Add logout"}}

	code, stdout, stderr := rig.run(t.Context(), "item", "list", "p")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "fix-login\tFix login\n7\tAdd logout\n", stdout)
	assert.Equal(t, 1, rig.items.closed)
}

func TestItemList_SaysSoWhenTheProjectHasNoItemAndReportsAFailedRead(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)

	code, stdout, stderr := rig.run(t.Context(), "item", "list", "p")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "no items in project p\n", stdout)

	rig.items.listErr = errFake
	code, _, stderr = rig.run(t.Context(), "item", "list", "p")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")

	rig.openErr = errFake
	code, _, stderr = rig.run(t.Context(), "item", "list", "p")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")

	code, _, stderr = rig.run(t.Context(), "item", "list", "zzz")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "no project \"zzz\" in the config")
}

func TestRun_AnItemThatALocalProjectNeverAddedIsRefusedBeforeTheEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	rig.items.itemErr = errNoItem

	code, _, stderr := rig.run(t.Context(), "run", "fix-login", "p", "--workflow", rig.file)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "no item fix-login in project p. Add it: beehave item add")
	assert.Zero(t, rig.engines, "no engine opens and no claim is taken")
	assert.Equal(t, 1, rig.items.closed)
}

func TestRun_AnItemThatALocalProjectHasRuns(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)

	code, stdout, stderr := rig.run(t.Context(), "run", "fix-login", "p", "--workflow", rig.file)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item fix-login finished\n", stdout)
	assert.Equal(t, []string{"fix-login@p"}, rig.runner.items)
}

func TestRun_ALocalStoreThatCannotOpenRefusesTheRun(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	rig.openErr = errors.New("cannot open local.db")

	code, _, stderr := rig.run(t.Context(), "run", "fix-login", "p", "--workflow", rig.file)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "cannot open local.db")
	assert.Zero(t, rig.engines)
}

func TestRun_AProjectWithAnotherTrackerNeverOpensTheLocalStore(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)
	writeConfig(t, rig.dir, "projects/q.star", validProject)

	code, _, stderr := rig.run(t.Context(), "run", "12", "q", "--workflow", rig.file)

	require.Equal(t, 0, code, stderr)
	assert.Zero(t, rig.opened)
}

func TestRun_ABuildWithNoItemStoreRunsAsBefore(t *testing.T) {
	t.Parallel()

	rig := newItemRig(t)

	code, _, stderr := rig.execute(t.Context(), "run", "12", "p")

	require.Equal(t, 0, code, stderr)
}
