package batch

import (
	"context"
	"errors"
	"slices"
	"strings"
)

var errBoom = errors.New("boom")

// world is a fake of git, the gate, the forge and the step store. A commit is "c:" and the sorted items it holds, a tree is "t:" and the same items, and a head is "h-" and one item.
// So two unions that hold the same items have the same tree id, as they do in git.
type world struct {
	items []string
	// bad says the gate is red for a union that holds these items.
	bad func(items []string) bool
	// conflict says that item conflicts with a commit that holds these items.
	conflict func(have []string, item string) bool
	version  string

	gateTrees     []string
	gateTreeFixed string
	merges        []string
	pushAfter     int
	// missing holds the heads that the repository does not have.
	missing map[string]bool
	// answers makes Merge answer for an item without merging it. asked lists the items that got such an answer.
	answers map[string]MergeOutcome
	asked   []string

	mergeTreeCalls int
	failMergeTree  int
	failNewCommit  error
	failGate       error
	failMerge      error
	failMergeItem  string
	failHasCommit  error
	failVersion    error
	failTip        error

	store *memory
}

func newWorld() *world {
	return &world{version: "git version 2.50.0", store: newMemory()}
}

func (w *world) deps() Deps {
	return Deps{Git: w, Gate: w, Base: w, Steps: w.store, Name: "q"}
}

func tree(items []string) string   { return "t:" + strings.Join(sorted(items), ",") }
func commit(items []string) string { return "c:" + strings.Join(sorted(items), ",") }

func sorted(items []string) []string {
	out := slices.Clone(items)
	slices.Sort(out)

	return slices.Compact(out)
}

// itemsOf reads the items of a commit or a head.
func itemsOf(id string) []string {
	if item, ok := strings.CutPrefix(id, "h-"); ok {
		return []string{item}
	}

	list := strings.TrimPrefix(strings.TrimPrefix(id, "c:"), "t:")
	if list == "" {
		return nil
	}

	return strings.Split(list, ",")
}

func (w *world) Version(context.Context) (string, error) {
	return w.version, w.failVersion
}

func (w *world) MergeTree(_ context.Context, ours, theirs string) (MergeResult, error) {
	w.mergeTreeCalls++
	if w.mergeTreeCalls == w.failMergeTree {
		return MergeResult{}, errBoom
	}

	have, item := itemsOf(ours), itemsOf(theirs)[0]
	clash := w.conflict != nil && w.conflict(have, item)

	return MergeResult{Tree: tree(append(slices.Clone(have), item)), Conflict: clash}, nil
}

func (w *world) NewCommit(_ context.Context, treeID string, _ ...string) (string, error) {
	if w.failNewCommit != nil {
		return "", w.failNewCommit
	}

	return "c:" + strings.TrimPrefix(treeID, "t:"), nil
}

func (w *world) Run(_ context.Context, in GateInput) (GateOutcome, error) {
	if w.failGate != nil {
		return GateOutcome{}, w.failGate
	}

	w.gateTrees = append(w.gateTrees, in.Tree)

	reported := in.Tree
	if w.gateTreeFixed != "" {
		reported = w.gateTreeFixed
	}

	red := w.bad != nil && w.bad(itemsOf(in.Commit))

	return GateOutcome{Tree: reported, Passed: !red, Report: "red on " + in.Tree}, nil
}

func (w *world) Tip(context.Context) (Tip, error) {
	return Tip{Commit: commit(w.items), Tree: tree(w.items)}, w.failTip
}

func (w *world) HasCommit(_ context.Context, id string) (bool, error) {
	return !w.missing[id], w.failHasCommit
}

func (w *world) Merge(_ context.Context, m Member, _ string) (MergeOutcome, error) {
	if w.failMerge != nil && (w.failMergeItem == "" || w.failMergeItem == m.Item) {
		return MergeOutcome{}, w.failMerge
	}

	if answer, ok := w.answers[m.Item]; ok {
		w.asked = append(w.asked, m.Item)

		return answer, nil
	}

	w.items = append(w.items, m.Head[2:])
	w.merges = append(w.merges, m.Item)

	if w.pushAfter > 0 && len(w.merges) == w.pushAfter {
		w.items = append(w.items, "other")
	}

	return MergeOutcome{Status: StatusMerged}, nil
}

// memory is a step store. A name that was recorded returns its bytes without a call. failOn makes the step of that name fail.
type memory struct {
	data   map[string][]byte
	names  []string
	failOn map[string]error
}

func newMemory() *memory {
	return &memory{data: map[string][]byte{}, failOn: map[string]error{}}
}

func (m *memory) Step(_ context.Context, name string, run func() ([]byte, error)) ([]byte, error) {
	if err := m.failOn[name]; err != nil {
		return nil, err
	}

	if raw, ok := m.data[name]; ok {
		return raw, nil
	}

	raw, err := run()
	if err != nil {
		return nil, err
	}

	m.data[name] = raw
	m.names = append(m.names, name)

	return raw, nil
}

func (m *memory) countPrefix(prefix string) int {
	return len(slices.DeleteFunc(slices.Clone(m.names), func(name string) bool { return !strings.HasPrefix(name, prefix) }))
}

func members(items ...string) []Member {
	out := make([]Member, 0, len(items))
	for _, item := range items {
		out = append(out, Member{Item: item, Head: "h-" + item})
	}

	return out
}

func itemsOfOutcomes(outcomes []Outcome) []string {
	var out []string
	for _, o := range outcomes {
		out = append(out, o.Item)
	}

	return out
}

func has(items []string, want string) bool { return slices.Contains(items, want) }
