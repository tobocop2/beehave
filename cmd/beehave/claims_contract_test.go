package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// This file is the contract test of the claim policy over the claim store. The scenarios run against the fake bd of beadstest, which keeps
// the fake honest, and against a real bd 1.3.2 with a private dolt server, which keeps the policy honest. The real run needs BEEHAVE_BD_132,
// the path of a bd 1.3.2 binary, and `dolt` on PATH. Without the variable the real run is skipped. The scenarios of the claim store alone
// are in internal/store, which may not import claim.

const (
	binaryVariable = "BEEHAVE_BD_132"
	realPrefix     = "ct"
)

// policyBackend is one bd the scenarios run against.
// now is the clock the policy compares leases with. state reads what the tracker keeps for some rows, so a test can show that a call wrote nothing.
type policyBackend struct {
	run    proc.Runner
	prefix string
	beads  *beads.Store
	expire func(t *testing.T, rowID string)
	now    func() time.Time
	state  func(t *testing.T, rowIDs ...string) string
}

func fakePolicyBackend(t *testing.T) policyBackend {
	t.Helper()

	bd := beadstest.NewBd(t)

	expire := func(_ *testing.T, rowID string) { bd.Rows[rowID]["lease_expires_at"] = beadstest.At(beadstest.Now - 1) }
	now := func() time.Time { return time.Unix(beadstest.Now, 0) }
	state := func(_ *testing.T, rowIDs ...string) string {
		kept := map[string]beadstest.Fields{}
		for _, id := range rowIDs {
			kept[id] = bd.Rows[id]
		}

		return beadstest.JSON(kept)
	}

	return policyBackend{run: bd, prefix: "p", beads: beadsOver(t, bd), expire: expire, now: now, state: state}
}

