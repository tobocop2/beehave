package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// goldenRecorded keeps the digest of every golden list for each module versions string that a build had. An entry is added when a bump records new lists,
// and an entry that is already there stays as it is. The digest of the build must equal the entry of its own module versions, so a change to a golden list
// with no bump of a module breaks the test, unless the change also rewrites the entry of the current versions, which a review sees in the diff.
// A change behind a patch leaves the base lists alone, so it needs no new entry.
var goldenRecorded = map[string]string{
	"core18.agent15.tracker2.forge6.land9.gate6.host1.round5":  "87c995bf3380115401d3164685d0c9c00a088e12c1c8132d8bb5c513b744b4a9",
	"core18.agent15.tracker2.forge7.land9.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core18.agent15.tracker2.forge7.land10.gate6.host1.round5": "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
}

// goldenDigest is the digest of the base lists, the shapes and the kinds. It covers each golden list of the step tests, not only the lists of goldenFlow.
func goldenDigest() string {
	parts := []string{
		fmt.Sprint(goldenBaseSteps), fmt.Sprint(goldenBaseGateSteps), fmt.Sprint(goldenBaseLostGateSteps),
		fmt.Sprint(goldenBatchSteps), fmt.Sprint(goldenBatchGateSteps), fmt.Sprint(goldenLostBatchSteps),
		fmt.Sprint(goldenLostAskSteps), fmt.Sprint(goldenOneMemberSteps), fmt.Sprint(goldenShapes), fmt.Sprint(goldenKinds),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	return hex.EncodeToString(sum[:])
}

func TestGoldenLists_AChangeNeedsABumpOfAModuleOrAMarker(t *testing.T) {
	t.Parallel()

	recorded, ok := goldenRecorded[moduleVersions()]

	assert.True(t, ok, "no digest of the golden lists for %s: add the entry %q to goldenRecorded", moduleVersions(), goldenDigest())
	assert.Equal(t, recorded, goldenDigest(), "a golden list changed under the same module versions: put the change behind a patch (a patch adds a marker and leaves the base lists alone), "+
		"or bump the version of the module that owns the steps in moduleTable (stepmodules.go) and add a digest for the new versions to goldenRecorded")
}
