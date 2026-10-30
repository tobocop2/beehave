package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	shipBase    = "integration"
	shipBranch  = "item-12"
	shipOwner   = "me"
	shipMail    = "me@x"
	shipTipOne  = "cccccccccccccccccccccccccccccccccccccccc"
	shipTipTwo  = "dddddddddddddddddddddddddddddddddddddddd"
	shipChild   = 9
	shipHeadKey = "headRefOid"
)

// shipWorld is the forge fake of the merge step of the example. It holds one open pull request. A method it does not define panics,
// so a call the merge makes that the test did not expect fails the test.
type shipWorld struct {
	merge.Forge

	mu        sync.Mutex
	pull      model.PullRequest
	children  []model.PullRequest
	requests  []model.ChangeRequest
	threads   []model.ReviewThread
	tipReads  []string
	committer model.Committer
	checks    []model.Check
	writes    []string
}

func (w *shipWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return w.pull, nil
}

func (w *shipWorld) PullCounts(context.Context, int) (commits, files int, err error) {
	return len(w.pull.Commits), len(w.pull.Files), nil
}

func (w *shipWorld) Committers(context.Context, int) ([]model.Committer, error) {
	return []model.Committer{w.committer}, nil
}

func (w *shipWorld) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return w.children, nil
}

func (w *shipWorld) PRChecks(context.Context, int) ([]model.Check, error) {
	if w.checks != nil {
		return w.checks, nil
	}

	return []model.Check{{Name: "ci", Bucket: model.CheckPass}}, nil
}

func (w *shipWorld) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return w.requests, nil
}

func (w *shipWorld) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return w.threads, nil
}

func (*shipWorld) StackID(context.Context, int) (string, error) { return "", nil }

func (w *shipWorld) BranchRef(context.Context, string) (string, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.tipReads) == 0 {
		return shipTipOne, true, nil
	}

	next := w.tipReads[0]
	w.tipReads = w.tipReads[1:]

	return next, true, nil
}

func (w *shipWorld) SetBase(_ context.Context, pr int, base string) error {
	w.write(fmt.Sprintf("SetBase %d %s", pr, base))

	return nil
}

func (w *shipWorld) MergePR(_ context.Context, pr int, sha string) error {
	w.write(fmt.Sprintf("MergePR %d %s", pr, sha))

	return nil
}

func (w *shipWorld) write(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.writes = append(w.writes, line)
}

// shipPull decodes the pull request of the item at head, with the keys of over replaced.
func shipPull(t *testing.T, head string, over map[string]any) model.PullRequest {
	t.Helper()

	row := map[string]any{
		"number": testPull, "state": "OPEN", "isDraft": false, "baseRefName": shipBase, "headRefName": shipBranch,
		shipHeadKey: head, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": "dev"},
		"files":   []any{map[string]any{"path": "a.go"}},
		"commits": []any{map[string]any{"oid": strings.Repeat("b", 40), "messageHeadline": "fix", "authors": []any{map[string]any{"email": "dev@x", "login": "dev"}}}},
	}
	for key, value := range over {
		row[key] = value
	}

	text, err := json.Marshal(row)
	require.NoError(t, err)

	view, err := model.Decode[model.PullRequest]("test", string(text))
	require.NoError(t, err)

	return view
}

// mergerOver returns the merger of the engine: the real merge over the forge fake, with the settings that the step evaluated.
func mergerOver(world *shipWorld) func(context.Context, string, MergeSettings) (merge.Result, error) {
	return func(ctx context.Context, head string, settings MergeSettings) (merge.Result, error) {
		rules := make([]*regexp.Regexp, len(settings.ForbidText))
		for i, pattern := range settings.ForbidText {
			rules[i] = regexp.MustCompile(pattern)
		}

		return merge.Run(ctx, world, merge.Request{
			PR: testPull, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: []string{"bee-1"},
			OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved,
			Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget,
		}), nil
	}
}

// mergingExample returns the example rig with the environment of the example, MERGE set to "true", and a merger over world.
// The file of the example is the one in the repository, unchanged.
func mergingExample(t *testing.T, world func(head string) *shipWorld) (*agentRig, string, *shipWorld) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	built := world(rig.head)
	rig.h.merge = &fakeMerge{over: mergerOver(built)}
	rig.h.opts.Env = map[string]string{"REPO": "o/r", "BASE": shipBase, "OWNER_LOGIN": shipOwner, "OWNER_EMAIL": shipMail, "MERGE": "true", "EXPECT_HEAD": ""}

	return rig, file, built
}

