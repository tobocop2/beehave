package beads_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// This file is the contract test of the claim store. The same scenarios run against the fake bd of beadstest, which keeps the
// fake honest, and against a real bd 1.3.2 with a private dolt server, which keeps the code honest. The real run needs
// BEEHAVE_BD_132, the path of a bd 1.3.2 binary, and `dolt` on PATH. Without the variable the real run is skipped.

const (
	binaryVariable = "BEEHAVE_BD_132"
	realPrefix     = "ct"
)

// leaseBackend is one bd the scenarios run against.
type leaseBackend struct {
	run    proc.Runner
	prefix string
	beads  *beads.Store
	real   bool
	expire func(t *testing.T, rowID string)
}

func fakeBackend(t *testing.T) leaseBackend {
	t.Helper()

	bd := newFakeBd(t)

	expire := func(_ *testing.T, rowID string) { bd.Rows[rowID]["lease_expires_at"] = beadstest.At(beadstest.Now - 1) }

	return leaseBackend{run: bd, prefix: "p", beads: beadsOver(t, bd), expire: expire}
}

// realBackend starts a private dolt server and a beads database on it. It skips when BEEHAVE_BD_132 is not set.
// Every command runs through env(1), so the test never changes the environment of the test process and can run in parallel.
func realBackend(t *testing.T) leaseBackend {
	t.Helper()

	binary := config.ProcessEnv()[binaryVariable]
	if binary == "" {
		t.Skip(binaryVariable + " is not set: the real bd contract test needs a bd 1.3.2 binary")
	}

	root := t.TempDir()
	port := beadstest.StartDolt(t, root, "dolt")
	database := filepath.Join(root, "db")
	require.NoError(t, os.Mkdir(database, 0o750))

	run := beadstest.EnvRunner{Inner: proc.NewExecRunner("env", database), Env: beadstest.BeadsEnv(root, port), Binary: binary}
	for _, step := range [][]string{{"git", "init", "-q"}, {binary, "init", "--prefix", realPrefix, "--server", "--external", "--server-port", strconv.Itoa(port)}} {
		done := beadstest.EnvRunner{Inner: run.Inner, Env: run.Env, Binary: step[0]}.Run(t.Context(), step[1:])
		require.Zero(t, done.ExitCode, "%v: %s", step, done.Stderr)
	}

	require.Contains(t, run.Run(t.Context(), []string{"--version"}).Stdout, "bd version 1.3.2")
	require.NoError(t, beads.RequireBd(t.Context(), run))

	beads, err := beads.New(run, realPrefix)
	require.NoError(t, err)

	name := beadstest.DatabaseName(t, database)
	expire := func(t *testing.T, rowID string) {
		t.Helper()

		query := "UPDATE leases SET lease_expires_at = NOW() - INTERVAL 1 MINUTE WHERE issue_id = '" + rowID + "'"
		done := run.Inner.Run(t.Context(), []string{"dolt", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-tls", "-u", "root", "-p", "", "--use-db", name, "sql", "-q", query})
		require.Zero(t, done.ExitCode, done.Stderr)
	}

	return leaseBackend{run: run, prefix: realPrefix, beads: beads, real: true, expire: expire}
}

func rowOf(b leaseBackend, item string) string { return b.prefix + "-lock-gh-" + item }

// plant makes a claim row and applies the bd update arguments to it, the way an old beehave or a person would leave it.
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

func hold(item, holder string) model.ClaimRow {
	return model.ClaimRow{Item: item, Holder: holder}
}

// rowFor returns the held row of the item, or false when no holder holds it.
func (b leaseBackend) rowFor(t *testing.T, item string) (model.ClaimRow, bool) {
	t.Helper()

	held, err := b.beads.HeldRows(t.Context())
	require.NoError(t, err)

	i := slices.IndexFunc(held, func(row model.ClaimRow) bool { return row.Item == item })
	if i < 0 {
		return model.ClaimRow{}, false
	}

	return held[i], true
}

func (b leaseBackend) holder(t *testing.T, item string) string {
	t.Helper()

	row, _ := b.rowFor(t, item)

	return row.Holder
}

// leaseScenarios lists the scenarios. Each one uses its own item, so one database serves them all.
var leaseScenarios = map[string]func(*testing.T, leaseBackend){
	"a take shows its holder and the end of its lease":        scenarioTake,
	"a second take is refused and changes nothing":            scenarioLostTake,
	"a beat renews the lease of the holder only":              scenarioBeat,
	"a release reopens the row so the next take can claim it": scenarioReleaseThenTake,
	"a release by another holder changes nothing":             scenarioReleaseByStranger,
	"a row left closed with an assignee is claimable again":   scenarioStaleClosed,
	"a row left open with an assignee is claimable again":     scenarioStaleOpen,
	"a release of a missing row is a release":                 scenarioMissing,
	"a take with the old bd is refused before it writes":      scenarioOldBd,
	"a take of two holders keeps one holder per item":         scenarioHeldRows,
	"a reclaim leaves a live lease":                           scenarioReclaimLive,
	"a reclaim frees an ended lease and the next take wins":   scenarioReclaimEnded,
	"a late beat revives an ended lease before the reclaim":   scenarioLateBeat,
	"a row held with no lease is never reclaimed":             scenarioNoLease,
	"a heartbeat bd refuses prints the text the fake prints":  scenarioHeartbeatTexts,
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

	require.NoError(t, b.beads.TryHold(t.Context(), hold("11", "session-a")))

	row, held := b.rowFor(t, "11")
	assert.True(t, held)
	assert.Equal(t, "session-a", row.Holder)
	assert.False(t, row.LeaseEnds.IsZero(), "bd grants the lease to the actor of the claim")
}

func scenarioLostTake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("12", "session-a")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("12", "session-b")), "a lost take is no error: the caller reads the row")

	assert.Equal(t, "session-a", b.holder(t, "12"))
}

