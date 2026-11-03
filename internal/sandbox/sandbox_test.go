package sandbox

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stub struct {
	name string
	err  error
}

func (s stub) Name() string { return s.name }
func (s stub) Check() error { return s.err }
func (stub) Run(context.Context, Plan, int, Command) (int, error) {
	return 0, nil
}

// recording is an enforcer that keeps what Execute gave it.
type recording struct {
	plan  Plan
	port  int
	cmd   Command
	alive bool
	code  int
	err   error
}

func (*recording) Name() string { return "recording" }
func (*recording) Check() error { return nil }
func (r *recording) Run(ctx context.Context, plan Plan, port int, cmd Command) (int, error) {
	r.plan, r.port, r.cmd = plan, port, cmd

	if port > 0 {
		conn, err := (&net.Dialer{Timeout: testTimeout}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
		r.alive = err == nil

		if err == nil {
			_ = conn.Close()
		}
	}

	return r.code, r.err
}

func TestExecute_APlanWithNoNetworkStartsNoProxyAndLeavesTheEnvironmentAlone(t *testing.T) {
	t.Parallel()

	e := &recording{code: 3}
	env := []string{"PATH=/bin", "HTTPS_PROXY=http://other:1"}

	code, err := Execute(t.Context(), e, Plan{Read: []string{"/usr"}}, Command{Argv: []string{"true"}, Env: env})
	require.NoError(t, err)
	assert.Equal(t, 3, code, "the exit code passes through")
	assert.Zero(t, e.port)
	assert.Equal(t, env, e.cmd.Env)
}

func TestExecute_APlanWithNetworkRunsTheCommandBehindALiveProxyThatStopsAfterwards(t *testing.T) {
	t.Parallel()

	failure := errors.New("the command could not run")
	e := &recording{err: failure}

	_, err := Execute(t.Context(), e, Plan{Network: true, Domains: []string{"a.example"}}, Command{Env: []string{"PATH=/bin"}})
	require.ErrorIs(t, err, failure, "the error of the enforcer passes through")
	assert.True(t, e.alive, "the proxy listened while the command ran")
	assert.Positive(t, e.port)
	assert.Contains(t, e.cmd.Env, "HTTPS_PROXY=http://127.0.0.1:"+strconv.Itoa(e.port))
	assert.Equal(t, []string{"a.example"}, e.plan.Domains)

	_, err = (&net.Dialer{Timeout: testTimeout}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(e.port))
	assert.Error(t, err, "the proxy stopped when the command ended")
}

func TestExecute_AProxyThatCannotStartMeansTheCommandDoesNotRun(t *testing.T) {
	t.Parallel()

	failure := errors.New("no port")
	e := &recording{}

	_, err := execute(t.Context(), e, Plan{Network: true}, Command{}, func(context.Context, []string) (*Proxy, error) { return nil, failure })
	require.ErrorIs(t, err, failure)
	assert.Zero(t, e.port)
	assert.Empty(t, e.cmd.Argv)
}

func TestChoose_AnEmptyOrderNamesTheSystemThatHasNoEnforcer(t *testing.T) {
	t.Parallel()

	_, err := Choose(map[string]Enforcer{}, nil, "auto")
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "this build has no enforcer for")

	_, err = Choose(map[string]Enforcer{"a": stub{name: "a"}}, []string{"a"}, "b")
	assert.ErrorContains(t, err, `names "b", and the enforcers are a`)
}

func TestDescribeAndSystemPaths_NameThePlanAndTheFoldersAProgramNeeds(t *testing.T) {
	t.Parallel()

	line := Plan{Read: []string{"/r"}, Write: []string{"/w"}, Network: true, Domains: []string{"d.example"}}.Describe("fake")
	assert.Equal(t, `sandbox fake plan: {read: ["/r"], write: ["/w"], network: true, domains: ["d.example"]}`, line)

	assert.Contains(t, SystemRead(), "/usr")
	assert.NotContains(t, SystemRead(), "/home")
	assert.Contains(t, SystemWrite(), "/dev/null")
}

func TestAllowed_MatchesExactHostsAndWildcardSubdomainsOnly(t *testing.T) {
	t.Parallel()

	list := []string{"proxy.golang.org", "*.example.org"}

	assert.True(t, allowed(list, "proxy.golang.org:443"))
	assert.True(t, allowed(list, "PROXY.golang.org."))
	assert.True(t, allowed(list, "a.example.org"))
	assert.False(t, allowed(list, "example.org"), "a wildcard does not name its apex")
	assert.False(t, allowed(list, "evilexample.org"))
	assert.False(t, allowed(list, "golang.org"))
	assert.False(t, allowed(nil, "proxy.golang.org"))
}

func TestWithProxy_ReplacesEveryProxyVariableInBothCases(t *testing.T) {
	t.Parallel()

	env := withProxy([]string{"PATH=/bin", "http_proxy=http://other:1", "NO_PROXY=*"}, 4000)

	assert.Contains(t, env, "PATH=/bin")
	assert.Contains(t, env, "HTTPS_PROXY=http://127.0.0.1:4000")
	assert.Contains(t, env, "https_proxy=http://127.0.0.1:4000")
	assert.Contains(t, env, "HTTP_PROXY=http://127.0.0.1:4000")
	assert.Contains(t, env, "http_proxy=http://127.0.0.1:4000")
	assert.NotContains(t, env, "NO_PROXY=*")
	assert.NotContains(t, env, "http_proxy=http://other:1")
}

func TestChoose_AutoTakesTheFirstThatCanRunAndANamedOneMustRun(t *testing.T) {
	t.Parallel()

	down := errors.Join(ErrUnavailable, errors.New("down"))
	table := map[string]Enforcer{"a": stub{name: "a", err: down}, "b": stub{name: "b"}}
	order := []string{"a", "b"}

	got, err := Choose(table, order, "auto")
	require.NoError(t, err)
	assert.Equal(t, "b", got)

	got, err = Choose(table, order, "")
	require.NoError(t, err)
	assert.Equal(t, "b", got)

	_, err = Choose(table, order, "a")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = Choose(table, order, "nope")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = Choose(map[string]Enforcer{"a": stub{name: "a", err: down}}, []string{"a"}, "auto")
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "down")
}
