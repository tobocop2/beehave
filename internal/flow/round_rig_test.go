package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

var errNoWorktree = errors.New("the rig could not make the worktree of an item")

// stallLimit is the longest a child waits for its turn or the round waits for a member to end. A turn that never comes is a mistake of the test.
const stallLimit = 30 * time.Second

// readsBeforeReview is the number of the read of the pull request that comes just before the full review of a child: the intake is the first read,
// and the read before the scoped review is the second.
const readsBeforeReview = 3

// readsToLanding is the number of reads of the pull request of a member by the end of its landing: the intake, the reads before the scoped and the full review, and the read of its own batch.
const readsToLanding = readsBeforeReview + 1

// turn is the wait of one member. The member holds at its read before the review until the round makes its read number `read` of the queue, the first read of the pass of that number.
type turn struct {
	read int
	open chan struct{}
}

// endOrder decides in which pass of the round each member ends. The first member never waits. Each later member waits at its read before the review until the round reads the queue of the pass after the member before it.
// So one member ends in each pass and the round lands the members in list order, one batch for each, at any speed of the machine: a pass starts only after the gather of the pass before returned.
type endOrder struct {
	stalled func(item string, what string)
	ended   func(item string) bool // reports whether the child run of the item has ended

	mu      sync.Mutex
	turns   map[string]*turn
	reads   map[string]int
	waiting map[int][]string // the members that the queue read of the turn waits for, until each has ended
	parked  map[string]bool  // the members that wait at their turn now or waited there before
}

func newEndOrder(members []string, stalled func(item string, what string)) *endOrder {
	s := &endOrder{stalled: stalled, turns: map[string]*turn{}, reads: map[string]int{}, waiting: map[int][]string{}, parked: map[string]bool{}}

	for at, item := range members {
		if at > 0 {
			s.turns[item] = &turn{read: at + 1, open: make(chan struct{})}
		}
	}

	return s
}

// together makes the members of rest end in one read of gather. They wait for queue read 2, and that read waits until each of them has ended.
// The run of a member that has ended stays ended, so the gather of pass 2 returns all of them. Every other member ends in pass 1. It replaces the end order of the round: call it before the run.
func (s *endOrder) together(rest []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.turns = map[string]*turn{}
	for _, item := range rest {
		s.turns[item] = &turn{read: 2, open: make(chan struct{})}
	}

	s.waiting = map[int][]string{2: slices.Clone(rest)}
}

// hold is the hook of a read of the pull request: the second read of a member waits for the turn of the member.
func (s *endOrder) hold(ctx context.Context, item string) {
	s.mu.Lock()
	s.reads[item]++
	mine := s.turns[item]
	holds := s.reads[item] == readsBeforeReview
	s.parked[item] = s.parked[item] || (mine != nil && holds)
	s.mu.Unlock()
	if mine == nil || !holds {
		return
	}

	select {
	case <-mine.open:
	case <-ctx.Done():
	case <-time.After(stallLimit):
		s.stalled(item, "its turn")
	}
}

// isParked reports whether the member has reached its turn: it waits there, or it waited there and has gone on.
func (s *endOrder) isParked(item string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.parked[item]
}

// queueRead is called at the start of each read of the queue of the round, with the number of the read. It opens the turns of that number, and then waits until the members that the turn names have ended.
func (s *endOrder) queueRead(n int) {
	s.mu.Lock()

	for _, mine := range s.turns {
		if mine.read == n {
			close(mine.open)
		}
	}

	names := s.waiting[n]
	s.mu.Unlock()

	for _, item := range names {
		if !waitUntil(func() bool { return s.ended(item) }) {
			s.stalled(item, "its end")
		}
	}
}

// children is what the round rig knows about the child runs: the head of the branch of each item, the items that reached the gate and the review in order,
// and the order that sets the ends of the members.
type children struct {
	heads map[string]string // the head of each item; an item with no branch of its own is at the tip of the base
	base  string
	turns *endOrder

	mu          sync.Mutex
	gated       []string
	reviewed    []string
	readAt      []string            // the items that were reviewed at the last read of the integration pull request
	reviewHooks map[string][]func() // the hooks that run when the review of the item has ended
}

// onReview registers a hook that runs when the review of the item has ended. No child reads a branch after its review.
func (c *children) onReview(item string, hook func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reviewHooks[item] = append(c.reviewHooks[item], hook)
}

func (c *children) head(item string) string {
	return cmp.Or(c.heads[item], c.base)
}

// started returns the items whose child reached the gate, in sorted order.
func (c *children) started() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	items := slices.Clone(c.gated)
	slices.Sort(items)

	return items
}

// atIntegration records the items that are reviewed now, at each read of the integration pull request, so the record is that of the last read.
func (c *children) atIntegration() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readAt = slices.Clone(c.reviewed)
}

