// Package beadstest holds a fake bd command line for tests, as net/http/httptest holds a fake server.
package beadstest

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// Progress is the status of a held claim row. Now is the unix time of the fake. LeaseSeconds is the lease bd 1.3.2 grants on a claim
// and on a heartbeat (internal/storage/issueops/lease.go:26).
const (
	Progress     = "in_progress"
	Now          = 1_900_000_000
	LeaseSeconds = 300

	guardExit = 13

	cmdList    = "list"
	cmdCreate  = "create"
	statusOpen = "open"
	keyDesc    = "description"
	keyLease   = "lease_expires_at"
	keyHolder  = "assignee"
	flagActor  = "--actor"
	actorName  = "tester"
)

// Fields is one row of the fake bd.
type Fields = map[string]any

// Block returns the beads block of a project whose database has the id prefix. The key is the one of beads.BlockPrefix; a backend
// package cannot be imported here, and every store test that builds a store from this block fails if the two drift apart.
func Block(prefix string) *model.Block {
	return model.NewBlock("test", []model.BlockEntry{model.TextEntry("prefix", prefix)})
}

// Blocks returns the blocks of a project whose database has the id prefix, as model.Project holds them: the beads block under its name.
func Blocks(prefix string) map[string]*model.Block {
	return map[string]*model.Block{"beads": Block(prefix)}
}

// Bd keeps rows in memory and answers the bd commands the claim code uses. It follows bd 1.3.2: --claim takes only an open row
// without an assignee, grants the lease to the actor and sets the actor as the assignee (a claim that also sets --assignee, or any update of
// the status or the assignee, ends the lease); a refused --claim exits 1 with the text of a lost take; bd heartbeat renews the lease of the
// holder; bd reclaim frees the rows whose lease ended; bd unclaim --if-assignee reopens the row;
// create --id on an existing row resets it; show resolves a partial id by prefix. The contract test in store runs the same scenarios against a real bd.
// A missing row prints what bd prints: v0.56.1 `Error fetching <id>: no issue found matching "<id>"` (cmd/bd/show.go:105, internal/utils/id_parser.go:140),
// v1.3.2 `Issue <id> not found` (cmd/bd/show.go:136). V1 picks the second text. NoGuard plays a bd that lacks --if-assignee and --if-status. Version is the answer of --version.
type Bd struct {
	Rows    map[string]Fields
	Calls   [][]string
	Hooks   []func(args []string)
	Fail    map[string]model.CommandResult
	V1      bool
	NoGuard bool
	Version string

	order []string
}

// NewBd returns a fake bd with no rows.
func NewBd(t *testing.T) *Bd {
	t.Helper()

	return &Bd{Rows: map[string]Fields{}, Fail: map[string]model.CommandResult{}, Version: "1.3.2"}
}

// Add makes a row. The row is open and titled with its id unless extra says otherwise.
func (b *Bd) Add(id string, extra Fields) Fields {
	row := Fields{"id": id, "status": statusOpen, "title": id}
	for key, value := range extra {
		row[key] = value
	}

	if _, found := b.Rows[id]; !found {
		b.order = append(b.order, id)
	}

	b.Rows[id] = row

	return row
}

// Run answers one bd command.
func (b *Bd) Run(_ context.Context, args []string) model.CommandResult {
	b.Calls = append(b.Calls, slices.Clone(args))

	for _, hook := range slices.Clone(b.Hooks) {
		hook(args)
	}

	if planned, found := b.Fail[args[0]]; found {
		return planned
	}

	return b.answer(args)
}

func (b *Bd) answer(args []string) model.CommandResult {
	switch args[0] {
	case "--version":
		return model.CommandResult{Stdout: "bd version " + b.Version + " (dev)\n"}
	case cmdList:
		return b.list(args[1:])
	case "show":
		return b.show(args[1])
	case cmdCreate, "update", "unclaim":
		return b.write(args)
	case "heartbeat":
		return b.heartbeat(args[1:])
	case "reclaim":
		return b.reclaim(args[1:])
	}

	return model.CommandResult{ExitCode: 1, Stderr: "fake bd: no answer planned"}
}

// write answers the bd commands that change a row.
func (b *Bd) write(args []string) model.CommandResult {
	switch args[0] {
	case cmdCreate:
		return b.create(args[1:])
	case "update":
		return b.update(args[1:])
	}

	return b.unclaim(args[1:])
}

// Count returns how many times the command ran.
func (b *Bd) Count(command string) int {
	n := 0

	for _, call := range b.Calls {
		if call[0] == command {
			n++
		}
	}

	return n
}

// Called reports whether the exact command ran.
func (b *Bd) Called(args ...string) bool {
	return slices.ContainsFunc(b.Calls, func(call []string) bool { return slices.Equal(call, args) })
}

func (b *Bd) list(rest []string) model.CommandResult {
	rows := []Fields{}

	for _, id := range b.order {
		if listed(b.Rows[id], rest) {
			rows = append(rows, b.Rows[id])
		}
	}

	return model.CommandResult{Stdout: JSON(rows)}
}