func scenarioBeat(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("13", "session-beat")))
	b.expire(t, rowOf(b, "13"))
	before, _ := b.rowFor(t, "13")

	require.NoError(t, b.beads.Beat(t.Context(), "session-beat"))

	after, _ := b.rowFor(t, "13")
	assert.True(t, after.LeaseEnds.After(before.LeaseEnds), "the beat moves the end of the lease forward")
	require.ErrorIs(t, b.beads.Beat(t.Context(), "session-nobody"), model.ErrNoClaim)
}

func scenarioReleaseThenTake(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("14", "session-a")))
	require.NoError(t, b.beads.Free(t.Context(), hold("14", "session-a")))
	_, held := b.rowFor(t, "14")
	assert.False(t, held)
	require.NoError(t, b.beads.Free(t.Context(), hold("14", "session-a")), "a second release of a free row is no error")

	require.NoError(t, b.beads.TryHold(t.Context(), hold("14", "session-b")))
	assert.Equal(t, "session-b", b.holder(t, "14"), "a release must leave the row claimable")
}

func scenarioReleaseByStranger(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("15", "session-a")))
	require.ErrorIs(t, b.beads.Free(t.Context(), hold("15", "session-b")), model.ErrRowMoved)
	assert.Equal(t, "session-a", b.holder(t, "15"))
}

func scenarioStaleClosed(t *testing.T, b leaseBackend) {
	t.Helper()

	b.plant(t, "17", "--assignee", "old", "--status", "closed")

	require.NoError(t, b.beads.TryHold(t.Context(), hold("17", "session-a")))
	assert.Equal(t, "session-a", b.holder(t, "17"))
}

func scenarioStaleOpen(t *testing.T, b leaseBackend) {
	t.Helper()

	b.plant(t, "18", "--assignee", "old")

	require.NoError(t, b.beads.TryHold(t.Context(), hold("18", "session-a")))
	assert.Equal(t, "session-a", b.holder(t, "18"))
}

func scenarioMissing(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.Free(t.Context(), hold("19", "session-a")))

	_, held := b.rowFor(t, "19")
	assert.False(t, held)
}

