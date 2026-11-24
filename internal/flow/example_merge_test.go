package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	shipBase    = "integration"
	shipBranch  = "item-12"
	shipOwner   = "me"
	shipMail    = "me@x"
	shipTipOne  = "cccccccccccccccccccccccccccccccccccccccc"
	shipChild   = 9
	shipHeadKey = "headRefOid"
)

// shipWorld is the forge fake of the example. It holds one open pull request. A method it does not define panics,
// so a call the merge makes that the test did not expect fails the test.
type shipWorld struct {
	merge.Forge

	mu        sync.Mutex
	pull      model.PullRequest
	children  []model.PullRequest
	requests  []model.ChangeRequest
	threads   []model.ReviewThread
	committer model.Committer
	checks    []model.Check
	writes    []string
	listed    []int // the issues the forge lists for the pull request: the fake lists the ones the member names
}

func (w *shipWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return w.pull, nil
}

func (w *shipWorld) PullRead(_ context.Context, pr int) (model.PullFacts, error) {
	facts := w.pull.Facts()
	facts.Number = pr

	if err := facts.Check(); err != nil {
		return model.PullFacts{}, err
	}

	return facts, nil
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

func (*shipWorld) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

func (*shipWorld) Compare(context.Context, string, string) (model.Comparison, error) {
	return model.Comparison{Status: "ahead", AheadBy: 1}, nil
}

func (*shipWorld) StackID(context.Context, int) (string, error) { return "", nil }

func (w *shipWorld) IssueState(context.Context, int) (string, error) {
	return "", errFake
}

func (w *shipWorld) IssueAssignees(context.Context, string, int) ([]string, error) {
	return nil, nil
}

func (w *shipWorld) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: w.listed}, nil
}

func (*shipWorld) BranchRef(context.Context, string) (string, bool, error) {
	return shipTipOne, true, nil
}

func (w *shipWorld) SetBase(_ context.Context, pr int, base string) error {
	w.write(fmt.Sprintf("SetBase %d %s", pr, base))

	return nil
}

// SetDraft marks the pull request a draft or ready, as the forge does.
func (w *shipWorld) SetDraft(_ context.Context, pr int, draft bool) error {
	w.write(fmt.Sprintf("SetDraft %d %t", pr, draft))

	w.pull.IsDraft = &draft

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
		rules, err := merge.Compile(settings.ForbidText)
		if err != nil {
			return merge.Result{}, err
		}

		world.listed = settings.Closes

		return merge.Run(ctx, world, merge.Request{
			Names: settings.Closes, PR: testPull, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: []string{"bee-1"},
			OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved, OwnDraft: settings.OwnDraft,
			Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget,
		}), nil
	}
}

// landWorld is shipWorld over the repository of the round rig. The guards read the pull request of shipWorld. The tip of the base, the merge call,
// the trees and the history are those of the branch main of the repository, so a merge that every guard passes lands for real and the proofs read the result.
type landWorld struct {
	*shipWorld

	repo repoGit
	// moveAt is the read of the base tip after which another commit lands on main, as a merge by another session does. Zero moves nothing, and a negative number moves it at every read.
	moveAt int
	reads  int
}

func (w *landWorld) BranchRef(ctx context.Context, branch string) (string, bool, error) {
	tip, err := w.git(ctx, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}

	w.mu.Lock()
	w.reads++
	move := w.reads == w.moveAt || w.moveAt < 0
	w.mu.Unlock()

	if move {
		if err := w.advance(ctx, branch, tip, tip+"^{tree}", "a merge of another session"); err != nil {
			return "", false, err
		}
	}

	return tip, true, nil
}

// MergePR merges the head into main with a merge commit, as the forge does, and records the call.
func (w *landWorld) MergePR(ctx context.Context, pr int, sha string) error {
	w.write(fmt.Sprintf("MergePR %d %s", pr, sha))

	tip, err := w.git(ctx, "rev-parse", "refs/heads/main")
	if err != nil {
		return err
	}

	tree, err := w.git(ctx, "merge-tree", "--write-tree", tip, sha)
	if err != nil {
		return err
	}

	return w.advance(ctx, "main", tip, tree, fmt.Sprintf("Merge #%d", pr), "-p", sha)
}