// realPolicyBackend starts a private dolt server and a beads database on it. It skips when BEEHAVE_BD_132 is not set.
// Every command runs through env(1), so the test never changes the environment of the test process and can run in parallel.
func realPolicyBackend(t *testing.T) policyBackend {
	t.Helper()

	binary := config.ProcessEnv()[binaryVariable]
	if binary == "" {
		t.Skip(binaryVariable + " is not set: the real bd contract test needs a bd 1.3.2 binary")
	}

	root := t.TempDir()
	port := beadstest.StartDolt(t, root, "dolt")
	database := filepath.Join(root, "db")
	require.NoError(t, os.Mkdir(database, 0o750))

	inner := proc.NewExecRunner("env", database)
	run := beadstest.EnvRunner{Inner: inner, Env: beadstest.BeadsEnv(root, port), Binary: binary}

	for _, step := range [][]string{{"git", "init", "-q"}, {binary, "init", "--prefix", realPrefix, "--server", "--external", "--server-port", strconv.Itoa(port)}} {
		done := beadstest.EnvRunner{Inner: inner, Env: run.Env, Binary: step[0]}.Run(t.Context(), step[1:])
		require.Zero(t, done.ExitCode, "%v: %s", step, done.Stderr)
	}

	require.NoError(t, beads.RequireBd(t.Context(), run))

	beads, err := beads.New(run, realPrefix)
	require.NoError(t, err)

	name := beadstest.DatabaseName(t, database)
	sql := func(t *testing.T, query string) string {
		t.Helper()

		done := inner.Run(t.Context(), []string{"dolt", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-tls", "-u", "root", "-p", "", "--use-db", name, "sql", "-r", "csv", "-q", query})
		require.Zero(t, done.ExitCode, done.Stderr)

		return done.Stdout
	}
	expire := func(t *testing.T, rowID string) {
		t.Helper()
		sql(t, "UPDATE leases SET lease_expires_at = NOW() - INTERVAL 1 MINUTE WHERE issue_id = '"+rowID+"'")
	}
	state := func(t *testing.T, rowIDs ...string) string {
		t.Helper()

		in := "('" + strings.Join(rowIDs, "', '") + "')"

		var all strings.Builder
		for _, query := range []string{
			"SELECT * FROM issues WHERE id IN " + in + " ORDER BY id", "SELECT * FROM leases WHERE issue_id IN " + in + " ORDER BY issue_id",
			"SELECT count(*) FROM events WHERE issue_id IN " + in, "SELECT count(*) FROM dolt_diff_issues WHERE to_id IN " + in + " OR from_id IN " + in,
		} {
			all.WriteString(sql(t, query))
		}

		return all.String()
	}

	return policyBackend{run: run, prefix: realPrefix, beads: beads, expire: expire, now: time.Now, state: state}
}

func (b policyBackend) rowOf(item string) string { return b.prefix + "-lock-gh-" + item }

// plant makes a claim row and applies the bd update arguments to it, the way a person would leave it.
func (b policyBackend) plant(t *testing.T, item string, update ...string) {
	t.Helper()

	id := b.rowOf(item)
	made := b.run.Run(t.Context(), []string{"create", "--id", id, "--title", "lock:gh-" + item, "--type", "task", "--priority", "3", "--silent", "--description", "planted"})
	require.Zero(t, made.ExitCode, made.Stderr)

	done := b.run.Run(t.Context(), append([]string{"update", id}, update...))
	require.Zero(t, done.ExitCode, done.Stderr)
}

func (b policyBackend) holder(t *testing.T, item string) string {
	t.Helper()

	return heldBy(t, b.beads, item)
}

// claimsOver builds the claim policy over rows, with the clock of the backend. The returned buffer holds what the policy says.
func (b policyBackend) claimsOver(t *testing.T, rows claim.Rows) (*claim.Claims, *bytes.Buffer) {
	t.Helper()

	out := &bytes.Buffer{}
	claims, err := claim.New(claim.Options{Rows: rows, Clock: b.now, Out: out})
	require.NoError(t, err)

	return claims, out
}

func hold(item, holder string) model.ClaimRow {
	return model.ClaimRow{Item: item, Holder: holder}
}

// recorder passes every command to inner and keeps the command words.
type recorder struct {
	inner proc.Runner
	mu    sync.Mutex
	words []string
}

func (r *recorder) Run(ctx context.Context, args []string) model.CommandResult {
	r.mu.Lock()
	r.words = append(r.words, args[0])
	r.mu.Unlock()

	return r.inner.Run(ctx, args)
}

// policyScenarios lists the scenarios. Each one uses its own item, so one database serves them all.
var policyScenarios = map[string]func(*testing.T, policyBackend){
	"a take reclaims an ended lease and takes again":                        scenarioTakeReclaims,
	"a take that loses names the winner with code 3":                        scenarioTakeNamesWinner,
	"a late beat after a retake changes nothing":                            scenarioLateBeatAfterRetake,
	"EnsureFree and Who write nothing":                                      scenarioInspectionWritesNothing,
	"a row held with no lease is refused by take and EnsureFree":            scenarioNoLeaseRefused,
	"a beat that finds the row taken since the list says the claim is gone": scenarioBeatAfterLateTake,
	"a beat that cannot renew a row is an error and renews the other rows":  scenarioBeatErrorRenewsTheRest,
}

// heartbeatSpy passes every command to inner. On a heartbeat it first runs before, then fails the rows in failing as a tracker that cannot be reached does.
type heartbeatSpy struct {
	inner   proc.Runner
	before  func(rowID string)
	failing map[string]bool
}

func (s heartbeatSpy) Run(ctx context.Context, args []string) model.CommandResult {
	if args[0] == "heartbeat" {
		if s.before != nil {
			s.before(args[1])
		}

		if s.failing[args[1]] {
			return model.CommandResult{ExitCode: 1, Stderr: "dolt server is not reachable"}
		}
	}

	return s.inner.Run(ctx, args)
}

func (b policyBackend) leaseOf(t *testing.T, item string) time.Time {
	t.Helper()

	rows, err := b.beads.HeldRows(t.Context())
	require.NoError(t, err)

	for _, row := range rows {
		if row.Item == item {
			return row.LeaseEnds
		}
	}

	t.Fatalf("no claim row for item %s", item)

	return time.Time{}
}

func scenarioBeatAfterLateTake(t *testing.T, b policyBackend) {
	t.Helper()

	// Each beating scenario has its own holder, because a beat renews every row of its holder and the scenarios share one database.
	require.NoError(t, b.beads.TryHold(t.Context(), hold("64", "session-owner64")))

	var once sync.Once

	// Another holder reclaims and takes the row between the list of the beat and its heartbeat.
	spy := heartbeatSpy{inner: b.run, before: func(rowID string) {
		once.Do(func() {
			b.expire(t, rowID)

			freed, err := b.beads.Reclaim(t.Context(), "64")
			require.NoError(t, err)
			require.True(t, freed)
			require.NoError(t, b.beads.TryHold(t.Context(), hold("64", "session-b")))
		})
	}}

	beads, err := beads.New(spy, b.prefix)
	require.NoError(t, err)

	claims, _ := b.claimsOver(t, beads)

	problem, err := claims.Beat(t.Context(), "owner64")
	require.NoError(t, err)
	assert.Contains(t, problem, "holds no claim", "bd refused the heartbeat of a row another holder took, and that reads as a lost claim")
	assert.Equal(t, "session-b", b.holder(t, "64"))
}

func scenarioBeatErrorRenewsTheRest(t *testing.T, b policyBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("65", "session-m")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("66", "session-m")))
	b.expire(t, b.rowOf("65"))
	b.expire(t, b.rowOf("66"))

	before := b.leaseOf(t, "66")

	beads, err := beads.New(heartbeatSpy{inner: b.run, failing: map[string]bool{b.rowOf("65"): true}}, b.prefix)
	require.NoError(t, err)

	claims, _ := b.claimsOver(t, beads)

	problem, err := claims.Beat(t.Context(), "m")
	require.ErrorContains(t, err, "dolt server is not reachable")
	assert.Empty(t, problem, "a failed renewal is an error, not the text of a lost claim")
	assert.True(t, b.leaseOf(t, "66").After(before), "the row after the failed one is renewed")
}