func TestExampleMerge_TheMergeStepOfTheExampleMergesThroughTheEngineAndTheForgeFake(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		over    map[string]any
		setup   func(*shipWorld)
		refusal string
		writes  func(head string) []string
	}{
		"a clean head merges at its pin": {
			writes: func(head string) []string { return []string{fmt.Sprintf("MergePR %d %s", testPull, head)} },
		},
		"own work merges because the review step approved it": {
			over:   map[string]any{"author": map[string]any{"login": shipOwner}},
			writes: func(head string) []string { return []string{fmt.Sprintf("MergePR %d %s", testPull, head)} },
		},
		"a child of the branch moves onto the base before the merge": {
			setup: func(w *shipWorld) {
				w.children = []model.PullRequest{{Number: shipChild}}
				w.children[0].BaseRefName = ptr(shipBranch)
			},
			writes: func(head string) []string {
				return []string{fmt.Sprintf("SetBase %d %s", shipChild, shipBase), fmt.Sprintf("MergePR %d %s", testPull, head)}
			},
		},
		"a pull request with the hold label is refused": {
			over:    map[string]any{"labels": []any{map[string]any{"name": "Hold"}}},
			refusal: "has the label hold",
		},
		"a body with an attribution line is refused with its line number": {
			over:    map[string]any{"body": "a\nb\nGenerated with [Claude Code]"},
			refusal: "3:Generated with [Claude Code]",
		},
		"a standing change request is refused": {
			setup: func(w *shipWorld) {
				w.requests = []model.ChangeRequest{{Login: "rev", Commit: strings.Repeat("a", 40)}}
			},
			refusal: "rev",
		},
		"an open review thread is refused": {
			setup: func(w *shipWorld) {
				w.threads = []model.ReviewThread{{Path: ptr("a.go"), Line: ptr(4), First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: ptr("fix this")}}}
			},
			refusal: "unresolved review threads",
		},
		"a failing check is refused": {
			setup:   func(w *shipWorld) { w.checks = []model.Check{{Name: "ci", Bucket: model.CheckFail}} },
			refusal: "checks are not green: ci=fail",
		},
		"a commit with no linked account is refused": {
			over: map[string]any{"commits": []any{map[string]any{
				"oid": strings.Repeat("b", 40), "messageHeadline": "fix", "authors": []any{map[string]any{"email": "dev@x"}},
			}}},
			refusal: "have no linked account",
		},
		"a closed pull request is refused": {
			over:    map[string]any{"state": "CLOSED"},
			refusal: "state is CLOSED",
		},
		"a pull request on another base is refused": {
			over:    map[string]any{"baseRefName": "main"},
			refusal: "base is main, expected " + shipBase,
		},
		"a pull request that cannot merge cleanly is refused": {
			over:    map[string]any{"mergeable": "CONFLICTING"},
			refusal: "mergeable is CONFLICTING",
		},
		"a draft is refused": {
			over:    map[string]any{"isDraft": true},
			refusal: "is a draft",
		},
		"a head that moved after the gate is refused": {
			over:    map[string]any{shipHeadKey: strings.Repeat("e", 40)},
			refusal: "is not the gated",
		},
		"a base that moved before the merge call is refused and no child moves": {
			setup: func(w *shipWorld) {
				w.tipReads = []string{shipTipOne, shipTipTwo}
				w.children = []model.PullRequest{{Number: shipChild}}
				w.children[0].BaseRefName = ptr(shipBranch)
			},
			refusal: "moved from " + shipTipOne + " to " + shipTipTwo,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file, world := mergingExample(t, func(head string) *shipWorld {
				return &shipWorld{pull: shipPull(t, head, tc.over)}
			})
			if tc.setup != nil {
				tc.setup(world)
			}

			result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			if tc.refusal != "" {
				require.ErrorIs(t, err, errMergeRefused)
				require.ErrorContains(t, err, tc.refusal)
				assert.Empty(t, world.writes, "a refused merge writes nothing")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, "merged", result.Merge)
			assert.Equal(t, tc.writes(rig.head), world.writes)
		})
	}
}

func TestExampleMerge_AMergeSwitchThatIsNotTrueLeavesTheMergeToTheOwner(t *testing.T) {
	t.Parallel()

	rig, file, world := mergingExample(t, func(head string) *shipWorld { return &shipWorld{pull: shipPull(t, head, nil)} })
	rig.h.opts.Env["MERGE"] = "false"

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, world.writes, "a dry run writes nothing")
	assert.Equal(t, "merged", result.Merge)
}

func TestExampleMerge_TheMergeCallPassesTheTreeTheGateRecorded(t *testing.T) {
	t.Parallel()

	rig, file, _ := mergingExample(t, func(head string) *shipWorld { return &shipWorld{pull: shipPull(t, head, nil)} })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.Len(t, rig.h.merge.given, 1)
	assert.Equal(t, exampleTree, rig.h.merge.given[0].GatedTree, "the merge proves the merged tree against the tree of the gate")
}

func ptr[T any](value T) *T { return &value }