// listed reports whether the row passes the --status= and --type filters in rest.
func listed(row Fields, rest []string) bool {
	for i, word := range rest {
		if status, found := strings.CutPrefix(word, "--status="); found && row["status"] != status {
			return false
		}

		if word == "--type" && row["issue_type"] != rest[i+1] {
			return false
		}
	}

	return true
}

func (b *Bd) show(wanted string) model.CommandResult {
	row, found := b.Rows[wanted]
	if !found {
		var near []Fields

		for _, id := range b.order {
			if strings.HasPrefix(id, wanted) {
				near = append(near, b.Rows[id])
			}
		}

		if len(near) != 1 {
			return model.CommandResult{ExitCode: 1, Stderr: b.missing("Error fetching", wanted)}
		}

		row = near[0]
	}

	return model.CommandResult{Stdout: JSON([]Fields{row})}
}

// missing returns what bd prints for an id it does not know.
func (b *Bd) missing(verb, id string) string {
	if b.V1 {
		return "Issue " + id + " not found\nHint: check the id"
	}

	return verb + " " + id + ": no issue found matching \"" + id + "\""
}

// Options reads the "--flag value" pairs of rest, from the index from.
func Options(rest []string, from int) map[string]string {
	found := map[string]string{}

	for i := from; i < len(rest)-1; i++ {
		if strings.HasPrefix(rest[i], "--") {
			found[rest[i]] = rest[i+1]
		}
	}

	return found
}

func (b *Bd) create(rest []string) model.CommandResult {
	opts := Options(rest, 0)
	kind := opts["--type"]

	if kind == "" {
		kind = "task"
	}

	b.Add(opts["--id"], Fields{"title": opts["--title"], "issue_type": kind, keyDesc: opts["--description"]})

	return model.CommandResult{Stdout: opts["--id"]}
}

func (b *Bd) update(rest []string) model.CommandResult {
	row, found := b.Rows[rest[0]]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: b.missing("Error resolving", rest[0])}
	}

	opts := Options(rest, 1)

	claim := claimRow(rest[0], row, actorOf(opts), slices.Contains(rest, "--claim"))
	if refused := cmp.Or(b.guard(row, opts), claim); refused != nil {
		return *refused
	}

	apply(row, opts)

	return model.CommandResult{}
}

// apply writes the fields that bd update sets. A change of the status or the assignee ends the lease.
func apply(row Fields, opts map[string]string) {
	for flag, key := range map[string]string{"--status": "status", "--assignee": keyHolder, "--description": keyDesc} {
		if value, set := opts[flag]; set {
			row[key] = value

			if key != keyDesc {
				delete(row, keyLease)
			}
		}
	}
}

// actorOf returns the actor of a command: the --actor flag, or the default actor of the fake.
func actorOf(opts map[string]string) string {
	return cmp.Or(opts[flagActor], actorName)
}

// At returns a lease end as bd prints it, for a unix time.
func At(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// claimRow plays --claim like bd 1.3.2: it takes an open row without an assignee for the actor and grants the lease, and exits 1 with the text of a lost take otherwise.
// A claim by the actor that holds the row already is a success that changes nothing (internal/storage/issueops/claim.go:150-157).
// The texts start with the sentinels of internal/storage/issueops/errors.go (:12 and :17) in the bd source.
func claimRow(id string, row Fields, actor string, claiming bool) *model.CommandResult {
	if !claiming {
		return nil
	}

	assignee, status := text(row[keyHolder]), text(row["status"])

	switch {
	case status == statusOpen && assignee == "":
		row["status"], row[keyHolder], row[keyLease] = Progress, actor, At(Now+LeaseSeconds)

		return nil
	case status == Progress && assignee == actor:
		return nil
	case status == statusOpen:
		return refusal(id, "issue already claimed: already assigned to \""+assignee+"\"")
	case assignee != "":
		return refusal(id, "issue already claimed by "+assignee)
	}

	return refusal(id, "issue not claimable: status "+status)
}

// refusal is what bd 1.3.2 prints when an update fails: exit 1 and the text three times (cmd/bd/update.go:553 and reportUpdateFailures).
func refusal(id, why string) *model.CommandResult {
	return &model.CommandResult{ExitCode: 1, Stderr: "Error updating " + id + ": " + why + "\nError: 1 of 1 issues failed to update\n  " + id + ": updating issue: " + why + "\n"}
}

// unclaim plays bd unclaim <id> --if-assignee <holder>: it reopens the row and clears the assignee, or exits 1 on a mismatch.
func (b *Bd) unclaim(rest []string) model.CommandResult {
	row, found := b.Rows[rest[0]]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: b.missing("Error resolving", rest[0])}
	}

	holder := Options(rest, 1)["--if-assignee"]

	switch {
	case b.NoGuard:
		return model.CommandResult{ExitCode: 1, Stderr: "Error: unknown flag: --if-assignee"}
	case text(row["status"]) == "closed":
		return model.CommandResult{ExitCode: 1, Stderr: "Error unclaiming " + rest[0] + ": cannot unclaim closed issue " + rest[0]}
	case text(row["assignee"]) != holder:
		return model.CommandResult{ExitCode: 1, Stderr: "Error unclaiming " + rest[0] + ": assignee mismatch: " + rest[0] + " is held by \"" + text(row["assignee"]) + "\", expected \"" + holder + "\""}
	}

	row["status"], row[keyHolder] = statusOpen, ""
	delete(row, keyLease)

	return model.CommandResult{}
}