// advance moves the branch from tip to a new commit of the tree, with tip and the other parents as its parents.
func (w *landWorld) advance(ctx context.Context, branch, tip, tree, message string, parents ...string) error {
	commit, err := w.git(ctx, append([]string{"commit-tree", tree, "-p", tip, "-m", message}, parents...)...)
	if err != nil {
		return err
	}

	_, err = w.git(ctx, "update-ref", "refs/heads/"+branch, commit, tip)

	return err
}

func (w *landWorld) Compare(ctx context.Context, commit, base string) (model.Comparison, error) {
	if res := w.repo.Run(ctx, []string{"merge-base", "--is-ancestor", commit, "refs/heads/" + base}); res.ExitCode != 0 {
		return model.Comparison{Status: "diverged", BehindBy: 1}, nil
	}

	return model.Comparison{Status: "ahead", AheadBy: 1}, nil
}

func (w *landWorld) CommitTree(ctx context.Context, commit string) (string, error) {
	return w.git(ctx, "rev-parse", commit+"^{tree}")
}

// TreeBlobs lists the blob of each path of the tree. The merge reads it only to name the paths of a tree that is not the gated one.
func (w *landWorld) TreeBlobs(ctx context.Context, tree string) (map[string]string, error) {
	listing, err := w.git(ctx, "ls-tree", "-r", tree)
	if err != nil {
		return nil, err
	}

	blobs := map[string]string{}

	for _, line := range strings.Split(listing, "\n") {
		meta, path, found := strings.Cut(line, "\t")
		if fields := strings.Fields(meta); found && len(fields) == 3 {
			blobs[path] = fields[2]
		}
	}

	return blobs, nil
}

// git runs git in the repository and returns its output with no trailing newline. A command that fails is an error.
func (w *landWorld) git(ctx context.Context, args ...string) (string, error) {
	res := w.repo.Run(ctx, args)
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%w: git %s: %s", errFake, strings.Join(args, " "), res.Stderr)
	}

	return strings.TrimSpace(res.Stdout), nil
}

// forgeMerger is the merger of a build: the real merge over the forge fake of each item, with the request that a build makes from the settings of merge.
type forgeMerger struct {
	worlds map[string]*landWorld

	mu     sync.Mutex
	lines  map[string][]string // the lines of every verdict of the merge of each item
	closes map[string][]int    // the closes of the member of each item, as the merge got them
}

func (m *forgeMerger) Merge(ctx context.Context, _, item, head string, settings MergeSettings) (merge.Result, error) {
	rules, err := merge.Compile(settings.ForbidText)
	if err != nil {
		return merge.Result{}, err
	}

	m.worlds[item].listed = settings.Closes

	got := merge.Run(ctx, m.worlds[item], merge.Request{
		Names: settings.Closes, PR: testPull, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: []string{"bee-1"},
		OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved, OwnDraft: settings.OwnDraft,
		Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget, GatedTree: settings.GatedTree, Intent: settings.Intent,
		Queue: true,
	})

	m.mu.Lock()
	m.lines[item] = append(m.lines[item], got.Lines...)

	if m.closes == nil {
		m.closes = map[string][]int{}
	}

	m.closes[item] = settings.Closes
	m.mu.Unlock()

	return got, nil
}

// said returns the lines of the verdicts of the merge of the item as one text.
func (m *forgeMerger) said(item string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return strings.Join(m.lines[item], "\n")
}

func (*forgeMerger) Poll(context.Context, string, string, string, MergeSettings, bool) (merge.Result, error) {
	return merge.Result{}, errFake
}

func (*forgeMerger) Disarm(context.Context, string, string, string, MergeSettings) error {
	return nil
}

