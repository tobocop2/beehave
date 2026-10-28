package store_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
)

// This file is the contract test of the lease store. The same scenarios run against the fake bd of storetest, which keeps the
// fake honest, and against a real bd 1.3.2 with a private dolt server, which keeps the code honest. The real run needs
// OBEE_BD_132, the path of a bd 1.3.2 binary, and `dolt` on PATH. Without the variable the real run is skipped.

const (
	binaryVariable = "OBEE_BD_132"
	realPrefix     = "ct"
	serverWait     = 30 * time.Second
	leaseSeconds   = 60
)

// liveServerPorts are the ports of a server that the owner may run. No test may use them.
var liveServerPorts = []int{0, 3306, 3307}

// leaseBackend is one bd the scenarios run against.
type leaseBackend struct {
	run    store.Runner
	prefix string
	beads  *store.BeadsStore
	real   bool
}

func fakeBackend(t *testing.T) leaseBackend {
	t.Helper()

	bd := newFakeBd(t)

	return leaseBackend{run: bd, prefix: "p", beads: beadsOver(t, bd)}
}

// realBackend starts a private dolt server and a beads database on it. It skips when OBEE_BD_132 is not set.
// Every command runs through env(1), so the test never changes the environment of the test process and can run in parallel.
func realBackend(t *testing.T) leaseBackend {
	t.Helper()

	binary := config.ProcessEnv()[binaryVariable]
	if binary == "" {
		t.Skip(binaryVariable + " is not set: the real bd contract test needs a bd 1.3.2 binary")
	}

	root := t.TempDir()
	port := startDolt(t, root)
	database := filepath.Join(root, "db")
	require.NoError(t, os.Mkdir(database, 0o750))

	run := envRunner{inner: proc.NewExecRunner("env", database), env: beadsEnv(root, port), binary: binary}
	for _, step := range [][]string{{"git", "init", "-q"}, {binary, "init", "--prefix", realPrefix, "--server", "--external", "--server-port", strconv.Itoa(port)}} {
		done := envRunner{inner: run.inner, env: run.env, binary: step[0]}.Run(t.Context(), step[1:])
		require.Zero(t, done.ExitCode, "%v: %s", step, done.Stderr)
	}

	require.Contains(t, run.Run(t.Context(), []string{"--version"}).Stdout, "bd version 1.3.2")
	require.NoError(t, store.RequireBd(t.Context(), run))

	beads, err := store.NewBeadsStore(run, realPrefix)
	require.NoError(t, err)

	return leaseBackend{run: run, prefix: realPrefix, beads: beads, real: true}
}

// beadsEnv keeps the real bd away from every database but the private one.
func beadsEnv(root string, port int) []string {
	return []string{
		"BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_HOST=", "BEADS_DOLT_SHARED_SERVER=",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(port), "BD_ACTOR=contract-test", "DOLT_ROOT_PATH=" + filepath.Join(root, "dolt-home"),
	}
}

// envRunner runs a binary under env(1) with the given variables set.
type envRunner struct {
	inner  proc.ExecRunner
	env    []string
	binary string
}

func (r envRunner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.inner.Run(ctx, slices.Concat(r.env, []string{r.binary}, args))
}

// startDolt runs a dolt server on a free port that is not a live port and stops it when the test ends.
func startDolt(t *testing.T, root string) int {
	t.Helper()

	port := freePort(t)
	require.NotContains(t, liveServerPorts, port, "a test may never use the live server")

	home := filepath.Join(root, "dolt-home")
	data := filepath.Join(root, "server")
	require.NoError(t, os.Mkdir(home, 0o750))
	require.NoError(t, os.Mkdir(data, 0o750))

	args := []string{"DOLT_ROOT_PATH=" + home, "dolt", "sql-server", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	cmd := proc.GroupCommand(t.Context(), "env", args, data)
	require.NoError(t, cmd.Err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		assert.NoError(t, proc.KillGroup(cmd))
		assert.Error(t, cmd.Wait(), "a killed server does not exit cleanly")
	})

	waitForServer(t, port)

	return port
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { require.NoError(t, listener.Close()) }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return addr.Port
}

func waitForServer(t *testing.T, port int) {
	t.Helper()

	require.Eventually(t, func() bool {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return false
		}

		return conn.Close() == nil
	}, serverWait, 250*time.Millisecond, "the private dolt server did not start")
}

