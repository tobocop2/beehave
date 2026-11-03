package srt_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/srt"
)

func plan() agent.SandboxPlan {
	return agent.SandboxPlan{
		Work: "/home/o/w", GitDirs: []string{"/home/o/repo/.git"}, Read: []string{"/home/o/go"},
		Deny: []string{"/home/o/.ssh"}, DenyPatterns: []string{"/home/o/w/.env*", "/home/o/w/**/.env*"},
		DenyWrite: []string{"/home/o/w/.git"}, Network: []string{"proxy.golang.org"}, AllowWrite: []string{"/home/o/w/out"},
	}
}

type decoded struct {
	Network struct {
		AllowedDomains []string `json:"allowedDomains"`
	} `json:"network"`
	Filesystem struct {
		DenyRead   []string `json:"denyRead"`
		AllowRead  []string `json:"allowRead"`
		DenyWrite  []string `json:"denyWrite"`
		AllowWrite []string `json:"allowWrite"`
	} `json:"filesystem"`
}

func settings(t *testing.T, p agent.SandboxPlan) decoded {
	t.Helper()

	var out decoded

	require.NoError(t, json.Unmarshal(srt.Settings(p, "/home/o", "/tmp"), &out))

	return out
}

func TestSettings_TheHomeFolderIsDeniedForReadAndOnlyThePlanReadsAreAllowed(t *testing.T) {
	t.Parallel()

	got := settings(t, plan())

	assert.Equal(t, []string{"/home/o", "/home/o/.ssh", "/home/o/w/.env*", "/home/o/w/**/.env*"}, got.Filesystem.DenyRead)
	assert.Equal(t, []string{"/home/o/w", "/home/o/repo/.git", "/home/o/go"}, got.Filesystem.AllowRead)
}

func TestSettings_WritesAreDeniedByDefaultAndTheWorkFolderTheTempFolderAndThePlanPathsAreAllowed(t *testing.T) {
	t.Parallel()

	got := settings(t, plan())

	assert.Equal(t, []string{"/home/o/w", "/tmp", "/home/o/w/out"}, got.Filesystem.AllowWrite)
	assert.Equal(t, []string{"/home/o/.ssh", "/home/o/w/.git"}, got.Filesystem.DenyWrite, "a glob pattern is a read deny only: Linux takes literal paths for a write deny")
}

func TestSettings_TheNetworkListIsTheOnlyWayOut(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"proxy.golang.org"}, settings(t, plan()).Network.AllowedDomains)

	empty := plan()
	empty.Network = nil

	raw := string(srt.Settings(empty, "/home/o", "/tmp"))
	assert.Contains(t, raw, `"allowedDomains":[]`, "an empty list is [] and never null: null would not close the network")
}

func TestSettings_EveryKeyThatSrtRequiresIsPresentAndNeverNull(t *testing.T) {
	t.Parallel()

	var got map[string]map[string]any

	require.NoError(t, json.Unmarshal(srt.Settings(agent.SandboxPlan{Work: "/w"}, "/home/o", "/tmp"), &got))

	// srt 0.0.81, dist/sandbox/sandbox-config.js:584-593 and 807-838: these keys have no .optional().
	for block, keys := range map[string][]string{
		"network":    {"allowedDomains", "deniedDomains"},
		"filesystem": {"denyRead", "allowWrite", "denyWrite"},
	} {
		for _, key := range keys {
			assert.IsType(t, []any{}, got[block][key], "%s.%s is a list", block, key)
		}
	}
}

func TestWrap_TheCommandRunsThroughSrtWithTheSettingsFileAndTheCleanupRemovesTheFile(t *testing.T) {
	t.Parallel()

	args, cleanup, err := srt.Wrap(plan(), "/home/o", "make check")

	require.NoError(t, err)
	require.Len(t, args, 4)
	assert.Equal(t, []string{"--settings", args[1], "-c", "make check"}, args)

	data, err := os.ReadFile(args[1])
	require.NoError(t, err)
	assert.JSONEq(t, string(srt.Settings(plan(), "/home/o", filepath.Clean(os.TempDir()))), string(data), "the file holds the settings of the plan")

	cleanup()

	_, err = os.Stat(args[1])
	assert.ErrorIs(t, err, os.ErrNotExist, "the settings file is gone after the run")
}

func TestMissing_AMissingProgramGivesANoteThatNamesTheInstallAndAFoundOneGivesNone(t *testing.T) {
	t.Parallel()

	gone := func(string) (string, error) { return "", errors.New("not found") }
	found := func(string) (string, error) { return "/bin/srt", nil }

	assert.Contains(t, srt.Missing(gone), "npm install -g @anthropic-ai/sandbox-runtime")
	assert.Contains(t, srt.Missing(gone), "sandbox = \"off\"")
	assert.Contains(t, srt.Missing(gone), "Node 22.12 or later")
	assert.Contains(t, srt.Missing(gone), "kernel.apparmor_restrict_unprivileged_userns=0")
	assert.Empty(t, srt.Missing(found))
}