func scenarioOldBd(t *testing.T, b leaseBackend) {
	t.Helper()

	old := &oldBd{inner: b.run}
	beads, err := beads.New(old, b.prefix)
	require.NoError(t, err)

	require.ErrorContains(t, beads.TryHold(t.Context(), hold("20", "session-a")), "beehave needs bd 1.3.2 or newer for claims; found 0.56.1")
	assert.Equal(t, 1, old.calls, "only bd --version ran")
}

func scenarioHeldRows(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("21", "session-a")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("22", "session-b")))

	assert.Equal(t, "session-a", b.holder(t, "21"))
	assert.Equal(t, "session-b", b.holder(t, "22"))
}

func scenarioReclaimLive(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("23", "session-a")))

	freed, err := b.beads.Reclaim(t.Context(), "23")
	require.NoError(t, err)
	assert.False(t, freed)
	assert.Equal(t, "session-a", b.holder(t, "23"))
}

func scenarioReclaimEnded(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("24", "session-a")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("25", "session-a")))
	b.expire(t, rowOf(b, "24"))
	b.expire(t, rowOf(b, "25"))

	freed, err := b.beads.Reclaim(t.Context(), "24")
	require.NoError(t, err)
	assert.True(t, freed)
	assert.Equal(t, "session-a", b.holder(t, "25"), "a reclaim is scoped to the row of its item")

	require.NoError(t, b.beads.TryHold(t.Context(), hold("24", "session-b")))
	assert.Equal(t, "session-b", b.holder(t, "24"))
}

func scenarioLateBeat(t *testing.T, b leaseBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("26", "session-late")))
	b.expire(t, rowOf(b, "26"))
	require.NoError(t, b.beads.Beat(t.Context(), "session-late"))

	freed, err := b.beads.Reclaim(t.Context(), "26")
	require.NoError(t, err)
	assert.False(t, freed, "expiry is enforced only by the reclaim, and a beat before it revives the lease")
}

func scenarioNoLease(t *testing.T, b leaseBackend) {
	t.Helper()

	b.plant(t, "27", "--assignee", "hand", "--status", "in_progress")

	row, held := b.rowFor(t, "27")
	assert.True(t, held)
	assert.True(t, row.LeaseEnds.IsZero())

	freed, err := b.beads.Reclaim(t.Context(), "27")
	require.NoError(t, err)
	assert.False(t, freed)
}

func scenarioRace(t *testing.T, b leaseBackend) {
	t.Helper()

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i, who := range []string{"a", "b"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = b.beads.TryHold(t.Context(), hold("30", "session-"+who))
		}()
	}

	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	assert.Contains(t, []string{"session-a", "session-b"}, b.holder(t, "30"))
}

func scenarioHeartbeatTexts(t *testing.T, b leaseBackend) {
	t.Helper()

	beat := func(item string) model.CommandResult {
		return b.run.Run(t.Context(), []string{"heartbeat", rowOf(b, item), "--actor", "session-a"})
	}

	missing := beat("58")
	assert.Equal(t, 1, missing.ExitCode)
	assert.Contains(t, missing.Stderr, "resolving "+rowOf(b, "58")+": no issue found matching")

	b.plant(t, "59")

	open := beat("59")
	assert.Equal(t, 1, open.ExitCode)
	assert.Contains(t, open.Stderr, "heartbeat "+rowOf(b, "59")+": issue not claimable: "+rowOf(b, "59")+" status open")

	require.NoError(t, b.beads.TryHold(t.Context(), hold("60", "session-b")))

	other := beat("60")
	assert.Equal(t, 1, other.ExitCode)
	assert.Contains(t, other.Stderr, "issue already claimed by session-b")
}

// oldBd answers --version like bd 0.56.1 and counts the commands it gets.
type oldBd struct {
	inner proc.Runner
	calls int
}

func (o *oldBd) Run(ctx context.Context, args []string) model.CommandResult {
	o.calls++

	if args[0] == "--version" {
		return model.CommandResult{Stdout: "bd version 0.56.1 (dev)\n"}
	}

	return o.inner.Run(ctx, args)
}