func rowOf(b leaseBackend, item string) string { return b.prefix + "-lock-gh-" + item }

// plant makes a claim row and applies the bd update arguments to it, the way an old obee or a person would leave it.
func (b leaseBackend) plant(t *testing.T, item string, update ...string) {
	t.Helper()

	id := rowOf(b, item)
	made := b.run.Run(t.Context(), []string{"create", "--id", id, "--title", "lock:gh-" + item, "--type", "task", "--priority", "3", "--silent", "--description", "planted"})
	require.Zero(t, made.ExitCode, made.Stderr)

	if len(update) > 0 {
		done := b.run.Run(t.Context(), append([]string{"update", id}, update...))
		require.Zero(t, done.ExitCode, done.Stderr)
	}
}

func take(item, holder, nonce string) model.ClaimRow {
	return model.ClaimRow{Item: item, Holder: holder, Nonce: nonce, Lease: leaseSeconds}
}

func (b leaseBackend) holder(t *testing.T, item string) model.RowState {
	t.Helper()

	state, err := b.beads.Row(t.Context(), item)
	require.NoError(t, err)

	return state
}

// leaseScenarios lists the scenarios. Each one uses its own item, so one database serves them all.
var leaseScenarios = map[string]func(*testing.T, leaseBackend){
	"a take shows its holder and token apart":                 scenarioTake,
	"a second take is refused and changes nothing":            scenarioLostTake,
	"a lease is stored only by the take that holds the row":   scenarioStoreLease,
	"a release reopens the row so the next take can claim it": scenarioReleaseThenTake,
	"a release by another take changes nothing":               scenarioReleaseByStranger,
	"a stale guard of a released take fails after a retake":   scenarioRetake,
	"a row left closed with an assignee is claimable again":   scenarioStaleClosed,
	"a row left open with an assignee is claimable again":     scenarioStaleOpen,
	"a release of a missing row is a release":                 scenarioMissing,
	"a take with the old bd is refused before it writes":      scenarioOldBd,
	"a heartbeat-free take of two holders keeps one holder":   scenarioHeldRows,
}

type backendKind struct {
	name   string
	shared bool
	build  func(*testing.T) leaseBackend
}

func TestLeaseContract(t *testing.T) {
	t.Parallel()

	for _, kind := range []backendKind{{"fake bd", false, fakeBackend}, {"real bd 1.3.2", true, realBackend}} {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			var shared leaseBackend

			if kind.shared {
				shared = kind.build(t)
			}

			for scenario, run := range leaseScenarios {
				t.Run(scenario, func(t *testing.T) {
					t.Parallel()

					b := shared
					if !kind.shared {
						b = kind.build(t)
					}

					run(t, b)
				})
			}

			if shared.real {
				t.Run("two takes at the same moment give one holder", func(t *testing.T) {
					t.Parallel()
					scenarioRace(t, shared)
				})
			}
		})
	}
}

func scenarioTake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("11", "session-a", "TOKENA")))

	state := b.holder(t, "11")
	assert.Equal(t, model.RowState{Exists: true, Held: true, Holder: "session-a", Nonce: "TOKENA"}, state)
}

func scenarioLostTake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("12", "session-a", "TOKENA")))
	require.NoError(t, b.beads.TryHold(t.Context(), take("12", "session-b", "TOKENB")), "a lost take is no error: the caller reads the row")

	assert.Equal(t, "TOKENA", b.holder(t, "12").Nonce)
	assert.Equal(t, "session-a", b.holder(t, "12").Holder)
}

func scenarioStoreLease(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("13", "session-a", "TOKENA")))
	require.NoError(t, b.beads.StoreLease(t.Context(), take("13", "session-a", "TOKENA")))
	require.ErrorIs(t, b.beads.StoreLease(t.Context(), take("13", "session-a", "OTHER")), model.ErrRowMoved)

	held, err := b.beads.HeldRows(t.Context())
	require.NoError(t, err)
	assert.Contains(t, held, model.ClaimRow{Item: "13", Holder: "session-a", Nonce: "TOKENA", Lease: leaseSeconds})
}

