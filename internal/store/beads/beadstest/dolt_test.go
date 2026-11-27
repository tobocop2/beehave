package beadstest_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/store/beads/beadstest"
)

const (
	fakeServerName  = "fakedolt"
	clashServerName = "clashdolt"
	squatServerName = "squatdolt"
	chattyName      = "chattydolt"
	silentName      = "silentdolt"
	wroteFile       = "wrote"

	squatLife = time.Second
)

// TestMain lets the test binary play the dolt server. StartDolt runs `<binary> sql-server --host H --port P`.
// The tests pass a link to the test binary named fakedolt, and testscript runs the command of that name: it listens on P until it is killed.
// The command clashdolt ends at its first start, like a server whose port another process took, and listens from its second start.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){fakeServerName: serveFake, clashServerName: serveAfterClash, squatServerName: serveAsSquatter, chattyName: serveChatty, silentName: serveSilent})
}

// serveAsSquatter plays a process that holds the port at the first start and is not the server: it listens, answers one connection, and ends without the ready line.
// Its second start is the real fake server.
func serveAsSquatter() {
	if os.Mkdir("clashed", 0o700) != nil {
		serveFake()

		return
	}

	args := os.Args[2:]

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(args[1], args[3]))
	if err != nil {
		return
	}

	defer func() { _ = listener.Close() }()

	time.AfterFunc(squatLife, func() { _ = listener.Close() }) // a start that waits for the ready line never connects

	if conn, err := listener.Accept(); err == nil {
		_ = conn.Close()
	}
}

func serveAfterClash() {
	if os.Mkdir("clashed", 0o700) == nil {
		return
	}

	serveFake()
}

func serveFake() {
	serve(func() error { return say(beadstest.ReadyLine + "\n") }) // the server prints its ready line like dolt does
}

// serveChatty plays a server that prints other output in several writes before the ready line, splits the line across two writes,
// and writes again after it, like a real server that logs while it starts. It creates the file wrote when its last write is done.
func serveChatty() {
	const (
		head  = "Server ready. Acc"
		pause = 50 * time.Millisecond // a pause keeps the writes from merging into one read
	)

	serve(func() error {
		for _, text := range []string{"starting\n", "loading the databases\n", head, beadstest.ReadyLine[len(head):] + "\n", "serving\n", "still serving\n"} {
			if err := say(text); err != nil {
				return err
			}

			time.Sleep(pause)
		}

		return os.WriteFile(wroteFile, nil, 0o600)
	})
}

// serveSilent plays a server that lives and prints nothing: it listens like serveFake and never announces.
func serveSilent() {
	serve(func() error { return nil })
}

func say(text string) error {
	_, err := syscall.Write(syscall.Stderr, []byte(text))

	return err
}

// serve listens on the host and port of its arguments, calls announce, and accepts connections until it is killed.
func serve(announce func() error) {
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

	if announce() != nil {
		return
	}

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

func (s *stubTB) Skip(args ...any) {
	s.message = "skipped: " + fmt.Sprint(args...)

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

	run := beadstest.EnvRunner{Inner: proc.NewExecRunner("env", t.TempDir()), Env: []string{"BEEHEST_PROBE=yes"}, Binary: "printenv"}

	done := run.Run(t.Context(), []string{"BEEHEST_PROBE"})

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

// The squatter answers the first connection and ends. A start that counts a connection as ready returns its port, and the next dial is refused.
func TestStartDolt_DoesNotTakeAnAnsweringPortForItsOwnServer(t *testing.T) {
	t.Parallel()

	port := beadstest.StartDolt(t, t.TempDir(), serverLink(t, squatServerName))

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

// The server prints other output in several writes, splits the ready line across two writes, and writes after it.
// StartDolt returns the port of the server, and the file wrote shows that the writes after the line were made before the server stopped.
func TestStartDolt_FindsTheReadyLineInTheOutputOfAServerThatLogsWhileItStarts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	port := beadstest.StartDolt(t, root, serverLink(t, chattyName))

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	assert.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(root, "server", wroteFile))

		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the server writes after the ready line")
}

// The server stays alive and prints nothing. Only the bound of the start ends the wait, so the test fails if the bound is missing.
func TestStartDoltWithin_StopsTheTestWhenALiveServerNeverPrintsTheReadyLine(t *testing.T) {
	t.Parallel()

	message := failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.StartDoltWithin(tb, t.TempDir(), serverLink(t, silentName), 200*time.Millisecond)
	})

	assert.Contains(t, message, "did not start")
	assert.Contains(t, message, beadstest.ReadyLine)
}

func TestRealBd_ReturnsTheBinaryWhenDoltIsOnThePath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "dolt"), "#!/bin/sh\n"))
	t.Setenv("PATH", dir)

	assert.Equal(t, "/bin/bd", beadstest.RealBd(t, map[string]string{beadstest.BinaryVariable: "/bin/bd", "CI": "true"}))
}

func TestRealBd_StopsTheTestWhenDoltIsNotOnThePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	assert.Contains(t, failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.RealBd(tb, map[string]string{beadstest.BinaryVariable: "/bin/bd"})
	}), "dolt is not on PATH")
}

func TestRealBd_SkipsOnAMachineWithoutTheBinary(t *testing.T) {
	t.Parallel()

	assert.Contains(t, failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.RealBd(tb, map[string]string{})
	}), "skipped: BEEHEST_BD_132 is not set")
}

func TestRealBd_StopsTheTestInCIWithoutTheBinary(t *testing.T) {
	t.Parallel()

	assert.Contains(t, failure(t, func(tb testing.TB) {
		tb.Helper()
		beadstest.RealBd(tb, map[string]string{"CI": "true"})
	}), "is not set in CI")
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