// reviewedAtIntegration returns the items that were reviewed when the round last read the integration pull request.
func (c *children) reviewedAtIntegration() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.readAt)
}

// itemTrees gives each item its own worktree of the repository, at the head of its branch.
type itemTrees struct {
	repo repoGit
	kids *children
	root string

	mu   sync.Mutex
	dirs map[string]string
}

// tree makes the worktree of the item on the first call and returns its folder.
func (t *itemTrees) tree(ctx context.Context, item string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if dir, made := t.dirs[item]; made {
		return dir, nil
	}

	dir := filepath.Join(t.root, "tree-"+item)
	if res := t.repo.Run(ctx, []string{"worktree", "add", "-q", "--detach", dir, t.kids.head(item)}); res.ExitCode != 0 {
		return "", fmt.Errorf("%w: item %s: %s", errNoWorktree, item, res.Stderr)
	}

	t.dirs[item] = dir

	return dir, nil
}

// afterReview makes the fake agents of the rig note the end of each review. The agent queue runs one agent for each login, so a review must not wait for another child:
// the hooks of an item run when its review has ended. The review is the last agent of an approved child.
func (c *children) afterReview(inner func(AgentRequest) agent.Result) func(AgentRequest) agent.Result {
	return func(request AgentRequest) agent.Result {
		result := inner(request)

		if strings.Contains(request.Prompt, fullReview) {
			c.mu.Lock()
			c.reviewed = append(c.reviewed, request.Item)
			hooks := slices.Clone(c.reviewHooks[request.Item])
			c.mu.Unlock()

			for _, hook := range hooks {
				hook()
			}
		}

		return result
	}
}

// servedOnce shows the items of the queue as the tracker does: an item that the round closed is gone, and an item that the round put on hold carries the hold.
type servedOnce struct {
	*fakeQueue

	turns  *endOrder // told at each read of the open pull requests: only the queue read of the round makes it, one for each pass, and a child run makes none
	writer *fakeWriter

	seenMu sync.Mutex
	reads  int
}

func (s *servedOnce) OpenPulls(ctx context.Context, project string) ([]model.PullRequest, error) {
	s.seenMu.Lock()
	s.reads++
	read := s.reads
	s.seenMu.Unlock()

	s.turns.queueRead(read)

	return s.fakeQueue.OpenPulls(ctx, project)
}

func (s *servedOnce) OpenItems(ctx context.Context, project string) ([]model.Item, error) {
	all, err := s.fakeQueue.OpenItems(ctx, project)

	var open []model.Item

	for _, item := range all {
		status := s.writer.statusOf(item.ID)
		if status == "closed" {
			continue
		}

		item.Hold = item.Hold || status == "hold"
		open = append(open, item)
	}

	return open, err
}

// statusOf returns the last status that the run wrote for the item, or an empty text.
func (w *fakeWriter) statusOf(item string) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	last := ""

	for _, status := range w.statuses {
		if parts := strings.Split(status, "|"); len(parts) == 3 && parts[1] == item {
			last = parts[2]
		}
	}

	return last
}

// roundRig runs the round of the example (the entry main) over the batch rig. The queue lists the items once. Each item gets a child that approves
// the head the repository holds for it. The batch merger lands the approved heads.
type roundRig struct {
	*batchRig

	file    string
	kids    *children
	writer  *fakeWriter
	remote  *remoteBranches
	hosts   *fakeHosts
	q       *fakeQueue
	last    *Engine // the engine of the last run
	project model.Project
	answer  string   // the answer to the first wait of the round, STOP when empty
	answers []string // the answers to the waits in order, which win over answer
	// deadlines is the deadline of each wait that run answered, as the status showed it, in order.
	deadlines []time.Time
	// questions is the text of each wait that run answered, in order.
	questions []string
	// forge, when set, is the merger of the run in place of the land merger of the batch rig: the real merge over a forge fake.
	forge merger
}

// setRound sets the round setting `key` of the project of the rig before the run starts. A nil value removes the key.
func (r *roundRig) setRound(key string, value any) {
	setRoundOf(&r.project, key, value)
}

// newRoundRig makes a round over the items of the string. The round branch is main and the owner is shipOwner.
// A round runs the file of the example as it is. Each child runs the real pipeline over its own worktree, with fake agents that approve every item.
// An item that has a branch in the repository (1, 2 and 3) is at the head of that branch, and any other item is at the tip of main.
func newRoundRig(t *testing.T, items string) *roundRig {
	t.Helper()

	return newRoundRigOf(t, items)
}

// itemGate is the gate of a child run: it passes at the head of the branch of the item. The gate of the batch (the run of the round) is the gate of the batch rig.
type itemGate struct {
	*treeGate

	kids *children
}

