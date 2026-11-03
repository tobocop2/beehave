package seatbelt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/sandbox"
	"github.com/tobocop2/obee/internal/sandbox/seatbelt"
)

func TestProfile_DeniesByDefaultAndOpensNoNetworkWithoutAPort(t *testing.T) {
	t.Parallel()

	text, defines := seatbelt.Profile(sandbox.Plan{}, 0)

	assert.Contains(t, text, "(deny default)")
	assert.NotContains(t, text, "network", "a plan with no network allows no network operation")
	assert.NotContains(t, text, "unix", "no rule grants a unix socket")
	assert.NotContains(t, strings.Join(defines, " "), "PORT")
}

func TestProfile_ALookupOfASystemServiceNamesTheService(t *testing.T) {
	t.Parallel()

	text, _ := seatbelt.Profile(sandbox.Plan{}, 0)

	assert.NotRegexp(t, `\(allow mach-lookup\s*\)`, text, "a bare mach-lookup rule reaches every service, launchd included")
	assert.Contains(t, text, `(global-name "com.apple.system.opendirectoryd.libinfo")`)
}

func TestProfile_APortOpensTheProxyPortOnLocalhostOnly(t *testing.T) {
	t.Parallel()

	text, defines := seatbelt.Profile(sandbox.Plan{Network: true}, 4321)

	assert.Contains(t, text, `(allow network-outbound (remote ip (string-append "localhost:" (param "PORT"))))`)
	assert.Contains(t, defines, "PORT=4321")
	assert.Equal(t, 1, strings.Count(text, "network"), "one network rule")
}

func TestProfile_APathNeverEntersTheTextOfTheProfile(t *testing.T) {
	t.Parallel()

	hostile := "/tmp/x\")) (allow default) (allow file-write* (subpath \"/\n"
	text, defines := seatbelt.Profile(sandbox.Plan{Read: []string{hostile}, Write: []string{hostile + "w"}}, 0)

	assert.NotContains(t, text, "allow default")
	assert.NotContains(t, text, "/tmp/x")
	assert.Contains(t, defines, "R"+indexOf(t, defines, hostile)+"="+hostile)
	assert.Contains(t, text, `(allow file-read* file-write* (subpath (param "W0")))`)
}

// indexOf returns the number in the name of the read parameter that holds value.
func indexOf(t *testing.T, defines []string, value string) string {
	t.Helper()

	for _, define := range defines {
		if name, got, _ := strings.Cut(define, "="); got == value {
			return strings.TrimPrefix(name, "R")
		}
	}

	require.Fail(t, "no parameter holds the path")

	return ""
}

func TestProfile_AFileIsAllowedByItselfAndAFolderWithWhatLiesBelowIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	text, _ := seatbelt.Profile(sandbox.Plan{Read: []string{file, dir, filepath.Join(dir, "missing")}}, 0)

	assert.Contains(t, text, `(literal (param "R`)
	assert.Contains(t, text, `(subpath (param "R`)
	assert.Equal(t, 1, strings.Count(text, "(literal (param"), "only the file is a literal")
}

func TestProfile_ASymbolicLinkIsFollowedBecauseSeatbeltComparesTheRealPath(t *testing.T) {
	t.Parallel()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(dir, link))

	_, defines := seatbelt.Profile(sandbox.Plan{Write: []string{link, filepath.Join(dir, "absent")}}, 0)

	assert.Contains(t, defines, "W0="+dir)
	assert.Contains(t, defines, "W1="+filepath.Join(dir, "absent"), "a path that does not exist is kept as it is")
}
