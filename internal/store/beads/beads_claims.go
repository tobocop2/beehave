package beads

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

const (
	statusProgress = "in_progress"
	maxTitleRunes  = 70
	noIssueText    = "no issue found matching"
	noIssuesText   = "no issues found matching"
	guardMismatch  = 13
	exitRefused    = 1
	// The sentinel texts of bd 1.3.2 that start its refusals (internal/storage/issueops/errors.go:12, :17 and :62). A refused take
	// (claim.go:161, :184, :186 and aggregate.go:173) and a release that lost (unclaim.go:181 and :222) both exit 1, not 13.
	// A release of a closed row says textClosed (unclaim.go:48 and :170): the row holds nothing.
	textClaimed      = "issue already claimed"
	textNotClaimable = "issue not claimable"
	textMismatch     = "assignee mismatch"
	textClosed       = "cannot unclaim closed issue"
	argActor         = "--actor"
	argClaim         = "--claim"
	argUnclaim       = "unclaim"
	statusOpen       = "open"
	argIfAssignee    = "--if-assignee"
	argIfStatus      = "--if-status"
	argStatus        = "--status"
	argUpdate        = "update"
	argAssignee      = "--assignee"
	argDescription   = "--description"
	argType          = "--type"
	argTitle         = "--title"
	argCreate        = "create"
	argID            = "--id"
	rowsHeldLabel    = "the held rows"
	claimNote        = "Claim row. Held while in_progress; the assignee holds it and bd keeps its lease."
)

// reclaimed is the answer of bd reclaim --json: the rows whose lease ended and that went back to open.
type reclaimed struct {
	Reclaimed []reclaimedRow `json:"reclaimed"`
}

type reclaimedRow struct {
	ID string `json:"id"`
}

// HeldRows returns every in_progress claim row with the end of its lease. A row with no lease has the zero LeaseEnds.
func (s *Store) HeldRows(ctx context.Context) ([]model.ClaimRow, error) {
	rows, err := s.list(ctx, []string{"list", "--status=" + statusProgress}, rowsHeldLabel, "store: cannot read "+rowsHeldLabel+"; refusing to answer")
	if err != nil {
		return nil, err
	}

	var held []model.ClaimRow

	for _, r := range rows {
		row, ok, err := claimRowOf(r)
		if err != nil {
			return nil, err
		}

		if ok {
			held = append(held, row)
		}
	}

	return held, nil
}

// claimRowOf reads a claim row from a bd row. It reports false for a row that is no claim row.
func claimRowOf(r bdRow) (model.ClaimRow, bool, error) {
	item, ok := claimedItem(model.Deref(r.Title))
	if !ok || r.ID == "" {
		return model.ClaimRow{}, false, nil
	}

	ends, err := leaseEnd(r)

	return model.ClaimRow{Item: item, Holder: r.Assignee, LeaseEnds: ends}, err == nil, err
}

// leaseEnd reads lease_expires_at of a row. A row with no lease has none, and a text that is no time is a refusal.
func leaseEnd(r bdRow) (time.Time, error) {
	if r.LeaseExpiresAt == "" {
		return time.Time{}, nil
	}

	ends, err := time.Parse(time.RFC3339Nano, r.LeaseExpiresAt)
	if err != nil {
		return time.Time{}, store.Refuse("store: the lease end %q of %s is not a time; refusing to answer", r.LeaseExpiresAt, r.ID)
	}

	return ends, nil
}

// claimedItem returns the item a claim row title names, or false when the title is not a claim row of a known kind.
func claimedItem(title string) (string, bool) {
	if !strings.HasPrefix(title, lockPrefix+model.KindForge+"-") && !strings.HasPrefix(title, lockPrefix+model.KindBeads+"-") {
		return "", false
	}

	word, _, _ := strings.Cut(strings.TrimPrefix(title, lockPrefix), " ")
	kind, item, _ := strings.Cut(word, "-")

	return item, model.ItemKind(item) == kind
}

// Beat renews the lease of every claim row the holder holds with bd heartbeat. A holder with no row has lost its claim.
func (s *Store) Beat(ctx context.Context, holder string) error {
	if err := s.requireGuards(ctx); err != nil {
		return err
	}

	rows, err := s.HeldRows(ctx)
	if err != nil {
		return err
	}

	mine := slices.DeleteFunc(rows, func(row model.ClaimRow) bool { return row.Holder != holder })
	if len(mine) == 0 {
		return fmt.Errorf("store: %s holds no claim row: %w", holder, model.ErrNoClaim)
	}

	return s.heartbeatAll(ctx, mine, holder)
}

// heartbeatAll renews every row, so one bad row does not starve the others toward their lease end. It joins the errors.
func (s *Store) heartbeatAll(ctx context.Context, rows []model.ClaimRow, holder string) error {
	var errs []error

	for _, row := range rows {
		errs = append(errs, s.heartbeat(ctx, s.rowID(row.Item), holder))
	}

	return errors.Join(errs...)
}

