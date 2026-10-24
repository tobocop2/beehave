package claim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/model"
)

const (
	lease     = 7200
	progress  = "in_progress"
	nowUnix   = 1_900_000_000
	rowGH5    = "p-lock-gh-5"
	sessionA  = "session-A"
	sessionB  = "session-B"
	agentKind = "agent"
)

type fields = map[string]any

// fakeBd keeps rows in memory and answers the bd commands the claim code uses. It copies the behaviors the claim code is written around:
// a refused --claim exits 0, the claim tests the assignee, create --id on an existing row resets it, and show resolves a partial id by prefix.
type fakeBd struct {
	t              *testing.T
	rows           map[string]fields
	order          []string
	calls          [][]string
	hooks          []func(args []string)
	fail           map[string]model.CommandResult
	heartbeatFails bool
}

func newFakeBd(t *testing.T) *fakeBd {
	t.Helper()

	return &fakeBd{t: t, rows: map[string]fields{}, fail: map[string]model.CommandResult{}}
}

func (b *fakeBd) add(id string, extra fields) fields {
	row := fields{"id": id, "status": "open", "title": id}
	for key, value := range extra {
		row[key] = value
	}

	if _, found := b.rows[id]; !found {
		b.order = append(b.order, id)
	}

	b.rows[id] = row

	return row
}

func (b *fakeBd) Run(_ context.Context, args []string) model.CommandResult {
	b.calls = append(b.calls, slices.Clone(args))

	for _, hook := range slices.Clone(b.hooks) {
		hook(args)
	}

	if planned, found := b.fail[args[0]]; found {
		return planned
	}

	switch args[0] {
	case "list":
		return b.list(args[1:])
	case "show":
		return b.show(args[1])
	case "create":
		return b.create(args[1:])
	case "update":
		return b.update(args[1:])
	case "agent":
		return b.heartbeat(args[2])
	}

	return model.CommandResult{ExitCode: 1, Stderr: "fake bd: no answer planned"}
}

func (b *fakeBd) count(command string) int {
	n := 0

	for _, call := range b.calls {
		if call[0] == command {
			n++
		}
	}

	return n
}

func (b *fakeBd) called(args ...string) bool {
	return slices.ContainsFunc(b.calls, func(call []string) bool { return slices.Equal(call, args) })
}

func (b *fakeBd) list(rest []string) model.CommandResult {
	rows := []fields{}

	for _, id := range b.order {
		row, kept := b.rows[id], true

		for i, word := range rest {
			if status, found := strings.CutPrefix(word, "--status="); found {
				kept = kept && row["status"] == status
			}

			if word == "--type" {
				kept = kept && row["issue_type"] == rest[i+1]
			}
		}

		if kept {
			rows = append(rows, row)
		}
	}

	return model.CommandResult{Stdout: b.json(rows)}
}

func (b *fakeBd) show(wanted string) model.CommandResult {
	row, found := b.rows[wanted]
	if !found {
		var near []fields

		for _, id := range b.order {
			if strings.HasPrefix(id, wanted) {
				near = append(near, b.rows[id])
			}
		}

		if len(near) != 1 {
			return model.CommandResult{ExitCode: 1, Stderr: "no issue found matching " + wanted}
		}

		row = near[0]
	}

	return model.CommandResult{Stdout: b.json([]fields{row})}
}

func options(rest []string, from int) map[string]string {
	found := map[string]string{}

	for i := from; i < len(rest)-1; i++ {
		if strings.HasPrefix(rest[i], "--") {
			found[rest[i]] = rest[i+1]
		}
	}

	return found
}

func (b *fakeBd) create(rest []string) model.CommandResult {
	opts := options(rest, 0)
	kind := opts["--type"]

	if kind == "" {
		kind = "task"
	}

	b.add(opts["--id"], fields{"title": opts["--title"], "issue_type": kind, "last_activity": nil})

	return model.CommandResult{Stdout: opts["--id"]}
}

func (b *fakeBd) update(rest []string) model.CommandResult {
	row, found := b.rows[rest[0]]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: "no such row"}
	}

	opts := options(rest, 1)

	if slices.Contains(rest, "--claim") {
		if assignee := textOf(row["assignee"]); assignee != "" {
			return model.CommandResult{Stderr: "Error claiming: already claimed by " + assignee}
		}

		row["status"] = progress
	}

	for flag, key := range map[string]string{"--status": "status", "--assignee": "assignee", "--description": "description"} {
		if value, set := opts[flag]; set {
			row[key] = value
		}
	}

	return model.CommandResult{}
}

func (b *fakeBd) heartbeat(id string) model.CommandResult {
	if b.heartbeatFails {
		return model.CommandResult{ExitCode: 1, Stderr: "heartbeat refused"}
	}

	b.rows[id]["last_activity"] = "2026-10-01T00:00:00Z"

	return model.CommandResult{}
}

func (b *fakeBd) json(value any) string {
	b.t.Helper()

	data, err := json.Marshal(value)
	require.NoError(b.t, err)

	return string(data)
}

type fakeGh struct {
	result model.CommandResult
	calls  [][]string
}

func (g *fakeGh) Run(_ context.Context, args []string) model.CommandResult {
	g.calls = append(g.calls, slices.Clone(args))

	return g.result
}

type harness struct {
	t      *testing.T
	bd     *fakeBd
	gh     *fakeGh
	out    bytes.Buffer
	warn   bytes.Buffer
	slept  []time.Duration
	claims *claim.Claims
	sleep  func(time.Duration)
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, bd: newFakeBd(t), gh: &fakeGh{}}
	h.sleep = func(d time.Duration) { h.slept = append(h.slept, d) }
	h.build()

	return h
}

func (h *harness) build() {
	claims, err := claim.New(claim.Options{
		BD: h.bd, GH: h.gh, Prefix: "p", Repo: "owner/name",
		Lease: lease * time.Second, Settle: 500 * time.Millisecond,
		Clock: func() time.Time { return time.Unix(nowUnix, 0) },
		Sleep: func(d time.Duration) { h.sleep(d) },
		Out:   &h.out, Warn: &h.warn,
	})
	require.NoError(h.t, err)

	h.claims = claims
}

func (h *harness) said() string {
	return h.out.String()
}

func (h *harness) hold(item, who, suffix string) string {
	h.t.Helper()

	return h.holdKind(item, who, "gh", suffix)
}

func (h *harness) holdKind(item, who, kind, suffix string) string {
	h.t.Helper()

	id := "p-lock-" + kind + "-" + item + suffix
	h.bd.add(id, fields{"title": "lock:" + kind + "-" + item + " t", "status": progress, "assignee": who})

	return id
}

// seen adds the agent bead of who, last stamped secondsAgo seconds before now. A negative value leaves it unstamped.
func (h *harness) seen(who string, secondsAgo int64) {
	h.t.Helper()

	var stamp any
	if secondsAgo >= 0 {
		stamp = time.Unix(nowUnix-secondsAgo, 0).UTC().Format(time.RFC3339)
	}

	h.bd.add("p-agent-"+who, fields{"issue_type": agentKind, "last_activity": stamp})
}

func (h *harness) code(err error) int {
	h.t.Helper()

	var failure *claim.Error
	require.ErrorAs(h.t, err, &failure)

	return failure.Code
}

func textOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}

	return ""
}
