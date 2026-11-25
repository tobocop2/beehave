package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A folder inside an .app bundle opens the bundle too, and every folder opens the preferences file.
func TestDeveloperReads_AFolderInABundleOpensTheBundleAndEveryFolderOpensThePreferences(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"/Applications/Xcode.app/Contents/Developer", "/Applications/Xcode.app", xcodePreferences},
		developerReads("/Applications/Xcode.app/Contents/Developer"))
	assert.Equal(t, []string{"/Library/Developer/CommandLineTools", xcodePreferences}, developerReads("/Library/Developer/CommandLineTools"))
	assert.Equal(t, []string{"/opt/Xcode.appx/Developer", xcodePreferences}, developerReads("/opt/Xcode.appx/Developer"), "a name that only holds .app is no bundle")
	assert.Equal(t, []string{"/a.app/Contents/Developer", "/a.app", xcodePreferences}, developerReads("/a.app/Contents/Developer"), "the nearest bundle above the folder")
	assert.Nil(t, developerReads(""), "no developer folder opens nothing")
}

// The command env names the developer folder and keeps the cache of xcrun inside the temporary folder.
func TestDeveloperEnv_NamesTheFolderAndKeepsTheCacheInTheTemporaryFolder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"DEVELOPER_DIR=/d", "xcrun_db=" + filepath.Join("/t", "xcrun_db")}, developerEnv("/d", "/t"))
	assert.Nil(t, developerEnv("", "/t"))
}

// The plan records the developer folder, so the enforcer knows that a shim of Apple may run.
func TestPlanOf_RecordsTheDeveloperFolder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/d", planOf(nil, "/w", "/t", nil, nil, false, "/d").Developer)
	assert.Empty(t, planOf(nil, "/w", "/t", nil, nil, false, "").Developer)
}

// A gate plan reads the developer folder, its bundle and the preferences file, and the command env wins over the request env.
func TestGateSandbox_ADeveloperFolderIsReadOnlyInThePlanAndAnEnvInTheRequestCannotChangeIt(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	box.developer = "/Applications/Xcode.app/Contents/Developer"
	dir := t.TempDir()

	_, code := box.run(t.Context(), "p", dir, []string{"true"}, []string{"DEVELOPER_DIR=/elsewhere", "xcrun_db=/elsewhere/db"}, 0)
	require.Zero(t, code)

	assert.Equal(t, box.developer, fake.plan.Developer)
	assert.Subset(t, fake.plan.Read, developerReads(box.developer))
	assert.NotContains(t, fake.plan.Write, box.developer)
	assert.NotContains(t, fake.plan.Write, "/Applications/Xcode.app")

	assert.Equal(t, box.developer, lastValue(fake.env, "DEVELOPER_DIR"), "the engine value comes after the request value, so it wins")

	cache := lastValue(fake.env, "xcrun_db")
	assert.Equal(t, xcrunCache, filepath.Base(cache))
	assert.Contains(t, fake.plan.Write, filepath.Dir(cache), "the cache lies in a folder that the plan already writes")
}

// lastValue returns the value of the last pair of env that sets name: the one that a started process sees.
func lastValue(env []string, name string) string {
	var value string

	for _, pair := range env {
		if v, ok := strings.CutPrefix(pair, name+"="); ok {
			value = v
		}
	}

	return value
}

// A gate on a machine with no developer folder adds no developer path and no env.
func TestGateSandbox_NoDeveloperFolderAddsNothingToThePlanOrTheEnv(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)

	_, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, []string{"A=1"}, 0)
	require.Zero(t, code)

	assert.Empty(t, fake.plan.Developer)
	assert.NotContains(t, fake.plan.Read, xcodePreferences)

	for _, pair := range fake.env {
		assert.NotContains(t, pair, "DEVELOPER_DIR")
		assert.NotContains(t, pair, "xcrun_db")
	}
}