// heartbeat renews the lease of one row. A row that another holder took since the list is no claim of this holder: bd refuses with the text of a lost take.
func (s *Store) heartbeat(ctx context.Context, rowID, holder string) error {
	done := s.run.Run(ctx, []string{"heartbeat", rowID, argActor, holder})
	if refusedWith(done, textClaimed) {
		return fmt.Errorf("store: %s no longer holds %s, which another holder took: %w", holder, rowID, model.ErrNoClaim)
	}

	if done.ExitCode != 0 {
		return store.Refuse("store: heartbeat for %s failed: %s", rowID, stderrText(done))
	}

	return nil
}

func (s *Store) rowID(item string) string {
	return fmt.Sprintf("%s-lock-%s-%s", s.prefix, model.ItemKind(item), item)
}

// showRow returns the row with exactly this id, or nil when it does not exist.
func (s *Store) showRow(ctx context.Context, rowID string) (*bdRow, error) {
	done := s.run.Run(ctx, []string{"show", rowID, argJSON})
	if done.ExitCode != 0 {
		if isMissing(done.Stderr, rowID) {
			return nil, nil //nolint:nilnil // a missing row is an answer, not an error
		}

		return nil, store.Refuse("store: cannot read the row %s: %s", rowID, stderrText(done))
	}

	shown, err := decodeShown(rowID, done.Stdout)
	if err != nil {
		return nil, err
	}

	// bd show resolves a partial id by prefix, so a short id returns the longer row.
	if shown.ID != rowID {
		return nil, nil //nolint:nilnil // a missing row is an answer, not an error
	}

	return &shown, nil
}

// isMissing reports whether the stderr of bd names a row that does not exist. Both texts are in the bd source:
// v0.56.1 prints `Error fetching <id>: no issue found matching "<id>"` (utils/id_parser.go:140, cmd/bd/show.go:105)
// and v1.3.2 prints `Issue <id> not found` (cmd/bd/show.go:136); with --json both end with `no issues found matching the provided IDs`.
func isMissing(stderr, id string) bool {
	text := strings.ToLower(stderr)

	return strings.Contains(text, noIssueText) || strings.Contains(text, noIssuesText) || strings.Contains(text, "issue "+strings.ToLower(id)+" not found")
}

// TryHold makes the row if it is missing, clears a stale holder, and claims the row with bd's atomic claim as the holder.
// bd grants the lease to its actor (issueops/claim.go:197-203), so the holder is the actor and the assignee: a take that also passes
// --assignee deletes the lease (cmd/bd/update.go:494). The clear is guarded by the assignee and status that were read, so it cannot wipe a claim made since.
// A caller reads HeldRows after TryHold to learn the winner.
func (s *Store) TryHold(ctx context.Context, row model.ClaimRow) error {
	if err := s.requireGuards(ctx); err != nil {
		return err
	}

	rowID := s.rowID(row.Item)

	shown, err := s.ensureRow(ctx, rowID, row)
	if err != nil {
		return err
	}

	if cleared, err := s.clearStale(ctx, rowID, shown); err != nil || !cleared {
		return err
	}

	return s.claim(ctx, rowID, row.Holder)
}

// ensureRow shows the claim row and makes it when it is missing.
func (s *Store) ensureRow(ctx context.Context, rowID string, row model.ClaimRow) (*bdRow, error) {
	shown, err := s.showRow(ctx, rowID)
	if err != nil || shown != nil {
		return shown, err
	}

	return s.createRow(ctx, rowID, row)
}

// claim runs bd update --claim as the holder. bd takes only an open row, and a refused take exits 1 with the text of a lost take, so
// that exit is no error: the caller reads the rows to learn the winner. Any other failure is an error.
func (s *Store) claim(ctx context.Context, rowID, holder string) error {
	done := s.run.Run(ctx, []string{argUpdate, rowID, argClaim, argActor, holder})
	if done.ExitCode == 0 || refusedWith(done, textClaimed, textNotClaimable) {
		return nil
	}

	return store.Refuse("store: cannot claim the row %s: %s", rowID, stderrText(done))
}

// stderrText is the stderr of a failed bd call, trimmed and cut for an error message.
func stderrText(done model.CommandResult) string {
	return model.FirstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes)
}

// refusedWith reports a bd refusal: exit 1 with one of the sentinel texts. Any other exit 1 is an error.
func refusedWith(done model.CommandResult, texts ...string) bool {
	return done.ExitCode == exitRefused && slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(done.Stderr, text) })
}

