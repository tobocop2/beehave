package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/tobocop2/obee/internal/model"
)

const (
	maxStderrRunes = 200
	bdPage         = 5000
	lockPrefix     = "lock:"
	epicType       = "epic"
	taskType       = "task"
)

// beadsType is an item type beads knows. Any other kind a caller files becomes a label on a task.
type beadsType string

// The item types beads accepts for create.
const (
	beadsBug     beadsType = "bug"
	beadsTask    beadsType = "task"
	beadsFeature beadsType = "feature"
	beadsChore   beadsType = "chore"
)

func (t beadsType) known() bool {
	switch t {
	case beadsBug, beadsTask, beadsFeature, beadsChore:
		return true
	}

	return false
}

type bdRow struct {
	ID          string  `json:"id"`
	Title       *string `json:"title"`
	IssueType   string  `json:"issue_type"`
	Priority    *int    `json:"priority"`
	Assignee    string  `json:"assignee"`
	Description *string `json:"description"`
	Status      string  `json:"status"`

	LeaseExpiresAt string `json:"lease_expires_at"`
}

// BeadsStore is one beads database, read and written through bd. It implements model.Tracker.
type BeadsStore struct {
	run    Runner
	prefix string

	mu      sync.Mutex
	guarded bool
}

// NewBeadsStore returns a store that runs bd through run. prefix is the id prefix of the beads database; claim rows are named from it.
func NewBeadsStore(run Runner, prefix string) (*BeadsStore, error) {
	if prefix == "" {
		return nil, refuse("store: the beads id prefix is required")
	}

	return &BeadsStore{run: run, prefix: prefix}, nil
}

