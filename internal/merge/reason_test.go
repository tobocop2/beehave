package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

func TestReason_AMergeableStateThatIsUnknownIsUnsettled(t *testing.T) {
	t.Parallel()

	result := newWorld(t).set(t, map[string]any{"mergeable": "UNKNOWN"}).run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Unsettled, result.Reason)
	assert.Equal(t, "REFUSE: mergeable is UNKNOWN", last(result))
}

func TestReason_OtherRefusalsAndADoneRunHaveNoReason(t *testing.T) {
	t.Parallel()

	conflicting := newWorld(t).set(t, map[string]any{"mergeable": "CONFLICTING"}).run(request())
	done := newWorld(t).run(request())

	assert.Equal(t, merge.Plain, conflicting.Reason)
	assert.Equal(t, merge.Plain, done.Reason)
}

func TestReason_ARefusalWithACauseThatCanPassByItselfIsTransient(t *testing.T) {
	t.Parallel()

	good := tipRead{sha: tipSHA, found: true}
	same := pullJSON(t, nil)
	running := rows(model.CheckPass, model.CheckPending)

	for name, set := range map[string]func(*world){
		"the state cannot be read":               func(w *world) { w.fails["PRView"] = true },
		"the checks cannot be read":              func(w *world) { w.fails["PRChecks"] = true },
		"the checks still run":                   func(w *world) { w.checks = running },
		"the head cannot be read again":          func(w *world) { w.later = &same; w.fails["PRView later"] = true },
		"the base moved while preparing":         func(w *world) { w.tipReads = []tipRead{good, {sha: moved, found: true}} },
		"the base tip cannot be read":            func(w *world) { w.tipReads = []tipRead{{err: errDown}} },
		"the base tip is read late":              func(w *world) { w.tipReads = []tipRead{good, {err: errDown}} },
		"the forge merge call returned an error": func(w *world) { w.fails["MergePR"] = true },
	} {
		w := newWorld(t)
		set(w)
		result := w.run(request())

		assert.Equal(t, merge.Refused, result.Stop, name)
		assert.Equal(t, merge.Transient, result.Reason, name)
	}
}

func TestReason_AGuardThatTheNextReadCannotChangeStaysPlain(t *testing.T) {
	t.Parallel()

	for name, over := range map[string]map[string]any{
		"the head is not the gated head": {"headRefOid": moved},
		"the pull request is closed":     {"state": "CLOSED"},
	} {
		result := newWorld(t).set(t, over).run(request())

		assert.Equal(t, merge.Plain, result.Reason, name)
	}

	later := pullJSON(t, map[string]any{"headRefOid": moved})
	w := newWorld(t)
	w.later = &later

	assert.Equal(t, merge.Plain, w.run(request()).Reason, "a head that moved while preparing is the member's own change, so the batch rejects it")
}

func TestReason_ATreeProofStopCarriesTheTreeProofReason(t *testing.T) {
	t.Parallel()

	differ := newEvidence(t)
	differ.treeOf[tipSHA] = otherTree
	differ.blobs[gatedTree] = map[string]string{"a.go": "a"}
	differ.blobs[otherTree] = map[string]string{"a.go": "b"}
	req := request()
	req.GatedTree = gatedTree

	noTrees := newWorld(t).run(req)

	assert.Equal(t, merge.TreeProof, differ.runWith(req).Reason, "the trees differ")
	assert.Equal(t, merge.TreeProof, noTrees.Reason, "the forge reads no trees")
}
