package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// goldenSteps, goldenGateSteps and goldenLostGateSteps are the lists that a fresh run records now. They equal the base lists until a change goes behind a patch:
// then the list shows the marker DBOS.patch-<name> and the steps of the new code, and the base list stays as the last bump left it.
var (
	goldenSteps         = slices.Clone(goldenBaseSteps)
	goldenGateSteps     = slices.Clone(goldenBaseGateSteps)
	goldenLostGateSteps = slices.Clone(goldenBaseLostGateSteps)
)

// patchMarkerPrefix starts the name of the step that DBOS records for a patch.
const patchMarkerPrefix = "DBOS.patch-"

// withoutMarkers returns the step names with the markers of patches left out.
func withoutMarkers(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool { return strings.HasPrefix(name, patchMarkerPrefix) })
}

// unpatchedChange says whether the golden list differs from the base list with no marker of a patch in it: a change to the steps of a builtin that no patch carries.
func unpatchedChange(base, golden []string) bool {
	return !slices.Equal(withoutMarkers(golden), base) && len(withoutMarkers(golden)) == len(golden)
}

func TestGoldenSteps_ACurrentListThatDiffersFromTheBaseListShowsTheMarkerOfAPatch(t *testing.T) {
	t.Parallel()

	assert.False(t, unpatchedChange(goldenBaseSteps, goldenSteps), "the steps of goldenFlow changed with no marker: put the change behind a patch, or bump the module and record goldenBaseSteps")
	assert.False(t, unpatchedChange(goldenBaseGateSteps, goldenGateSteps), "the steps of the gate run changed with no marker: put the change behind a patch, or bump the module and record goldenBaseGateSteps")
	assert.False(t, unpatchedChange(goldenBaseLostGateSteps, goldenLostGateSteps), "the steps of the lost gate job changed with no marker: put the change behind a patch, or bump the module and record goldenBaseLostGateSteps")
}

func TestUnpatchedChange_TellsAPatchedChangeFromAnUnpatchedOne(t *testing.T) {
	t.Parallel()

	base := []string{"now#1", "wait:w#1"}
	marker := patchMarkerPrefix + "now-checks-first"

	tests := []struct {
		name   string
		golden []string
		want   bool
	}{
		{name: "equal to the base", golden: []string{"now#1", "wait:w#1"}, want: false},
		{name: "a step added with no marker", golden: []string{"now:check#1", "now#1", "wait:w#1"}, want: true},
		{name: "a step removed with no marker", golden: []string{"wait:w#1"}, want: true},
		{name: "a step reordered with no marker", golden: []string{"wait:w#1", "now#1"}, want: true},
		{name: "a step added behind a marker", golden: []string{marker, "now:check#1", "now#1", "wait:w#1"}, want: false},
		{name: "a step of the engine added is not a marker", golden: []string{"DBOS.sleep", "now#1", "wait:w#1"}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, unpatchedChange(base, test.golden))
		})
	}
}
