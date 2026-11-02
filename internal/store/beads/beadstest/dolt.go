package beadstest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// The helpers in this file start a private dolt server for the contract tests that run against a real bd.
const (
	serverWait  = 30 * time.Second
	serverPoll  = 250 * time.Millisecond
	dialTimeout = time.Second
	dirMode     = 0o750

	startAttempts = 3

	// The ports of a server that the owner may run. No test may use them.
	livePortA = 3306
	livePortB = 3307
)

// check stops the test with the message when ok is false. It is the one place where these helpers stop a test.
func check(tb testing.TB, ok bool, format string, args ...any) {
	tb.Helper()

	if !ok {
		tb.Fatalf(format, args...)
	}
}

// EnvRunner runs a binary under env(1) with the given variables set, so a test never changes the environment of the test process.
type EnvRunner struct {
	Inner  proc.ExecRunner
	Env    []string
	Binary string
}

// Run runs Binary with args. Inner must run env(1).
func (r EnvRunner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.Inner.Run(ctx, slices.Concat(r.Env, []string{r.Binary}, args))
}

// BeadsEnv keeps the real bd away from every database but the private one on port.
func BeadsEnv(root string, port int) []string {
	return []string{
		"BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_HOST=", "BEADS_DOLT_SHARED_SERVER=",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(port), "BD_ACTOR=contract-test", "DOLT_ROOT_PATH=" + filepath.Join(root, "dolt-home"),
	}
}

// DatabaseName reads the name of the dolt database that bd init made in dir.
func DatabaseName(tb testing.TB, dir string) string {
	tb.Helper()

	data, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json")) //nolint:gosec // G304: dir is the temp folder of the test
	check(tb, err == nil, "read the metadata of the beads database: %v", err)

	var meta struct {
		Database string `json:"dolt_database"`
	}

	err = json.Unmarshal(data, &meta)
	check(tb, err == nil && meta.Database != "", "the metadata of the beads database names no dolt database: %v", err)

	return meta.Database
}

// FreePort returns a port that nothing listens on.
func FreePort(tb testing.TB) int {
	tb.Helper()

	listener, err := (&net.ListenConfig{}).Listen(tb.Context(), "tcp", "127.0.0.1:0")
	check(tb, err == nil, "find a free port: %v", err)

	defer func() { _ = listener.Close() }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	check(tb, ok, "the address of the listener is %T, not a TCP address", listener.Addr())

	return addr.Port
}

// StartDolt runs the dolt server binary on a free port that is not a live port, with its home and data folders under root,
// and stops the server group when the test ends. It returns when the server accepts connections.
//
// Another process can take the free port before the server binds it. The server then ends, and StartDolt starts it again on a new port, up to startAttempts times.
func StartDolt(tb testing.TB, root, binary string) int {
	tb.Helper()

	home, data := serverFolders(tb, root)

	var (
		port int
		ok   bool
	)

	for attempt := 0; attempt < startAttempts && !ok; attempt++ {
		port, ok = startOnFreePort(tb, home, data, binary)
	}

	check(tb, ok, "the private dolt server did not start")

	return port
}

// startOnFreePort starts the server on a free port and reports whether it accepts connections before it ends or the wait is over.
func startOnFreePort(tb testing.TB, home, data, binary string) (int, bool) {
	tb.Helper()

	port := FreePort(tb)
	check(tb, port != livePortA && port != livePortB, "port %d belongs to the live server: a test may never use it", port)

	cmd := launch(tb, home, data, binary, port)
	ctx, done := watchServer(tb, cmd)

	tb.Cleanup(func() { stopServer(tb, cmd, done) })

	ok := reachable(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), serverWait)
	if !ok {
		stopServer(tb, cmd, done) // a server that never answered must not outlive its attempt
	}

	return port, ok
}

// launch starts the server binary on the port in its own process group.
func launch(tb testing.TB, home, data, binary string, port int) *exec.Cmd {
	tb.Helper()

	args := []string{"DOLT_ROOT_PATH=" + home, binary, "sql-server", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}

	cmd := proc.GroupCommand(tb.Context(), "env", args, data)
	check(tb, cmd.Err == nil, "prepare the private server: %v", cmd.Err)

	err := cmd.Start()
	check(tb, err == nil, "start the private server: %v", err)

	return cmd
}

// watchServer waits for the server in the background. The context ends, and done closes, when the server ends.
func watchServer(tb testing.TB, cmd *exec.Cmd) (context.Context, <-chan struct{}) {
	tb.Helper()

	ctx, ended := context.WithCancel(tb.Context())
	done := make(chan struct{})

	go func() {
		if err := cmd.Wait(); err != nil {
			tb.Logf("the private server ended: %v", err) // a killed server does not exit cleanly
		}

		close(done)
		ended()
	}()

	return ctx, done
}

// serverFolders makes the home folder and the data folder of the private server under root.
func serverFolders(tb testing.TB, root string) (home, data string) {
	tb.Helper()

	home, data = filepath.Join(root, "dolt-home"), filepath.Join(root, "server")

	for _, dir := range []string{home, data} {
		check(tb, os.Mkdir(dir, dirMode) == nil, "make the folder %s of the private server", dir)
	}

	return home, data
}

// stopServer kills the group of the server and waits for it.
// A group that already exited reads as ESRCH, or as EPERM on macOS when only a zombie is left: that is a stopped server, not a failure.
func stopServer(tb testing.TB, cmd *exec.Cmd, done <-chan struct{}) {
	tb.Helper()

	killed := proc.KillGroup(cmd)

	<-done

	check(tb, killed == nil || errors.Is(killed, syscall.ESRCH) || errors.Is(killed, syscall.EPERM), "stop the private server: %v", killed)
}

// reachable polls until a connection to the address works, and reports whether one did within wait or before ctx ends.
func reachable(ctx context.Context, address string, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(serverPoll) {
		conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()

			return true
		}
	}

	return false
}