// guard answers a bd update whose guards do not hold: exit 13 and nothing written, or an unknown flag.
// --if-assignee and --if-status both have to hold when both are present.
func (b *Bd) guard(row Fields, opts map[string]string) *model.CommandResult {
	holder, byAssignee := opts["--if-assignee"]
	status, byStatus := opts["--if-status"]

	switch {
	case !byAssignee && !byStatus:
		return nil
	case b.NoGuard:
		return &model.CommandResult{ExitCode: 1, Stderr: "Error: unknown flag: --if-assignee"}
	case byAssignee && text(row["assignee"]) != holder:
		return &model.CommandResult{ExitCode: guardExit, Stderr: "assignee mismatch: " + text(row["id"]) + " is held by \"" + text(row["assignee"]) + "\", expected \"" + holder + "\""}
	case byStatus && text(row["status"]) != status:
		return &model.CommandResult{ExitCode: guardExit, Stderr: "status mismatch: " + text(row["id"]) + " is " + text(row["status"]) + ", expected " + status}
	}

	return nil
}

// heartbeat plays bd heartbeat <id>: it renews the lease of the actor that holds the row, and refuses any other actor
// (cmd/bd/heartbeat.go:71, internal/storage/issueops/lease.go:380-407). A holder whose lease ended and was not reclaimed gets the lease back.
func (b *Bd) heartbeat(rest []string) model.CommandResult {
	row, found := b.Rows[rest[0]]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: b.missing("Error: resolving", rest[0])}
	}

	if status := text(row["status"]); status != Progress {
		return model.CommandResult{ExitCode: 1, Stderr: "Error: heartbeat " + rest[0] + ": issue not claimable: " + rest[0] + " status " + status}
	}

	if holder := text(row[keyHolder]); holder != actorOf(Options(rest, 1)) {
		return model.CommandResult{ExitCode: 1, Stderr: "Error: heartbeat " + rest[0] + ": issue already claimed by " + holder}
	}

	row[keyLease] = At(Now + LeaseSeconds)

	return model.CommandResult{}
}

// reclaim plays bd reclaim --id <id> --older-than 0s --json (cmd/bd/reclaim.go:107): every in_progress row whose lease ended goes back to open.
func (b *Bd) reclaim(rest []string) model.CommandResult {
	only := Options(rest, 0)["--id"]
	freed := []Fields{}

	for _, id := range b.order {
		row := b.Rows[id]
		ends, err := time.Parse(time.RFC3339, text(row[keyLease]))

		if (only != "" && only != id) || text(row["status"]) != Progress || err != nil || !ends.Before(time.Unix(Now, 0)) {
			continue
		}

		freed = append(freed, Fields{"id": id, "previous_owner": text(row[keyHolder])})
		row["status"], row[keyHolder] = statusOpen, ""
		delete(row, keyLease)
	}

	return model.CommandResult{Stdout: JSON(Fields{"count": len(freed), "reclaimed": freed, "scoped": only != ""})}
}

// JSON encodes a value. A value that cannot be encoded gives the error text, which no reader of a fake can parse, so the test fails.
func JSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "beadstest: " + err.Error()
	}

	return string(data)
}

func text(value any) string {
	if word, ok := value.(string); ok {
		return word
	}

	return ""
}

// Hook wraps a proc.Runner and calls Fire once, just before the first command that When accepts.
// It plays a second claimer that acts between the read of the first claimer and its write.
type Hook struct {
	Inner proc.Runner
	When  func(args []string) bool
	Fire  func()

	done bool
}

// Run runs one command.
func (h *Hook) Run(ctx context.Context, args []string) model.CommandResult {
	if !h.done && h.When(args) {
		h.done = true
		h.Fire()
	}

	return h.Inner.Run(ctx, args)
}

// Reads wraps a proc.Runner and fails the bd commands that list or show rows while Down is set. Writes still run.
type Reads struct {
	Inner proc.Runner
	Down  bool
}

// Run runs one command.
func (r *Reads) Run(ctx context.Context, args []string) model.CommandResult {
	if r.Down && slices.Contains([]string{cmdList, "show"}, args[0]) {
		return model.CommandResult{ExitCode: 1, Stderr: "network down"}
	}

	return r.Inner.Run(ctx, args)
}