// OpenItems lists the items bd ready names. Epics and claim rows are not work.
func (s *BeadsStore) OpenItems(ctx context.Context) ([]model.Item, error) {
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
func (s *BeadsStore) heldIDs(ctx context.Context) (map[string]bool, error) {
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
	title, err := readableTitle(r.Title)
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

func (s *BeadsStore) parents(ctx context.Context) (map[string]string, error) {
	containers, err := s.Containers(ctx)
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
func (s *BeadsStore) addChildren(ctx context.Context, parents map[string]string, container string) error {
	children, err := s.children(ctx, container, "; refusing to list work")

	for _, child := range children {
		if _, seen := parents[child]; !seen {
			parents[child] = container
		}
	}

	return err
}

// Containers lists the ids of the open epics.
func (s *BeadsStore) Containers(ctx context.Context) ([]string, error) {
	rows, err := s.list(ctx, []string{argList, "--status=open", argType, epicType}, "the containers list", "store: cannot read beads containers; refusing")
	if err != nil {
		return nil, err
	}

	return ids(rows)
}

// Children lists the ids of the open children of one epic.
func (s *BeadsStore) Children(ctx context.Context, container string) ([]string, error) {
	return s.children(ctx, container, "; refusing")
}

func (s *BeadsStore) children(ctx context.Context, container, tail string) ([]string, error) {
	done := s.run.Run(ctx, []string{"children", container, argJSON})
	if done.ExitCode != 0 {
		return nil, refuse("store: cannot read the children of %s%s", container, tail)
	}

	rows, err := decode[[]bdRow]("bd children", done.Stdout)
	if err != nil {
		return nil, err
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

// Show returns the title, description and comments of an item.
func (s *BeadsStore) Show(ctx context.Context, item string) (string, error) {
	done := s.run.Run(ctx, []string{"show", item, argJSON})
	if done.ExitCode != 0 {
		return "", refuse("REFUSE: cannot read item %s in beads", item)
	}

	row, err := decodeShown(item, done.Stdout)
	if err != nil {
		return "", err
	}

	head, err := row.heading()
	if err != nil {
		return "", err
	}

	return s.withComments(ctx, item, head)
}

func (s *BeadsStore) withComments(ctx context.Context, item, head string) (string, error) {
	comments := s.run.Run(ctx, []string{"comments", item})
	if comments.ExitCode != 0 {
		return "", refuse("REFUSE: cannot read the comments of %s in beads", item)
	}

	return head + comments.Stdout, nil
}

func (r bdRow) heading() (string, error) {
	title, err := readableTitle(r.Title)
	if err != nil {
		return "", err
	}

	id, err := readableID(r.ID)

	return fmt.Sprintf("# %s %s\n\n%s\n", id, title, derefText(r.Description)), err
}

// decodeShown reads the row of bd show, which prints one object or a list of one.
func decodeShown(item, text string) (bdRow, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return decode[bdRow]("bd show", text)
	}

	rows, err := decode[[]bdRow]("bd show", text)
	if err != nil {
		return bdRow{}, err
	}

	if len(rows) == 0 {
		return bdRow{}, refuse("REFUSE: beads returned nothing for %s", item)
	}

	return rows[0], nil
}

// Close closes an item with a reason. A rejected close is an error with code 1.
func (s *BeadsStore) Close(ctx context.Context, item, reason string) error {
	done := s.run.Run(ctx, []string{"close", item, "--reason", reason})
	if done.ExitCode != 0 {
		return &Error{Code: CodeCloseFailed, Message: fmt.Sprintf("store: cannot close %s: %s", item, strings.TrimSpace(done.Stderr))}
	}

	return nil
}

// Create files an item and returns its id. The duplicate check reads every item that is not closed.
func (s *BeadsStore) Create(ctx context.Context, request model.CreateRequest) (string, error) {
	if err := validated(request); err != nil {
		return "", err
	}

	titles, err := s.openTitles(ctx)
	if err != nil {
		return "", err
	}

	if err = refuseDuplicate(request, titles); err != nil {
		return "", err
	}

	return s.file(ctx, request)
}

func (s *BeadsStore) file(ctx context.Context, request model.CreateRequest) (string, error) {
	done := s.run.Run(ctx, createArgs(request))
	if done.ExitCode != 0 {
		return "", refuse("store: bd create returned no id")
	}

	made, err := decode[bdRow]("bd create", done.Stdout)
	if err != nil {
		return "", err
	}

	return readableCreated(made.ID)
}

func (s *BeadsStore) openTitles(ctx context.Context) ([]openTitle, error) {
	rows, err := s.list(ctx, []string{argList}, "the duplicate check", "store: cannot read beads for the duplicate check; refusing to file")
	if err != nil {
		return nil, err
	}

	titles := make([]openTitle, len(rows))

	for i, row := range rows {
		if titles[i], err = row.openTitle(); err != nil {
			return nil, err
		}
	}

	return titles, nil
}

func (r bdRow) openTitle() (openTitle, error) {
	title, err := readableTitle(r.Title)
	if err != nil {
		return openTitle{}, err
	}

	id, err := readableID(r.ID)

	return openTitle{id: id, title: title}, err
}

func readableCreated(id string) (string, error) {
	if id == "" {
		return "", refuse("store: bd create returned no id")
	}

	return id, nil
}

func createArgs(request model.CreateRequest) []string {
	kind, label := request.Kind, ""
	if !beadsType(kind).known() {
		kind, label = taskType, request.Kind
	}

	args := []string{
		argCreate, request.Title, "-t", kind, "-p", strings.TrimPrefix(string(request.Severity), "P"),
		"--body-file", request.BodyFile, argJSON,
	}

	return appendOptional(args, optional{"--labels", label}, optional{"--parent", request.Parent}, optional{"--assignee", request.Assignee})
}

type optional struct {
	flag  string
	value string
}

// appendOptional adds each flag whose value is not empty.
func appendOptional(args []string, flags ...optional) []string {
	for _, f := range flags {
		if f.value != "" {
			args = append(args, f.flag, f.value)
		}
	}

	return args
}

// list reads one page of a beads list. A full page may be cut off, so it is a refusal.
func (s *BeadsStore) list(ctx context.Context, command []string, what, failure string) ([]bdRow, error) {
	args := append(append([]string{}, command...), "--limit", strconv.Itoa(bdPage), argJSON)

	done := s.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return nil, refuse("%s", failure)
	}

	return decodeRows[bdRow]("bd "+command[0], done.Stdout, bdPage, what)
}

func firstRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		runes = runes[:limit]
	}

	return string(runes)
}
