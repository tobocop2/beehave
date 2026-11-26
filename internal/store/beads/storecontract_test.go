package beads_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// This file is the contract test of the item store. The same scenario runs against a fake bd that answers the item commands, which keeps the store honest about
// what it sends, and against a real bd 1.3.2 with a private dolt server (BEEHAVE_BD_132, as the lease contract), which keeps the fake honest.

// itemRow is one item of the fake bd, in the fields that bd prints.
type itemRow struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	Comments    []string `json:"-"`
}

// itemBd is a fake bd that answers the item commands of the beads store: create, list, show, close, reopen, update and comments.
type itemBd struct {
	mu   sync.Mutex
	rows []*itemRow
}

func (b *itemBd) row(id string) *itemRow {
	for _, row := range b.rows {
		if row.ID == id {
			return row
		}
	}

	return nil
}

func jsonOf(value any) model.CommandResult {
	data, err := json.Marshal(value)
	if err != nil {
		return model.CommandResult{ExitCode: 1, Stderr: err.Error()}
	}

	return model.CommandResult{Stdout: string(data)}
}

func (b *itemBd) Run(_ context.Context, args []string) model.CommandResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	opts := beadstest.Options(args, 0)

	switch args[0] {
	case "create":
		row := &itemRow{ID: fmt.Sprintf("ct-%d", len(b.rows)+1), Title: opts["--title"], Description: opts["--description"], Status: "open", Labels: []string{}}
		b.rows = append(b.rows, row)

		return model.CommandResult{Stdout: row.ID + "\n"}
	case "list":
		found := []*itemRow{}

		for _, row := range b.rows {
			if strings.Contains(row.Description, opts["--desc-contains"]) {
				found = append(found, row)
			}
		}

		return jsonOf(found)
	case "comments":
		return b.comments(args[1:])
	}

	return b.one(args)
}

// one answers the commands that name one item: show, close, reopen and update.
func (b *itemBd) one(args []string) model.CommandResult {
	row := b.row(args[1])
	if row == nil {
		return model.CommandResult{ExitCode: 1, Stderr: "Error resolving " + args[1] + ": no issue found matching \"" + args[1] + "\""}
	}

	opts := beadstest.Options(args, 2)

	switch args[0] {
	case "show":
		return jsonOf([]*itemRow{row})
	case "close":
		row.Status = "closed"
	case "reopen":
		row.Status = "open"
	case "update":
		if label := opts["--add-label"]; label != "" && !slices.Contains(row.Labels, label) {
			row.Labels = append(row.Labels, label)
		}

		row.Labels = slices.DeleteFunc(row.Labels, func(l string) bool { return l == opts["--remove-label"] })
	default:
		return model.CommandResult{ExitCode: 1, Stderr: "fake bd: no answer planned for " + args[0]}
	}

	return model.CommandResult{}
}

// comments answers bd comments <id> --json and bd comments add <id> -- <text>.
func (b *itemBd) comments(rest []string) model.CommandResult {
	if rest[0] == "add" {
		row := b.row(rest[1])
		if row == nil {
			return model.CommandResult{ExitCode: 1, Stderr: "no issue found matching \"" + rest[1] + "\""}
		}

		row.Comments = append(row.Comments, rest[len(rest)-1])

		return model.CommandResult{}
	}

	row := b.row(rest[0])
	if row == nil {
		return model.CommandResult{ExitCode: 1, Stderr: "no issue found matching \"" + rest[0] + "\""}
	}

	type comment struct {
		Text string `json:"text"`
	}

	out := []comment{}
	for _, text := range row.Comments {
		out = append(out, comment{Text: text})
	}

	return jsonOf(out)
}

func itemFakeBackend(t *testing.T) leaseBackend {
	t.Helper()

	run := &itemBd{}
	items, err := beads.New(run, beadstest.Block("ct"))
	require.NoError(t, err)

	return leaseBackend{run: run, prefix: "ct", beads: items}
}

// stateOf reads the status and the labels of an item with bd show, the way a person would.
func stateOf(t *testing.T, b leaseBackend, id string) (string, []string) {
	t.Helper()

	done := b.run.Run(t.Context(), []string{"show", id, "--json"})
	require.Zero(t, done.ExitCode, done.Stderr)

	var rows []itemRow
	if strings.HasPrefix(strings.TrimSpace(done.Stdout), "[") {
		require.NoError(t, json.Unmarshal([]byte(done.Stdout), &rows))
	} else {
		var row itemRow

		require.NoError(t, json.Unmarshal([]byte(done.Stdout), &row))

		rows = []itemRow{row}
	}

	require.Len(t, rows, 1)

	return rows[0].Status, rows[0].Labels
}

func scenarioItemRoundTrip(t *testing.T, b leaseBackend) {
	t.Helper()

	ctx := t.Context()

	id, created, err := b.beads.Create(ctx, "rt-key", store.NewItem{Title: "round trip", Body: "the body"})
	require.NoError(t, err)
	assert.True(t, created, "the first create of a key makes the item")
	require.NotEmpty(t, id)

	again, createdAgain, err := b.beads.Create(ctx, "rt-key", store.NewItem{Title: "round trip", Body: "the body"})
	require.NoError(t, err)
	assert.False(t, createdAgain, "a second create of the same key finds the item")
	assert.Equal(t, id, again)

	item, err := b.beads.Item(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "round trip", item["title"])
	assert.Contains(t, item["body"], "the body")

	require.NoError(t, b.beads.Comment(ctx, id, "note-1", "first note"))
	require.NoError(t, b.beads.Comment(ctx, id, "note-1", "first note"))

	texts, err := b.beads.Comments(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"first note"}, texts, "a comment of one key is added once")

	require.NoError(t, b.beads.SetStatus(ctx, id, store.StatusHold))

	status, labels := stateOf(t, b, id)
	assert.Equal(t, "open", status)
	assert.Contains(t, labels, "hold")

	require.NoError(t, b.beads.SetStatus(ctx, id, store.StatusClosed))

	status, _ = stateOf(t, b, id)
	assert.Equal(t, "closed", status)

	require.NoError(t, b.beads.SetStatus(ctx, id, store.StatusOpen))

	status, labels = stateOf(t, b, id)
	assert.Equal(t, "open", status)
	assert.NotContains(t, labels, "hold")
}

func TestStoreContract_ItemRoundTrip(t *testing.T) {
	t.Parallel()

	for _, kind := range []struct {
		name  string
		build func(*testing.T) leaseBackend
	}{{"fake bd", itemFakeBackend}, {"real bd 1.3.2", realBackend}} {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			scenarioItemRoundTrip(t, kind.build(t))
		})
	}
}