// landingRound is the round rig over items 1 and 3, with the real merge over a forge fake for each item. The pull request of item 1 has the keys of over replaced.
func landingRound(t *testing.T, over map[string]any) (*roundRig, *forgeMerger) {
	t.Helper()

	rig := newRoundRig(t, "1 3")
	worlds := map[string]*landWorld{}

	for _, item := range []string{"1", "3"} {
		fields := map[string]any{"baseRefName": "main", "headRefName": item}
		if item == "1" {
			maps.Copy(fields, over)
		}

		worlds[item] = &landWorld{shipWorld: &shipWorld{pull: shipPull(t, rig.heads[item], fields)}, repo: rig.repo}
	}

	forge := &forgeMerger{worlds: worlds, lines: map[string][]string{}}
	rig.forge = forge

	return rig, forge
}

func TestExampleMerge_TheRoundLandsThroughTheBatchMergerAndTheGuardsOfTheForgeFake(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		over    map[string]any
		setup   func(*landWorld)
		refusal string // a line of the verdict that refuses item 1; empty when item 1 lands
		defer1  bool   // item 1 is deferred: neither landed nor held
	}{
		"two clean heads land at their pins": {},
		"own work lands, because its member carries the approval of its review": {
			over: map[string]any{"author": map[string]any{"login": shipOwner}},
		},
		"a pull request with a child is refused, because merge never retargets": {
			setup: func(w *landWorld) {
				branch := "1"
				w.children = []model.PullRequest{{Number: shipChild}}
				w.children[0].BaseRefName = &branch
			},
			refusal: "--allow-orphans",
		},
		"a pull request with the hold label is refused": {
			over:    map[string]any{"labels": []any{map[string]any{"name": "Hold"}}},
			refusal: "has the label hold",
		},
		"a body with an attribution line is refused with its line number": {
			over:    map[string]any{"body": "a\nb\nGenerated with [Claude Code]"},
			refusal: "line 3 matches pattern 4 of forbid_text",
		},
		"a standing change request is refused": {
			setup: func(w *landWorld) {
				w.requests = []model.ChangeRequest{{Login: "rev", Commit: strings.Repeat("a", 40)}}
			},
			refusal: "rev",
		},
		"an open review thread is refused": {
			setup: func(w *landWorld) {
				path, line, body := "a.go", 4, "fix this"
				w.threads = []model.ReviewThread{{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: &body}}}
			},
			refusal: "unresolved review threads",
		},
		"a failing check is refused": {
			setup:   func(w *landWorld) { w.checks = []model.Check{{Name: "ci", Bucket: model.CheckFail}} },
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
			over:    map[string]any{"baseRefName": shipBase},
			refusal: "base is " + shipBase + ", expected main",
		},
		"a pull request that cannot merge cleanly is refused": {
			over:    map[string]any{"mergeable": "CONFLICTING"},
			refusal: "mergeable is CONFLICTING",
		},
		"a head that moved after the review is refused": {
			over:    map[string]any{shipHeadKey: strings.Repeat("e", 40)},
			refusal: "is not the gated",
		},
		"a base that moved before the merge call defers the item": {
			setup:  func(w *landWorld) { w.moveAt = 1 },
			defer1: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, forge := landingRound(t, tc.over)
			one, three := forge.worlds["1"], forge.worlds["3"]
			if tc.setup != nil {
				tc.setup(one)
			}

			_, err := rig.run(t)

			require.NoError(t, err)

			switch {
			case tc.defer1:
				// A deferral writes nothing and holds nothing, so the row of item 1 stays done and the next pass offers it again, once the base stopped moving.
				assert.Contains(t, forge.said("1"), "while preparing")
				assert.Empty(t, heldOf(rig.writer), "a deferred item is not held")
				assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["1"])}, one.writes, "item 1 lands at its pin in the next pass")
				assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["3"])}, three.writes, "item 3 lands at its pin")
				assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t), "both items land: the deferral costs item 1 one pass")
			case tc.refusal != "":
				assert.Contains(t, forge.said("1"), tc.refusal)
				assert.Empty(t, one.writes, "a refused merge writes nothing")
				assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["3"])}, three.writes, "item 3 lands at its pin")
				assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
				assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
				assert.Len(t, notesWithKey(rig.writer, "rejected"), 1, "the held item gets the reason as a comment")
			default:
				assert.Contains(t, forge.said("1"), "holds the gated tree", "the tree proof reads the tip that the merge made")
				assert.Contains(t, forge.said("3"), "holds the gated tree", "the tree proof reads the tip that the merge made")
				assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["1"])}, one.writes, "item 1 lands at its pin")
				assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["3"])}, three.writes, "item 3 lands at its pin")
				assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t), "main holds the merges of both items")
				assert.Empty(t, heldOf(rig.writer))
			}
		})
	}
}