// clearStale gives a row back that holds nothing but cannot be taken: bd takes only an open row without an assignee.
// The reset is guarded by the assignee and status that were read. It reports false when another take got there first.
func (s *Store) clearStale(ctx context.Context, rowID string, shown *bdRow) (bool, error) {
	if shown.Status == statusProgress && shown.Assignee != "" || shown.Status == statusOpen && shown.Assignee == "" {
		return true, nil
	}

	guard := []string{argIfAssignee, shown.Assignee, argIfStatus, shown.Status}

	return s.updateIf(ctx, rowID, guard, argStatus, statusOpen, argAssignee, "")
}

// updateIf runs bd update with the guard arguments. It reports false when the guard did not match: bd then writes nothing and exits 13.
func (s *Store) updateIf(ctx context.Context, rowID string, guard []string, args ...string) (bool, error) {
	done := s.run.Run(ctx, slices.Concat([]string{argUpdate, rowID}, guard, args))
	if done.ExitCode == guardMismatch {
		return false, nil
	}

	if done.ExitCode != 0 {
		return false, store.Refuse("store: cannot update the row %s (the guards need bd 1.3.2 or newer): %s", rowID, stderrText(done))
	}

	return true, nil
}

func (s *Store) createRow(ctx context.Context, rowID string, row model.ClaimRow) (*bdRow, error) {
	title := s.itemTitle(ctx, row.Item)
	rowTitle := strings.TrimRight(fmt.Sprintf("%s%s-%s %s", lockPrefix, model.ItemKind(row.Item), row.Item, model.FirstRunes(title, maxTitleRunes)), " \t\r\n")

	s.run.Run(ctx, []string{argCreate, argID, rowID, argTitle, rowTitle, argType, "task", "--priority", "3", "--silent", argDescription, claimNote})

	shown, err := s.showRow(ctx, rowID)
	if err == nil && shown == nil {
		err = store.Refuse("store: could not create the claim row %s", rowID)
	}

	return shown, err
}

// itemTitle returns the title of a beads item. A forge number has no beads title, and the row title stays bare.
func (s *Store) itemTitle(ctx context.Context, item string) string {
	if model.ItemKind(item) != model.KindBeads {
		return ""
	}

	shown, err := s.showRow(ctx, item)
	if err != nil || shown == nil {
		return ""
	}

	return model.Deref(shown.Title)
}

// Reclaim runs bd reclaim for the row of the item with no grace window, so it frees the row only when the lease of its holder ended.
// It reports whether the row went back to open. The scope --id keeps it from freeing any other row (cmd/bd/reclaim.go:161).
func (s *Store) Reclaim(ctx context.Context, item string) (bool, error) {
	if err := s.requireGuards(ctx); err != nil {
		return false, err
	}

	rowID := s.rowID(item)

	done := s.run.Run(ctx, []string{"reclaim", "--id", rowID, "--older-than", "0s", argJSON})
	if done.ExitCode != 0 {
		return false, store.Refuse("store: cannot reclaim the row %s: %s", rowID, stderrText(done))
	}

	freed, err := model.Decode[reclaimed]("bd reclaim", done.Stdout)
	if err != nil {
		return false, store.Unreadable(err)
	}

	return slices.ContainsFunc(freed.Reclaimed, func(r reclaimedRow) bool { return r.ID == rowID }), nil
}

// Free gives the row back as open and clears its holder, only while the holder still holds the row. It runs bd unclaim,
// which reopens the row so the next take can claim it. A mismatch writes nothing. The row is then free, or held by another holder, which is ErrRowMoved.
func (s *Store) Free(ctx context.Context, row model.ClaimRow) error {
	if err := s.requireGuards(ctx); err != nil {
		return err
	}

	rowID := s.rowID(row.Item)

	if released, err := s.release(ctx, rowID, row.Holder); err != nil || released {
		return err
	}

	shown, err := s.showRow(ctx, rowID)
	if err != nil || shown == nil || shown.Status != statusProgress {
		return err
	}

	return fmt.Errorf("store: the claim row of #%s is held by %s now: %w", row.Item, shown.Assignee, model.ErrRowMoved)
}

// release runs bd unclaim for the holder. It reports false when the row was not released: it is held by another holder, or it holds nothing.
func (s *Store) release(ctx context.Context, rowID, holder string) (bool, error) {
	done := s.run.Run(ctx, []string{argUnclaim, rowID, argIfAssignee, holder, argActor, holder})
	if done.ExitCode == 0 {
		return true, nil
	}

	if refusedWith(done, textMismatch, textClosed) {
		return false, nil
	}

	return false, s.missingIsFree(ctx, rowID, store.Refuse("store: cannot release the row %s: %s", rowID, stderrText(done)))
}

// missingIsFree turns a failed update of a row that does not exist into success: nothing is held.
func (s *Store) missingIsFree(ctx context.Context, rowID string, failed error) error {
	shown, err := s.showRow(ctx, rowID)
	if err != nil {
		return err
	}

	if shown == nil {
		return nil
	}

	return failed
}
