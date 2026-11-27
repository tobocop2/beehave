package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
)

// statusShip is the forge fake of the merge scripts that also posts statuses, into the same list of writes, so a test sees the post and the merge in one order.
type statusShip struct {
	*shipForge

	postErr error
	// protection is what the base of the fake forge requires.
	protection model.Protection
}

func (f statusShip) PostStatus(_ context.Context, status model.CommitStatus) error {
	if f.postErr != nil {
		return f.postErr
	}

	f.writes = append(f.writes, fmt.Sprintf("PostStatus %s %s %s", status.Context, status.State, status.SHA))

	return nil
}

func (f statusShip) Protection(context.Context, string) (model.Protection, error) {
	return f.protection, nil
}

var gateStatus = model.CommitStatus{SHA: gated, Context: "beehest/gate", State: model.StatusSuccess, Description: "d"}

// statusMerge is the merge of the shipping fixture with the gate status in its settings, over a project with or without a writer role.
func statusMerge(t *testing.T, withWriter bool, over map[string]any, postErr error) (pulls, *shipForge, flow.MergeSettings) {
	t.Helper()

	p, f, settings := shipping(t, over)
	settings.Statuses = []model.CommitStatus{gateStatus}

	project := p.projects["p"]
	protection := model.Protection{}

	if withWriter {
		project.Credentials = map[model.Role]model.Credential{model.RoleWriter: {}}
		protection = model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextGate, model.ContextReview}}
	}

	p.projects = map[string]config.Project{"p": project}
	p.forge = func(model.Project) (forge.Forge, error) {
		return statusShip{shipForge: f, postErr: postErr, protection: protection}, nil
	}

	return p, f, settings
}

func TestMerge_TheWriterPostsTheStatusesOfTheSettingsRightBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	p, f, settings := statusMerge(t, true, nil, nil)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, result.Lines)
	assert.Equal(t, []string{"PostStatus beehest/gate success " + gated, "MergePR 5 " + gated}, f.writes)
}

func TestMerge_APullRequestThatAGuardRefusesGetsNoStatusFromTheHost(t *testing.T) {
	t.Parallel()

	p, f, settings := statusMerge(t, true, map[string]any{"labels": []any{map[string]any{"name": "hold"}}}, nil)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, f.writes, "a refused pull request carries no status")
}

func TestMerge_AProjectWithNoWriterRolePostsNoStatusWarnsAndMerges(t *testing.T) {
	t.Parallel()

	p, f, settings := statusMerge(t, false, nil, nil)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, result.Lines, "WARN: "+noWriterNote)
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes)
}

func TestMerge_AStatusThatTheForgeRefusesStopsTheMergeBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	p, f, settings := statusMerge(t, true, nil, errFake)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Transient, result.Reason)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "post the status beehest/gate on "+gated)
	assert.Empty(t, f.writes, "no merge call without the statuses")
}

func TestStatusPost_AForgeThatCannotPostStatusesSaysSo(t *testing.T) {
	t.Parallel()

	project := model.Project{Credentials: map[model.Role]model.Credential{model.RoleWriter: {}}}

	skipped, err := posting(project, hostForge{}, []model.CommitStatus{gateStatus}).Post(t.Context())

	require.NoError(t, err)
	assert.Equal(t, noStatusNote, skipped)
}

func TestStatusPost_NoStatusesIsNoPoster(t *testing.T) {
	t.Parallel()

	assert.Nil(t, posting(model.Project{}, hostForge{}, nil))
}

func TestNewGithubForge_PostsAStatusThroughTheWriterRunnerWithItsTokenAndItsOwnConfigFolder(t *testing.T) {
	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	script := "#!/bin/sh\nprintf '%s|%s' \"$GH_TOKEN\" \"$GH_CONFIG_DIR\" > " + seen + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "gh"), script))
	t.Setenv("PATH", bin+":"+testPath) // The runner finds gh on the PATH of the engine process, so the stand-in goes first there.

	roles := &writers{byProject: map[string]writerRole{"p": {token: writerToken, configDir: "/role/gh"}}, env: map[string]string{"PATH": bin + ":" + testPath, "HOME": "/owner"}}
	project := model.Project{Name: "p", Repo: "o/r", Credentials: map[model.Role]model.Credential{model.RoleWriter: {}}}

	built, err := newGithubForge(roles, project)
	require.NoError(t, err)

	poster, ok := built.(forge.Statuses)
	require.True(t, ok, "the GitHub forge posts statuses")
	require.NoError(t, poster.PostStatus(t.Context(), model.CommitStatus{SHA: gated, Context: "beehest/gate", State: model.StatusSuccess, Description: "d"}))

	got, err := os.ReadFile(seen) //nolint:gosec // the file the stand-in for gh wrote in the folder of this test
	require.NoError(t, err)
	assert.Equal(t, writerToken+"|/role/gh", string(got), "gh ran with the token of the writer and the config folder of the role, not the login of the owner")
}