// A deferral writes no row, so a pass that only deferred must not start the next pass at once: the round offers the rows once in each pass that a child ended in, and then waits for the owner.
func TestExampleMerge_ABaseThatKeepsMovingMakesTheRoundWaitAfterOneLandingPass(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, nil)
	forge.worlds["1"].moveAt = -1
	forge.worlds["3"].moveAt = -1

	_, err := rig.run(t)

	require.NoError(t, err)

	lands := 0
	for _, name := range stepNames(t, rig.last) {
		if strings.HasPrefix(name, "integration_pull:round-integration-land-") {
			lands++
		}
	}

	assert.Equal(t, 2, lands, "the round offers the deferred rows once in each pass that a child ended in, and then waits")
	assert.Empty(t, heldOf(rig.writer), "a deferred item is not held")
	require.Len(t, rig.questions, 1, "the round waits for the owner")
}

// rejectReviewOf makes the review of the item write the verdict reject in place of the approve of the round rig. The other items keep their approve.
func rejectReviewOf(t *testing.T, rig *roundRig, item string) {
	t.Helper()

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		if request.Item == item && strings.Contains(request.Prompt, reviewMarker) {
			if err := os.WriteFile(promptOutput(request.Prompt), []byte(`{"verdict":"reject","mechanism":"","regressions":"0","open":"","findings":"[]"}`), 0o600); err != nil {
				t.Errorf("write the reject verdict of item %s: %v", item, err)
			}
		}

		return result
	}
}

// The round lands each member with the issues that its child returned, and the child chose them from the base of its pull request.
func TestExampleMerge_TheMemberOfEachChildCarriesTheIssuesThatItsChildReturned(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, nil)

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, map[string][]int{"1": {1}, "3": {3}}, forge.closes, "a trunk round closes the issue of each item")
}

// The round sends a child to the batch only when its review approved it. The merger checks the approval only for own work,
// so the pull request of another author (dev, outside the round) with a rejected head would merge if the round sent it.
func TestExampleMerge_TheRoundSendsNoChildThatItsReviewDidNotApproveToTheBatch(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, nil)
	rejectReviewOf(t, rig, "1")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, forge.said("1"), "the rejected child is not a member of the batch, so no merge of it runs")
	assert.Empty(t, forge.worlds["1"].writes, "nothing of the rejected item is merged")
	assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["3"])}, forge.worlds["3"].writes, "the approved item lands at its pin")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer), "the child holds its rejected item")
}

// The merge refuses a body with an attribution line and names the line by its number, never by its text. The round posts every line of the refusal
// as the comment of the held item, with the attribution patterns as forbid_text, and the comment passes. The other item lands, and the round goes on.
func TestExampleMerge_AnAttributionLineHoldsTheItemWithEveryLineOfTheRefusalAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, map[string]any{"body": "a\nb\nGenerated with [Claude Code]"})

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, forge.worlds["1"].writes, "a refused merge writes nothing")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))

	notes := notesWithKey(rig.writer, "rejected")
	require.Len(t, notes, 1, "the held item gets the refusal as a comment")
	assert.Contains(t, notes[0], fmt.Sprintf("REFUSE: forbidden text on #%d. Offending lines:", testPull), "the comment holds the cause line of the refusal")
	assert.Contains(t, notes[0], "line 3 matches pattern 4 of forbid_text", "the comment holds the line that names the hit")
	assert.NotContains(t, notes[0], "Generated with", "the comment never quotes the forbidden text")
}
