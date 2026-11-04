package beads

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

var _ store.Writer = (*Store)(nil)

const (
	statusClosed = "closed"
	argComments  = "comments"
)

type bdComment struct {
	Text string `json:"text"`
}

// Create files an item for the key and returns its id. It writes the line `obee-key: <key>` at the end of the description and, before it writes,
// lists all items whose description holds that line, so a repeated key returns the first item. bd create --silent prints the new id and nothing else.
// A parent is set with --parent. The lookup matches the whole line, so the key r:1 does not match r:10.
func (s *Store) Create(ctx context.Context, key string, item store.NewItem) (string, bool, error) {
	found, err := s.keyed(ctx, key)
	if err != nil || found != "" {
		return found, false, err
	}

	done := s.run.Run(ctx, append(createArgs(key, item), "--silent"))
	if done.ExitCode != 0 {
		return "", false, store.Refuse("REFUSE: cannot create an item in beads")
	}

	id, err := readableID(strings.TrimSpace(done.Stdout))

	return id, err == nil, err
}

// createArgs are the arguments of bd create for the item, with the marker of the key. The parent and the assignee are set only when the item has one.
func createArgs(key string, item store.NewItem) []string {
	args := []string{"create", "--title", item.Title, "--description", store.WithMarker(item.Body, key)}
	if item.Parent != "" {
		args = append(args, "--parent", item.Parent)
	}

	if item.Assignee != "" {
		args = append(args, "--assignee", item.Assignee)
	}

	if item.Severity != "" {
		args = append(args, "--priority", strconv.Itoa(slices.Index(model.Severities(), model.Severity(item.Severity))))
	}

	if item.Operator {
		args = append(args, "--labels", store.OperatorLabel)
	}

	return args
}

// keyed returns the id of the item whose description holds the marker line of the key, or empty.
func (s *Store) keyed(ctx context.Context, key string) (string, error) {
	rows, err := s.list(ctx, []string{argList, "--all", "--desc-contains", store.Marker(key)}, "the items of a key", "store: cannot search beads; refusing to write")
	if err != nil {
		return "", err
	}

	for _, row := range rows {
		if store.HasMarker(derefText(row.Description), key) {
			return readableID(row.ID)
		}
	}

	return "", nil
}

// SetStatus reads the item and runs only the commands that change it: close, reopen, and the hold label. open = no hold label and not closed.
func (s *Store) SetStatus(ctx context.Context, id string, status store.Status) error {
	row, err := s.shown(ctx, id)
	if err != nil {
		return err
	}

	if status == store.StatusClosed {
		return s.step(ctx, row.Status != statusClosed, "close", id)
	}

	if err = s.step(ctx, row.Status == statusClosed, "reopen", id); err != nil {
		return err
	}

	held := slices.Contains(row.Labels, holdLabel)
	if status == store.StatusHold {
		return s.step(ctx, !held, argUpdate, id, "--add-label", holdLabel)
	}

	return s.step(ctx, held, argUpdate, id, "--remove-label", holdLabel)
}

// step runs one bd command on an item when run is true.
func (s *Store) step(ctx context.Context, run bool, command, id string, extra ...string) error {
	if !run {
		return nil
	}

	done := s.run.Run(ctx, append([]string{command, id}, extra...))
	if done.ExitCode != 0 {
		return store.Refuse("REFUSE: cannot %s item %s in beads", command, id)
	}

	return nil
}

// Assign sets the assignee of the item when it has none or already has this login. beads keeps one assignee, so another holder is refused and nothing is written.
// Setting the same assignee again leaves the same state, so the call is safe to repeat.
func (s *Store) Assign(ctx context.Context, id, assignee string) error {
	row, err := s.shown(ctx, id)
	if err != nil {
		return err
	}

	if row.Assignee != "" && row.Assignee != assignee {
		return store.Refuse("REFUSE: item %s is assigned to %s, and beads keeps one assignee. Ask %s to give it up, or use a tracker that keeps many", id, row.Assignee, row.Assignee)
	}

	return s.step(ctx, true, argUpdate, id, "--assignee", assignee)
}

// Comment adds a comment with the marker line of the key at its end, once for each key: it lists the comments of the item first and returns when one holds the marker.
func (s *Store) Comment(ctx context.Context, id, key, text string) error {
	seen, err := s.commented(ctx, id, key)
	if err != nil || seen {
		return err
	}

	added := s.run.Run(ctx, []string{argComments, "add", id, "--", store.WithMarker(text, key)})
	if added.ExitCode != 0 {
		return store.Refuse("REFUSE: cannot comment on item %s in beads", id)
	}

	return nil
}

// commented reports whether a comment of the item holds the marker line of the key.
func (s *Store) commented(ctx context.Context, id, key string) (bool, error) {
	done := s.run.Run(ctx, []string{argComments, id, argJSON})
	if done.ExitCode != 0 {
		return false, store.Refuse("REFUSE: cannot read the comments of item %s in beads", id)
	}

	seen, err := model.Decode[[]bdComment]("bd comments", done.Stdout)
	if err != nil {
		return false, store.Unreadable(err)
	}

	return slices.ContainsFunc(seen, func(c bdComment) bool { return store.HasMarker(c.Text, key) }), nil
}