func (g itemGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	if item == testItem {
		return g.treeGate.Run(ctx, project, item, host, job)
	}

	g.kids.mu.Lock()
	g.kids.gated = append(g.kids.gated, item)
	g.kids.mu.Unlock()

	return GateResult{Passed: true, Head: g.kids.head(item), Tree: exampleTree, Checks: cleanChecks()}, nil
}

// endTogether makes the children of the items end in one read of gather, in pass 2, and every other member end in pass 1. The round lands the two groups as two batches. Call it before the run.
func (r *roundRig) endTogether(items ...string) {
	r.kids.turns.together(items)
}

// landing reports whether the round has fetched the head of the item to land it. The children never fetch.
// Call it from a reply of the command fake only: the reply runs under the lock of the fake, which this reads without taking it.
func (r *roundRig) landing(item string) bool {
	return slices.ContainsFunc(r.h.commands.requests, func(request CommandRequest) bool {
		return len(request.Run) > 2 && request.Run[0] == "git" && request.Run[1] == "fetch" && request.Run[len(request.Run)-1] == r.heads[item]
	})
}

// pushAfterReviews moves the branch of the item on the remote to head when the review of the item has ended, and a head of "" deletes the branch.
func (r *roundRig) pushAfterReviews(item, head string) {
	r.kids.onReview(item, func() {
		r.remote.mu.Lock()
		defer r.remote.mu.Unlock()

		if head == "" {
			delete(r.remote.heads, item)

			return
		}

		r.remote.heads[item] = head
	})
}

// engine is the engine of the batch rig, with the gate of a child run in front of the gate of the batch.
func (r *roundRig) engine(t *testing.T) *Engine {
	t.Helper()

	return r.engineWith(t, func(*Deps) {})
}

// engineWith is engine with the change made to its collaborators, such as a clock of the test.
func (r *roundRig) engineWith(t *testing.T, change func(*Deps)) *Engine {
	t.Helper()

	deps := r.deps()
	deps.Gate = itemGate{treeGate: r.gate, kids: r.kids}

	if r.forge != nil {
		deps.Merge = r.forge
	}

	change(&deps)

	engine, err := NewEngine(t.Context(), r.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// loginsOf gives each item its own login. The agent queue runs one agent for each login, so no review waits for the agent of another member.
func loginsOf(items []string) map[string]string {
	logins := map[string]string{}
	for _, id := range items {
		logins[id] = "login-" + id
	}

	return logins
}

// listOf is the list setting of the words of the string, as the project vars hold a list.
func listOf(words string) []any {
	list := []any{}
	for _, word := range strings.Fields(words) {
		list = append(list, word)
	}

	return list
}

func newRoundRigOf(t *testing.T, items string) *roundRig {
	t.Helper()

	rig := newBatchRig(t)
	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "remediation"))))

	file := filepath.Join(folder, "workflow.star")
	queued := strings.Fields(items)

	// The members are the queued items that have a branch of their own: the items that the round lands. An item with no branch ends at once and would move the pass count of the end order.
	members := slices.DeleteFunc(slices.Clone(queued), func(id string) bool { return rig.heads[id] == "" })
	if len(members) > 1 && len(members) != len(queued) {
		t.Fatalf("a round with %d members and %d items with no branch: the end order of the ends needs every item to be a member", len(members), len(queued)-len(members))
	}

	stalled := func(item, what string) {
		t.Errorf("the child of item %s waited for %s: the round never reached it", item, what)
	}
	kids := &children{heads: rig.heads, base: strings.TrimSpace(rig.repo.must(t, "rev-parse", "main")), turns: newEndOrder(members, stalled), reviewHooks: map[string][]func(){}}
	// The checks before review of each child compare its head with the base origin/main.
	rig.repo.must(t, "update-ref", "refs/remotes/origin/main", kids.base)
	rig.h.logins = fakeLogins{login: testLogin, byItem: loginsOf(members)}
	trees := &itemTrees{repo: rig.repo, kids: kids, root: t.TempDir(), dirs: map[string]string{}}
	rig.h.trees = &fakeTrees{per: trees.tree, repos: filepath.Dir(rig.repo.dir)}
	rig.h.forge.state = review.Standing{Number: testPull, Head: kids.base}
	rig.h.forge.heads = rig.heads

	agents := &agentRig{h: rig.h}
	agents.answerLikeTheExample(exampleSkill, func(int) string { return "approve" }, func(int) string { return "[]" }, func(int) string { return "" }, "", designReviewLimit)
	rig.h.agents.result = kids.afterReview(rig.h.agents.result)

	// A round over items selects them by the item list, so the queue rows need no label. A round over no item keeps the label of the example rig and reads an empty queue.
	r := &roundRig{batchRig: rig, file: file, kids: kids, project: exampleProject()}
	r.setRound("base", "main")
	r.setRound("owner", shipOwner)
	r.setRound("owner_email", shipMail)
	r.setRound("merge", true)

	if len(queued) > 0 {
		r.setRound("label", nil)
		r.setRound("items", listOf(items))
	}
	rig.h.facts = func(string) model.Project { return r.project }

	// Each child runs the mutation step of the example after its green gate, in the one slot of the class mutation. The command fake runs it clean.
	rig.h.mutation = exampleMutationCommand
	rig.h.slots = func(_, _, class string) (SlotGrant, error) {
		return SlotGrant{Host: "box", Count: map[string]int{"mutation": 1}[class]}, nil
	}

	var rows []model.Item
	for _, id := range queued {
		rows = append(rows, queueItem(id, model.SeverityP1))
	}

	// Each child reads its own item: the id is the id of the item.
	// The item of the round itself has the field id only, as a round item has: the example runs a round for it, and a pipeline for an item with a title.
	rig.h.items.byItem = map[string]map[string]string{testItem: {"id": testItem}}
	for _, id := range queued {
		rig.h.items.byItem[id] = map[string]string{"id": id, "title": "a title", "body": "a body"}
	}

	r.writer = newFakeWriter()
	rig.h.writer = r.writer

	r.q = &fakeQueue{items: rows}
	rig.h.queue = &servedOnce{fakeQueue: r.q, turns: kids.turns, writer: r.writer}
	kids.turns.ended = func(item string) bool { return itemEnded(r.last, item) }
	rig.h.reader = &fakeReader{}

	// The round starts the host once and stops it in finish.
	r.hosts = &fakeHosts{host: &stubHost{}}
	rig.h.hosts = r.hosts

	// A member waits for its turn at the second read of the pull request of its item: the read before the review (the first is the intake).
	// A read holds no slot of the exclusive queue or of the agent queue, so a child waits there without blocking another.
	r.remote = &remoteBranches{heads: map[string]string{}, before: kids.turns.hold}

	for _, id := range queued {
		r.remote.heads[id] = kids.head(id)
	}

	for item, head := range rig.heads {
		r.remote.heads[item] = head
	}

	rig.h.preflight = r.remote

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 2 && request.Run[1] == "integration" {
			kids.atIntegration()

			return CommandOutput{Stdout: "pr=9 state=OPEN\n"}, nil
		}

		return CommandOutput{}, nil
	}

	return r
}

