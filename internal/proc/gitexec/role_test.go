package gitexec_test

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// fill runs git credential fill in a folder with the role environment and the given input, and returns the output and the error.
func fill(t *testing.T, env []string, input string) (string, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", "credential", "fill")
	command.Env = env
	command.Dir = filepath.Clean(t.TempDir())
	command.Stdin = strings.NewReader(input)

	out, err := command.Output()

	return string(out), err
}

func TestRoleEnv_GitAsksTheCredentialHelperOfTheCallAndTheTokenGoesByTheEnvironment(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "")

	env := gitexec.RoleEnv("role-token", "github.com", map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/owner"})
	require.NotContains(t, strings.Join(env, "\n"), "HOME=")

	out, err := fill(t, env, "protocol=https\nhost=github.com\n\n")

	require.NoError(t, err)
	require.Contains(t, out, "username=x-access-token\n")
	require.Contains(t, out, "password=role-token\n")
}

func TestRoleEnv_TheHelperAnswersForTheForgeHostAndHTTPSOnlyAndGivesNothingForAnyOtherHostOrProtocol(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "")

	env := gitexec.RoleEnv("role-token", "github.com", map[string]string{"PATH": "/usr/bin:/bin"})

	for name, input := range map[string]string{
		"another host":                  "protocol=https\nhost=evil.example\n\n",
		"the same host on another port": "protocol=https\nhost=github.com:8443\n\n",
		"http":                          "protocol=http\nhost=github.com\n\n",
		"ssh":                           "protocol=ssh\nhost=github.com\n\n",
		"no host":                       "protocol=https\n\n",
		"no protocol":                   "host=github.com\n\n",
		"empty input":                   "\n",
	} {
		out, err := fill(t, env, input)

		require.Error(t, err, name)
		require.NotContains(t, out, "role-token", name)
	}
}

func TestRoleEnv_AUrlRewriteInTheConfigOfTheRepositorySendsNoTokenToTheOtherHost(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "")

	var (
		mu      sync.Mutex
		headers []string
		hits    int
	)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		hits++
		headers = append(headers, r.Header.Get("Authorization"))

		w.Header().Set("WWW-Authenticate", `Basic realm="other"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(other.Close)

	repo := filepath.Clean(t.TempDir())
	runner := func(env []string, args ...string) error {
		command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test runs git on its own temporary folder
		command.Env = env
		command.Dir = repo

		return command.Run()
	}

	env := gitexec.RoleEnv("role-token", "github.com", map[string]string{"PATH": "/usr/bin:/bin"})
	require.NoError(t, runner(env, "init", "-q"))
	require.NoError(t, runner(env, "config", "url."+other.URL+"/.insteadOf", "https://github.com/"))
	require.Error(t, runner(env, "ls-remote", "https://github.com/o/r.git"), "the other host asks for a login and git has none to give")

	mu.Lock()
	defer mu.Unlock()

	assert.Positive(t, hits, "the call went to the other host, so the rewrite was in force")

	for _, header := range headers {
		assert.Empty(t, header, "no token went to the other host")
	}
}

func TestRoleEnv_TheGivenPairsComeLast(t *testing.T) {
	t.Parallel()

	env := gitexec.RoleEnv("role-token", "github.com", nil, "GIT_DIR=/r")

	require.Equal(t, "GIT_DIR=/r", env[len(env)-1])
}