func TestClaimContract(t *testing.T) {
	t.Parallel()

	for _, kind := range []struct {
		name   string
		shared bool
		build  func(*testing.T) policyBackend
	}{{"fake bd", false, fakePolicyBackend}, {"real bd 1.3.2", true, realPolicyBackend}} {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			var shared policyBackend

			if kind.shared {
				shared = kind.build(t)
			}

			for scenario, run := range policyScenarios {
				t.Run(scenario, func(t *testing.T) {
					t.Parallel()

					b := shared
					if !kind.shared {
						b = kind.build(t)
					}

					run(t, b)
				})
			}

			// The fake bd is not safe for two goroutines: races on it run through hooks in race_test.go.
			if kind.shared {
				t.Run("two takes at the same moment give one winner and a loser", func(t *testing.T) {
					t.Parallel()
					scenarioTakeRace(t, shared)
				})
			}
		})
	}
}

func scenarioTakeReclaims(t *testing.T, b policyBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("51", "session-b")))
	b.expire(t, b.rowOf("51"))

	claims, out := b.claimsOver(t, b.beads)

	require.NoError(t, claims.Take(t.Context(), "a", "51"))
	assert.Equal(t, "reclaiming #51 from session-b: its lease ended\nTAKEN #51 by session-a\n", out.String())
	assert.Equal(t, "session-a", b.holder(t, "51"))
}

func scenarioTakeNamesWinner(t *testing.T, b policyBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("52", "session-b")))

	claims, _ := b.claimsOver(t, b.beads)

	err := claims.Take(t.Context(), "a", "52")
	require.EqualError(t, err, "REFUSED: #52 is held by session-b. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, claimCode(t, err))
	assert.Equal(t, "session-b", b.holder(t, "52"))
}

func scenarioTakeRace(t *testing.T, b policyBackend) {
	t.Helper()

	errs := make([]error, 2)

	var wg sync.WaitGroup

	for i, who := range []string{"a", "b"} {
		claims, _ := b.claimsOver(t, b.beads)

		wg.Go(func() { errs[i] = claims.Take(t.Context(), who, "53") })
	}

	wg.Wait()

	winners := 0

	for i, err := range errs {
		if err == nil {
			winners++

			continue
		}

		other := []string{"session-b", "session-a"}[i]
		require.EqualError(t, err, "REFUSED: #53 is held by "+other+". Do not touch it.")
		assert.Equal(t, claim.CodeHeld, claimCode(t, err))
	}

	assert.Equal(t, 1, winners)
}

func scenarioLateBeatAfterRetake(t *testing.T, b policyBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("54", "session-dead")))
	b.expire(t, b.rowOf("54"))

	claims, _ := b.claimsOver(t, b.beads)
	require.NoError(t, claims.Take(t.Context(), "fresh", "54"))

	before := b.state(t, b.rowOf("54"))

	problem, err := claims.Beat(t.Context(), "dead")
	require.NoError(t, err)
	assert.Contains(t, problem, "holds no claim", "the tracker says the claim is gone")
	assert.Equal(t, "session-fresh", b.holder(t, "54"))
	assert.Equal(t, before, b.state(t, b.rowOf("54")), "the late beat leaves the row and its lease as they were")
}

func scenarioInspectionWritesNothing(t *testing.T, b policyBackend) {
	t.Helper()

	require.NoError(t, b.beads.TryHold(t.Context(), hold("55", "session-b")))
	require.NoError(t, b.beads.Free(t.Context(), hold("55", "session-b")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("56", "session-b")))
	require.NoError(t, b.beads.TryHold(t.Context(), hold("57", "session-b")))
	b.expire(t, b.rowOf("57"))

	rows := []string{b.rowOf("55"), b.rowOf("56"), b.rowOf("57")}
	before := b.state(t, rows...)

	spy := &recorder{inner: b.run}
	beads, err := beads.New(spy, b.prefix)
	require.NoError(t, err)

	claims, _ := b.claimsOver(t, beads)

	// A free row and an ended lease are free for another owner. A live lease is held.
	for item, wantCode := range map[string]int{"55": 0, "56": claim.CodeHeld, "57": 0} {
		_, whoErr := claims.Who(t.Context(), item)
		require.NoError(t, whoErr)

		free := claims.EnsureFree(t.Context(), "a", item)
		if wantCode == 0 {
			require.NoError(t, free, item)
		} else {
			assert.Equal(t, wantCode, claimCode(t, free), item)
		}
	}

	assert.Equal(t, before, b.state(t, rows...), "nothing the tracker keeps changed")
	assert.Subset(t, []string{"list", "show", "--version"}, spy.words, "only reads ran")
}

func scenarioNoLeaseRefused(t *testing.T, b policyBackend) {
	t.Helper()

	b.plant(t, "58", "--assignee", "hand", "--status", "in_progress")

	claims, _ := b.claimsOver(t, b.beads)

	assert.Equal(t, claim.CodeHeld, claimCode(t, claims.Take(t.Context(), "a", "58")))
	assert.Equal(t, claim.CodeHeld, claimCode(t, claims.EnsureFree(t.Context(), "a", "58")))

	who, err := claims.Who(t.Context(), "58")
	require.NoError(t, err)
	assert.Equal(t, "#58 held by hand (no lease: release it by hand)", who)
}