// run runs the round to its first wait for the owner, answers STOP, and returns the result. A round that stops with an error before the wait returns that error.
func (r *roundRig) run(t *testing.T) (ItemResult, error) {
	t.Helper()

	engine := r.engine(t)
	r.last = engine

	return r.runOn(t, engine)
}

// runOn is run on an engine that the test built.
func (r *roundRig) runOn(t *testing.T, engine *Engine) (ItemResult, error) {
	t.Helper()

	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, r.file)
		done <- runOutcome{result: result, err: err}
	}()

	client := r.h.client(t, testVersion)
	owner := &answering{}

	out := waitOn(t, waiting[runOutcome]{what: "the round to end", ch: done, read: func() (runOutcome, bool, error) {
		r.answerOnce(t, client, owner)

		return runOutcome{}, false, nil
	}})

	return out.result, out.err
}

// answering is what the loop of run knows about the waits that it answered: how many, and whether the last one still shows in the status.
type answering struct {
	given int
	open  bool
}

// answerOnce reads the status and answers a question that shows and that the loop has not answered yet.
func (r *roundRig) answerOnce(t *testing.T, client *Client, owner *answering) {
	t.Helper()

	infos, err := client.Status()
	if err != nil {
		return
	}

	at := slices.IndexFunc(infos, func(info RunInfo) bool { return info.Question != "" })
	if at < 0 {
		owner.open = false

		return
	}

	if owner.open || !recordsItsReceive(client, infos[at].ID) {
		return
	}

	// The deadline is read after the record check: the status read above can come before the record.
	view, err := client.Steps(infos[at].ID)
	if err != nil {
		return
	}

	r.deadlines = append(r.deadlines, view.Deadline)
	r.questions = append(r.questions, infos[at].Question)

	require.NoError(t, client.Reply(testItem, Reply{From: shipOwner, Text: r.answerFor(owner.given)}))

	owner.given++
	owner.open = true
}

// recordsItsReceive reports whether the open run has recorded the deadline of its receive. A wait shows its question before that record, and the status of a run reads its deadline from that record.
func recordsItsReceive(client *Client, id string) bool {
	records, err := client.records(id)

	return err == nil && len(records) > 0 && records[len(records)-1].StepName == sleepStep
}

// answerFor is the answer to the nth wait of the round: the nth of answers, then answer for the first wait, and STOP for the rest.
func (r *roundRig) answerFor(n int) string {
	if n < len(r.answers) {
		return r.answers[n]
	}

	if n == 0 {
		return cmp.Or(r.answer, "STOP")
	}

	return "STOP"
}
