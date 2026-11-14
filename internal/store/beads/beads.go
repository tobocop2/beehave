// Package beads implements store.Tracker and store.ClaimRows over the bd command line.
package beads

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store"
)

const (
	maxStderrRunes = 200
	bdPage         = 5000
	lockPrefix     = "lock:"
	epicType       = "epic"
	argJSON        = "--json"
	argList        = "list"
	holdLabel      = "hold"
)

// readableID returns the id, or a refusal when the answer has none.
func readableID(id string) (string, error) {
	if id == "" {
		return "", store.Refuse("store: an item has no readable id; refusing")
	}

	return id, nil
}

type bdRow struct {
	ID          string   `json:"id"`
	Title       *string  `json:"title"`
	IssueType   string   `json:"issue_type"`
	Priority    *int     `json:"priority"`
	Assignee    string   `json:"assignee"`
	Description *string  `json:"description"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	// Parent is the id of the parent that bd show prints for a child item. It is empty for an item with no parent.
	Parent string `json:"parent"`

	LeaseExpiresAt string `json:"lease_expires_at"`
}

// Store is one beads database, read and written through bd. It implements store.Tracker and store.Writer.
type Store struct {
	run    proc.Runner
	prefix string

	mu      sync.Mutex
	guarded bool
}

// The names the beads adapter is chosen by: the tracker and the claim store.
const (
	Tracker model.TrackerName = "beads"
	Claims  model.ClaimsName  = "beads"
)

// Tool is the program that the beads adapter needs on the path.
const Tool = "bd"

// BlockPrefix is the key of the id prefix in the beads block.
const BlockPrefix = "prefix"

// Block returns the schema of the beads block: the id prefix of the database, which the project must give.
func Block() config.Block {
	return config.Block{Name: "beads", Args: []config.Arg{{Key: BlockPrefix}}}
}

// New returns a store that runs bd through run. block is the beads block of the project: the id prefix of the beads database, from which claim rows are named.
func New(run proc.Runner, block *model.Block) (*Store, error) {
	prefix, given := block.Text(BlockPrefix)
	if !given || strings.TrimSpace(prefix) == "" {
		return nil, store.Refuse("store: the beads id prefix is required")
	}

	return &Store{run: run, prefix: prefix}, nil
}

// OpenItems lists the items bd ready names. Epics and claim rows are not work.
func (s *Store) OpenItems(ctx context.Context) ([]model.Item, error) {
	ready, err := s.list(ctx, []string{"ready"}, "the ready list", "store: cannot read beads; refusing to list work")
	if err != nil {
		return nil, err
	}

	held, err := s.heldIDs(ctx)
	if err != nil {
		return nil, err
	}

	parents, err := s.parents(ctx)
	if err != nil {
		return nil, err
	}

	return queue(ready, held, parents)
}

// heldIDs reads the ids that carry the hold label. bd ready does not send the label.
func (s *Store) heldIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.list(ctx, []string{"ready", "--label", holdLabel}, "the held list", "store: cannot read the held items; refusing to list work")
	if err != nil {
		return nil, err
	}

	held := make(map[string]bool, len(rows))
	for _, row := range rows {
		held[row.ID] = true
	}

	return held, nil
}

// queue turns the ready rows into items. bd ready sends no parent, so the parents come in as a lookup.
func queue(ready []bdRow, held map[string]bool, parents map[string]string) ([]model.Item, error) {
	items := make([]model.Item, 0, len(ready))

	for _, row := range ready {
		item, err := row.item(held, parents)
		if err != nil {
			return nil, err
		}

		if row.IssueType != epicType && !strings.HasPrefix(item.Title, lockPrefix) {
			items = append(items, item)
		}
	}

	return items, nil
}

func (r bdRow) item(held map[string]bool, parents map[string]string) (model.Item, error) {
	title, err := store.ReadableTitle(r.Title)
	if err != nil {
		return model.Item{}, err
	}

	id, err := readableID(r.ID)

	return model.Item{
		ID: id, Severity: beadsSeverity(r.Priority), Hold: held[id],
		Parent: parents[id], Assignee: r.Assignee, Title: title,
	}, err
}

// beadsSeverity maps beads priority 0 to 4 onto P0 to P3, reading 4 as P3.
func beadsSeverity(priority *int) model.Severity {
	if priority == nil || *priority < 0 || *priority > len(model.Severities()) {
		return model.SeverityUnknown
	}

	return model.Severities()[min(*priority, len(model.Severities())-1)]
}

func (s *Store) parents(ctx context.Context) (map[string]string, error) {
	containers, err := s.containers(ctx)
	if err != nil {
		return nil, err
	}

	parents := map[string]string{}

	for _, container := range containers {
		if err = s.addChildren(ctx, parents, container); err != nil {
			return nil, err
		}
	}

	return parents, nil
}

// addChildren records the container as the parent of each child that has none yet.
func (s *Store) addChildren(ctx context.Context, parents map[string]string, container string) error {
	children, err := s.children(ctx, container)

	for _, child := range children {
		if _, seen := parents[child]; !seen {
			parents[child] = container
		}
	}

	return err
}

// containers lists the ids of the open epics.
func (s *Store) containers(ctx context.Context) ([]string, error) {
	rows, err := s.list(ctx, []string{argList, "--status=open", argType, epicType}, "the containers list", "store: cannot read beads containers; refusing")
	if err != nil {
		return nil, err
	}

	return ids(rows)
}

func (s *Store) children(ctx context.Context, container string) ([]string, error) {
	done := s.run.Run(ctx, []string{"children", container, argJSON})
	if done.ExitCode != 0 {
		return nil, store.Refuse("store: cannot read the children of %s; refusing to list work", container)
	}

	rows, err := model.Decode[[]bdRow]("bd children", done.Stdout)
	if err != nil {
		return nil, store.Unreadable(err)
	}

	return ids(rows)
}

func ids(rows []bdRow) ([]string, error) {
	out := make([]string, len(rows))

	for i, row := range rows {
		id, err := readableID(row.ID)
		if err != nil {
			return nil, err
		}

		out[i] = id
	}

	return out, nil
}

// shown runs bd show for an item and decodes its row.
func (s *Store) shown(ctx context.Context, item string) (bdRow, error) {
	done := s.run.Run(ctx, []string{"show", item, argJSON})
	if done.ExitCode != 0 {
		return bdRow{}, store.Refuse("REFUSE: cannot read item %s in beads", item)
	}

	return decodeShown(item, done.Stdout)
}

// ItemFields declares the fields that Item returns.
func (*Store) ItemFields() []string { return model.ItemFields() }

// Item returns the id, title, description and severity of an item.
func (s *Store) Item(ctx context.Context, item string) (map[string]string, error) {
	row, err := s.shown(ctx, item)
	if err != nil {
		return nil, err
	}

	title, err := store.ReadableTitle(row.Title)
	if err != nil {
		return nil, err
	}

	id, err := readableID(row.ID)
	if err != nil {
		return nil, err
	}

	return map[string]string{"id": id, "title": title, "body": model.Deref(row.Description), "severity": string(beadsSeverity(row.Priority))}, nil
}

// decodeShown reads the row of bd show, which prints one object or a list of one.
func decodeShown(item, text string) (bdRow, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "[") {
		row, err := model.Decode[bdRow]("bd show", text)

		return row, store.Unreadable(err)
	}

	rows, err := model.Decode[[]bdRow]("bd show", text)
	if err != nil {
		return bdRow{}, store.Unreadable(err)
	}

	if len(rows) == 0 {
		return bdRow{}, store.Refuse("REFUSE: beads returned nothing for %s", item)
	}

	return rows[0], nil
}

// list reads one page of a beads list. A full page may be cut off, so it is a refusal.
func (s *Store) list(ctx context.Context, command []string, what, failure string) ([]bdRow, error) {
	args := append(append([]string{}, command...), "--limit", strconv.Itoa(bdPage), argJSON)

	done := s.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return nil, store.Refuse("%s", failure)
	}

	return store.DecodeRows[bdRow]("bd "+command[0], done.Stdout, bdPage, what)
}
