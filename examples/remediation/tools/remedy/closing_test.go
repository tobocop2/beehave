package remedy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/model"
)

// closingWorld is a forge that lists closing references. Its body closes nothing, so only the references can name an issue.
type closingWorld struct {
	bodyWorld
	refs       model.ClosingRefs
	closingErr error
	closingFor []int
}

// ClosingRefs answers like the forge: the issues, the base branch of the pull request and the default branch in one read.
func (w *closingWorld) ClosingRefs(_ context.Context, pr int) (model.ClosingRefs, error) {
	w.closingFor = append(w.closingFor, pr)

	return w.refs, w.closingErr
}

func closingDeps(world *closingWorld) remedy.Deps {
	deps := ledgerDeps(&world.bodyWorld)
	deps.Forge = func(string) (remedy.Forge, error) { return world, nil }

	return deps
}

// review records the first review of key 7 from pull request 7 in a new state folder, and gives the exit code and the two outputs.
func review(t *testing.T, deps remedy.Deps) (int, string, string) {
	t.Helper()

	return run(t, deps, "ledger", "add", "7", "rev-1", "review", "--pr", "7", "--state", t.TempDir())
}

func TestClosing_APullRequestIntoTheDefaultBranchRecordsTheListOfTheForgeFromOneCall(t *testing.T) {
	t.Parallel()

	world := &closingWorld{refs: model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{12, 13}}, bodyWorld: bodyWorld{body: "Closes #9"}}

	code, out, errOut := review(t, closingDeps(world))

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "12\n13\n", out, "the sidebar-linked issue is recorded and the body is not")
	assert.Equal(t, []int{7}, world.closingFor, "one read of the forge")
	assert.Empty(t, world.asked, "the body is not read")
}

func TestClosing_AnEmptyListForTheDefaultBranchIsTrustedAsEmpty(t *testing.T) {
	t.Parallel()

	world := &closingWorld{refs: model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{}}, bodyWorld: bodyWorld{body: "Closes #9"}}

	code, out, errOut := review(t, closingDeps(world))

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Empty(t, out)
}

func TestClosing_APullRequestIntoAnotherBranchRecordsTheClosingKeywordsOfTheBody(t *testing.T) {
	t.Parallel()

	world := &closingWorld{refs: model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{99}}, bodyWorld: bodyWorld{body: "Closes #9"}}

	code, out, errOut := review(t, closingDeps(world))

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "9\n", out)
	assert.Equal(t, []int{7}, world.closingFor, "one read of the forge, which tells the base branch")
}

func TestClosing_AForgeWithNoClosingReferencesRecordsTheClosingKeywordsOfTheBody(t *testing.T) {
	t.Parallel()

	code, out, errOut := review(t, ledgerDeps(&bodyWorld{body: "Fixes #4"}))

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "4\n", out)
}

func TestClosing_EveryFailedReadIsARefusalWithItsOwnWords(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		world *closingWorld
		want  string
		cause error
	}{
		"the read fails": {
			world: &closingWorld{closingErr: errDown},
			want:  "cannot read the issues that pull request 7 closes.",
			cause: errDown,
		},
		"the base is not given": {
			world: &closingWorld{refs: model.ClosingRefs{DefaultBranch: "main"}},
			want:  "cannot read the base branch of pull request 7.",
		},
		"the default branch is not given": {
			world: &closingWorld{refs: model.ClosingRefs{BaseBranch: "main"}},
			want:  "cannot read the default branch of the repository of pull request 7.",
		},
		"the body of a pull request into another branch cannot be read": {
			world: &closingWorld{bodyWorld: bodyWorld{bodyErr: errDown}, refs: model.ClosingRefs{BaseBranch: "x", DefaultBranch: "main"}},
			want:  "cannot read the body of pull request 7.",
			cause: errDown,
		},
		"the body names an issue number above the range of int": {
			world: &closingWorld{bodyWorld: bodyWorld{body: "Closes #99999999999999999999999"}, refs: model.ClosingRefs{BaseBranch: "x", DefaultBranch: "main"}},
			want:  "the body of pull request 7 names the issue number 99999999999999999999999, which is too large.",
		},
	}

	for name, tt := range tests {
		code, out, errOut := review(t, closingDeps(tt.world))

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, tt.want, name)

		if tt.cause != nil {
			assert.Contains(t, errOut, tt.cause.Error(), name)
		}
	}
}