func scenarioReleaseThenTake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("14", "session-a", "TOKENA")))
	require.NoError(t, b.beads.Free(t.Context(), take("14", "session-a", "TOKENA")))
	assert.False(t, b.holder(t, "14").Held)
	require.NoError(t, b.beads.Free(t.Context(), take("14", "session-a", "TOKENA")), "a second release of a free row is no error")

	require.NoError(t, b.beads.TryHold(t.Context(), take("14", "session-b", "TOKENB")))
	assert.Equal(t, "session-b", b.holder(t, "14").Holder, "a release must leave the row claimable")
}

func scenarioReleaseByStranger(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("15", "session-a", "TOKENA")))
	require.ErrorIs(t, b.beads.Free(t.Context(), take("15", "session-b", "TOKENB")), model.ErrRowMoved)
	assert.Equal(t, "session-a", b.holder(t, "15").Holder)
}

func scenarioRetake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("16", "session-a", "TOKEN1")))
	require.NoError(t, b.beads.Free(t.Context(), take("16", "session-a", "TOKEN1")))
	require.NoError(t, b.beads.TryHold(t.Context(), take("16", "session-a", "TOKEN2")))

	require.ErrorIs(t, b.beads.Free(t.Context(), take("16", "session-a", "TOKEN1")), model.ErrRowMoved, "the same holder with an old token")
	require.ErrorIs(t, b.beads.StoreLease(t.Context(), take("16", "session-a", "TOKEN1")), model.ErrRowMoved)
	assert.Equal(t, "TOKEN2", b.holder(t, "16").Nonce)
}

func scenarioStaleClosed(t *testing.T, b leaseBackend) {
	t.Helper()

	b.plant(t, "17", "--assignee", "old#TOKEN", "--status", "closed")

	require.NoError(t, b.beads.TryHold(t.Context(), take("17", "session-a", "TOKENA")))
	assert.Equal(t, "session-a", b.holder(t, "17").Holder)
}

func scenarioStaleOpen(t *testing.T, b leaseBackend) {
	t.Helper()

	b.plant(t, "18", "--assignee", "old#TOKEN")

	require.NoError(t, b.beads.TryHold(t.Context(), take("18", "session-a", "TOKENA")))
	assert.Equal(t, "session-a", b.holder(t, "18").Holder)
}

func scenarioMissing(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.Free(t.Context(), take("19", "session-a", "TOKENA")))
	assert.Equal(t, model.RowState{}, b.holder(t, "19"))
}

func scenarioOldBd(t *testing.T, b leaseBackend) {
	t.Helper()

	old := &oldBd{inner: b.run}
	beads, err := store.NewBeadsStore(old, b.prefix)
	require.NoError(t, err)

	require.ErrorContains(t, beads.TryHold(t.Context(), take("20", "session-a", "TOKENA")), "obee needs bd 1.3.2 or newer for claims; found 0.56.1")
	assert.Equal(t, 1, old.calls, "only bd --version ran")
}

func scenarioHeldRows(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), take("21", "session-a", "TOKENA")))
	require.NoError(t, b.beads.TryHold(t.Context(), take("22", "session-b", "TOKENB")))

	held, err := b.beads.HeldRows(t.Context())
	require.NoError(t, err)

	var holders []string

	for _, row := range held {
		if row.Item == "21" || row.Item == "22" {
			holders = append(holders, row.Item+":"+row.Holder)
		}
	}

	assert.ElementsMatch(t, []string{"21:session-a", "22:session-b"}, holders)
}

func scenarioRace(t *testing.T, b leaseBackend) {
	t.Helper()

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i, who := range []string{"a", "b"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = b.beads.TryHold(t.Context(), take("30", "session-"+who, "TOKEN"+strings.ToUpper(who)))
		}()
	}

	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	state := b.holder(t, "30")
	assert.True(t, state.Held)
	assert.Equal(t, strings.ToUpper(strings.TrimPrefix(state.Holder, "session-")), strings.TrimPrefix(state.Nonce, "TOKEN"), "the token belongs to the holder")
}

// oldBd answers --version like bd 0.56.1 and counts the commands it gets.
type oldBd struct {
	inner store.Runner
	calls int
}

func (o *oldBd) Run(ctx context.Context, args []string) model.CommandResult {
	o.calls++

	if args[0] == "--version" {
		return model.CommandResult{Stdout: "bd version 0.56.1 (dev)\n"}
	}

	return o.inner.Run(ctx, args)
}
