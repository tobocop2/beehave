package beadstest_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

const (
	fakeServerName  = "fakedolt"
	clashServerName = "clashdolt"
)

// TestMain lets the test binary play the dolt server. StartDolt runs `<binary> sql-server --host H --port P`.
// The tests pass a link to the test binary named fakedolt, and testscript runs the command of that name: it listens on P until it is killed.
// The command clashdolt ends at its first start, like a server whose port another process took, and listens from its second start.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){fakeServerName: serveFake, clashServerName: serveAfterClash})
}

func serveAfterClash() {
	if os.Mkdir("clashed", 0o700) == nil {
		return
	}

	serveFake()
}

func serveFake() {
	args := os.Args[2:]

	var host, port string

	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--host":
			host = args[i+1]
		case "--port":
			port = args[i+1]
		}
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return
	}

	defer func() { _ = listener.Close() }()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		_ = conn.Close()
	}
}

// fakeServer returns the path of a link, named like the fake server command, to the test binary.
func fakeServer(t *testing.T) string {
	t.Helper()

	return serverLink(t, fakeServerName)
}

func serverLink(t *testing.T, name string) string {
	t.Helper()

	self, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.Symlink(self, link))

	return link
}

// stubTB records what a helper tells a failed test and stops the helper like Fatalf does.
type stubTB struct {
	testing.TB

	message string
}

func (s *stubTB) Fatalf(format string, args ...any) {
	s.message = fmt.Sprintf(format, args...)

	runtime.Goexit()
}

func (s *stubTB) Helper() {}

// failure runs call against a stub and returns what the helper told the test, or an empty text when it did not stop.
// The call runs in its own goroutine, because Goexit ends the goroutine that calls it.
func failure(t *testing.T, call func(testing.TB)) string {
	t.Helper()

	stub := &stubTB{TB: t}
	done := make(chan struct{})

	go func() {
		defer close(done)

		call(stub)
	}()

	<-done

	return stub.message
}

func writeMetadata(t *testing.T, text string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".beads"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte(text), 0o600))

	return dir
}

func TestDatabaseName_ReadsTheNameThatBdWrote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "beads_ct", beadstest.DatabaseName(t, writeMetadata(t, `{"dolt_database":"beads_ct"}`)))
}

func TestDatabaseName_StopsTheTestWhenTheMetadataNamesNoDatabase(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]string{"": "read the metadata", "not json": "names no dolt database", "{}": "names no dolt database"} {
		dir := t.TempDir()
		if text != "" {
			dir = writeMetadata(t, text)
		}

		assert.Contains(t, failure(t, func(tb testing.TB) {
			tb.Helper()
			beadstest.DatabaseName(tb, dir)
		}), want)
	}
}

func TestFreePort_ReturnsAPortThatNothingListensOn(t *testing.T) {
	t.Parallel()

	port := beadstest.FreePort(t)

	assert.Positive(t, port)
	assert.NotContains(t, []int{3306, 3307}, port)
}

func TestBeadsEnv_PointsBdAtThePrivateServerOnly(t *testing.T) {
	t.Parallel()

	env := beadstest.BeadsEnv("/tmp/root", 4242)

	assert.Contains(t, env, "BEADS_DOLT_SERVER_PORT=4242")
	assert.Contains(t, env, "DOLT_ROOT_PATH=/tmp/root/dolt-home")
	assert.Contains(t, env, "BEADS_DIR=", "the variables of the owner are cleared")
}

func TestEnvRunner_RunsTheBinaryUnderTheGivenVariables(t *testing.T) {
	t.Parallel()

	run := beadstest.EnvRunner{Inner: proc.NewExecRunner("env", t.TempDir()), Env: []string{"BEEHAVE_PROBE=yes"}, Binary: "printenv"}

	done := run.Run(t.Context(), []string{"BEEHAVE_PROBE"})

	assert.Zero(t, done.ExitCode)
	assert.Equal(t, "yes\n", done.Stdout)
}

func TestStartDolt_StartsAServerOnAFreePortAndStopsItAtTheEndOfTheTest(t *testing.T) {
	t.Parallel()

	var port int

	// Cleanups run last in first out: this one runs after the cleanup of StartDolt has stopped the server.
	t.Cleanup(func() {
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
		}

		assert.Error(t, err, "the server stops when its test ends")
	})

	root := t.TempDir()
	port = beadstest.StartDolt(t, root, fakeServer(t))

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.DirExists(t, filepath.Join(root, "dolt-home"))
}

func TestStartDolt_StartsAgainOnANewPortWhenTheServerEndsAtOnce(t *testing.T) {
	t.Parallel()

	port := beadstest.StartDolt(t, t.TempDir(), serverLink(t, clashServerName))

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestStartDolt_StopsTheTestWhenTheServerEndsOnEveryAttempt(t *testing.T) {
	t.Parallel()

	assert.Contains(t, failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.StartDolt(tb, t.TempDir(), "true")
	}), "did not start")
}

func TestStartDolt_StopsTheTestWhenItsFoldersCannotBeMade(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "dolt-home"), 0o750))

	assert.Contains(t, failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.StartDolt(tb, root, os.Args[0])
	}), "make the folder")
}
