package credential_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/credential"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
)

// home makes a home folder with a credential folder of mode 0700 and returns the home and an environment that names it.
func home(t *testing.T) (string, map[string]string) {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	folder := filepath.Join(dir, ".config", "beehest", "credentials")
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, exectest.ChmodFolder(folder, 0o700))

	return dir, map[string]string{"HOME": dir, "PATH": "/usr/bin:/bin", "USER": "u"}
}

func tokenFile(t *testing.T, dir, text string, loose bool) string {
	t.Helper()

	path := filepath.Join(dir, ".config", "beehest", "credentials", "w.token")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	if loose {
		require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // the test makes a loose file, which the loader refuses
	}

	return path
}

func TestLoad_AFileInTheCredentialFolderGivesItsTrimmedText(t *testing.T) {
	t.Parallel()

	dir, env := home(t)
	tokenFile(t, dir, "tok-1\n", false)

	got, err := credential.Load(t.Context(), model.Credential{File: "~/.config/beehest/credentials/w.token"}, env)
	require.NoError(t, err)
	require.Equal(t, "tok-1", got)

	got, err = credential.Load(t.Context(), model.Credential{File: filepath.Join(dir, ".config/beehest/credentials/w.token")}, env)
	require.NoError(t, err)
	require.Equal(t, "tok-1", got)
}

func TestLoad_AFileIsRefusedForItsModeItsPlaceItsOwnerItsKindOrItsText(t *testing.T) {
	t.Parallel()

	dir, env := home(t)
	path := tokenFile(t, dir, "tok-1", true)
	load := func(file string, env map[string]string) error {
		_, err := credential.Load(t.Context(), model.Credential{File: file}, env)

		return err
	}

	require.ErrorContains(t, load(path, env), "has mode 644, want 600")

	require.NoError(t, os.Chmod(path, os.FileMode(0o600)))
	require.NoError(t, exectest.ChmodFolder(filepath.Dir(path), 0o755))
	require.ErrorContains(t, load(path, env), "want 700")
	require.NoError(t, exectest.ChmodFolder(filepath.Dir(path), 0o700))

	outside := filepath.Join(dir, "other.token")
	require.NoError(t, os.WriteFile(outside, []byte("tok-1"), 0o600))
	require.ErrorContains(t, load(outside, env), "is outside")

	require.ErrorContains(t, load(filepath.Join(dir, "missing.token"), env), "the credential file")
	require.ErrorContains(t, load(path, map[string]string{}), "needs the HOME variable")
	require.ErrorContains(t, load(filepath.Dir(path), env), "not the kind of entry")

	empty := model.Credential{File: path}
	require.NoError(t, os.WriteFile(path, []byte(" \n"), 0o600))
	_, err := credential.Load(t.Context(), empty, env)
	require.ErrorContains(t, err, "empty token")

	require.NoError(t, os.WriteFile(path, []byte("a\nb"), 0o600))
	_, err = credential.Load(t.Context(), empty, env)
	require.ErrorContains(t, err, "more than one line")

	_, err = credential.Load(t.Context(), model.Credential{File: path}, map[string]string{"HOME": filepath.Join(dir, "nohome")})
	require.ErrorContains(t, err, "the credential")
}

func TestLoad_ACommandGivesItsOutputAndSeesOnlyPathHomeAndUser(t *testing.T) {
	_, env := home(t)
	t.Setenv("BEEHEST_PLANTED", "leak")

	got, err := credential.Load(t.Context(), model.Credential{Command: []string{"sh", "-c", `echo "$USER-${BEEHEST_PLANTED:-none}"`}}, env)
	require.NoError(t, err)
	require.Equal(t, "u-none", got)

	_, err = credential.Load(t.Context(), model.Credential{Command: []string{"sh", "-c", "exit 3"}}, env)
	require.ErrorContains(t, err, "exited with code 3")

	_, err = credential.Load(t.Context(), model.Credential{Command: []string{"sh", "-c", "true"}}, env)
	require.ErrorContains(t, err, "empty token")
}
