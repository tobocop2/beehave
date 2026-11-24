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
	"core18.agent15.tracker2.forge6.land9.gate6.host1.round5":   "87c995bf3380115401d3164685d0c9c00a088e12c1c8132d8bb5c513b744b4a9",
	"core18.agent15.tracker2.forge7.land9.gate6.host1.round5":   "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core18.agent15.tracker2.forge7.land10.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core18.agent15.tracker2.forge7.land11.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core18.agent15.tracker2.forge7.land12.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core19.agent15.tracker2.forge7.land12.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core19.agent15.tracker2.forge7.land13.gate6.host1.round5":  "600a75b36a1fac86d98d974b8acbfc837447315fabec83a939a2c1dc9f88825e",
	"core19.agent15.tracker3.forge8.land13.gate6.host1.round5":  "d2e27aa5bab908ddda26f2481fd7ce7b9cbf9a743889dfb322edb84b73dfc6a1",
	"core19.agent15.tracker4.forge9.land13.gate6.host1.round6":  "c0437bb0e8c2a569677e50e4dc410a5a99b8c1fe1a579c25ca0d6b9b48550bd6",
	"core20.agent15.tracker4.forge10.land13.gate6.host1.round6": "b75c8dd18ff96322209c22013bd57d35ad10428f2543b4c9632525c9058a15a3",
	"core20.agent15.tracker4.forge11.land13.gate6.host1.round6": "ccee5fffb807f08e62bea02ea4b780e1902e4cdfbc361b7e92557aa7fd77ad4a",
	"core20.agent15.tracker4.forge11.land13.gate7.host1.round6": "a489f6e9f9ac6cdbd5ca5b9cc0e1132df2d38c3ffa67787dd890650ffb9739fb",
	"core20.agent15.tracker4.forge11.land14.gate7.host1.round6": "3c0c5a833414b40f566fb3e115f34c3fce980b1b00dcd7c9872e7c2edf474be9",
	"core20.agent15.tracker4.forge11.land15.gate7.host1.round7": "4b1d3887ad5fec4e501bb54d12dbdb340c4abbf4951114ed61fd10f52db2b823",
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
