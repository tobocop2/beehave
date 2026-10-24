package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const (
	statusProgress = "in_progress"
	agentMiddle    = "-agent-"
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
	argClaim         = "--claim"
	argUnclaim       = "unclaim"
	statusOpen       = "open"
	tokenSeparator   = "#"
	argIfAssignee    = "--if-assignee"
	argIfStatus      = "--if-status"
	argStatus        = "--status"
	argUpdate        = "update"
	argAssignee      = "--assignee"
	argDescription   = "--description"
	argType          = "--type"
	argTitle         = "--title"
	argCreate        = "create"
	typeAgent        = "agent"
	rowsHeldLabel    = "the held rows"
	rowsAgentsLabel  = "the agent beads"
)

// HeldRows returns every in_progress claim row.
func (s *BeadsStore) HeldRows(ctx context.Context) ([]model.ClaimRow, error) {
	rows, err := s.list(ctx, []string{"list", "--status=" + statusProgress}, rowsHeldLabel, "store: cannot read "+rowsHeldLabel+"; refusing to answer")
	if err != nil {
		return nil, err
	}

	var held []model.ClaimRow

	for _, r := range rows {
		if item, ok := claimedItem(derefText(r.Title)); ok && r.ID != "" {
			holder, nonce := splitAssignee(r.Assignee)
			held = append(held, model.ClaimRow{Item: item, Holder: holder, Lease: model.StoredLease(derefText(r.Description)), Nonce: nonce})
		}
	}

	return held, nil
}

// assigneeOf returns the assignee text of a take: the holder, then a token that never repeats.
// A guard on this text fails for any later take, even a later take by the same holder.
func assigneeOf(row model.ClaimRow) string {
	if row.Nonce == "" {
		return row.Holder
	}

	return row.Holder + tokenSeparator + row.Nonce
}

// splitAssignee returns the holder and the token of an assignee text. The holder is the name people see.
func splitAssignee(assignee string) (string, string) {
	holder, nonce, _ := strings.Cut(assignee, tokenSeparator)

	return holder, nonce
}

// guardOf returns the guard arguments that hold while the row is an in_progress take of row.
func guardOf(row model.ClaimRow) []string {
	return []string{argIfAssignee, assigneeOf(row), argIfStatus, statusProgress}
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

// LastSeen returns when each owner with an agent bead last beat.
func (s *BeadsStore) LastSeen(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.list(ctx, []string{"list", argType, typeAgent}, rowsAgentsLabel, "store: cannot read "+rowsAgentsLabel+"; refusing to answer")
	if err != nil {
		return nil, err
	}

	prefix := s.prefix + agentMiddle
	seen := map[string]time.Time{}

	for _, r := range rows {
		stamp, err := time.Parse(time.RFC3339Nano, r.LastActivity)
		if strings.HasPrefix(r.ID, prefix) && err == nil {
			seen[strings.TrimPrefix(r.ID, prefix)] = stamp
		}
	}

	return seen, nil
}

// Beat stamps the heartbeat of the holder's agent bead and creates the bead first.
func (s *BeadsStore) Beat(ctx context.Context, holder string) error {
	agent := s.prefix + agentMiddle + holder

	if err := s.ensureAgent(ctx, agent, holder); err != nil {
		return err
	}

	if done := s.run.Run(ctx, []string{"agent", "heartbeat", agent}); done.ExitCode != 0 {
		return refuse("store: heartbeat for %s failed: %s", agent, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes))
	}

	return nil
}

func (s *BeadsStore) ensureAgent(ctx context.Context, agent, holder string) error {
	shown, err := s.showRow(ctx, agent)
	if err != nil || shown != nil {
		return err
	}

	s.run.Run(ctx, []string{
		argCreate, "--id", agent, argTitle, "lock owner " + holder, argType, typeAgent, "--labels", "gt:agent",
		"--silent", argDescription, "Liveness for the claim: last_activity is this owner's last heartbeat.",
	})

	if shown, err = s.showRow(ctx, agent); shown == nil {
		return firstError(err, refuse("store: could not create the agent bead %s", agent))
	}

	return nil
}

func firstError(first, second error) error {
	if first != nil {
		return first
	}

	return second
}

func (s *BeadsStore) rowID(item string) string {
	return fmt.Sprintf("%s-lock-%s-%s", s.prefix, model.ItemKind(item), item)
}

// showRow returns the row with exactly this id, or nil when it does not exist.
func (s *BeadsStore) showRow(ctx context.Context, rowID string) (*bdRow, error) {
	done := s.run.Run(ctx, []string{"show", rowID, argJSON})
	if done.ExitCode != 0 {
		if isMissing(done.Stderr, rowID) {
			return nil, nil //nolint:nilnil // a missing row is an answer, not an error
		}

		return nil, refuse("store: cannot read the row %s: %s", rowID, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes))
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

// Row reads the claim row of the item.
func (s *BeadsStore) Row(ctx context.Context, item string) (model.RowState, error) {
	shown, err := s.showRow(ctx, s.rowID(item))
	if err != nil || shown == nil {
		return model.RowState{}, err
	}

	holder, nonce := splitAssignee(shown.Assignee)

	return model.RowState{Exists: true, Held: shown.Status == statusProgress, Holder: holder, Nonce: nonce}, nil
}

// TryHold makes the row if it is missing, clears a stale holder, and claims the row with bd's atomic claim.
// The assignee of the take is the holder and a token that never repeats. The clear is guarded by the assignee and status that were read,
// so it cannot wipe a claim made since. bd does not take a guard and --claim together, so a caller reads Row after TryHold to learn the winner.
func (s *BeadsStore) TryHold(ctx context.Context, row model.ClaimRow) error {
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

	return s.claim(ctx, rowID, row)
}

// ensureRow shows the claim row and makes it when it is missing.
func (s *BeadsStore) ensureRow(ctx context.Context, rowID string, row model.ClaimRow) (*bdRow, error) {
	shown, err := s.showRow(ctx, rowID)
	if err != nil || shown != nil {
		return shown, err
	}

	return s.createRow(ctx, rowID, row)
}

// claim runs bd update --claim. bd takes only an open row, and a refused take exits 1 with the text of a lost take, so
// that exit is no error: the caller reads the row to learn the winner. Any other failure is an error.
func (s *BeadsStore) claim(ctx context.Context, rowID string, row model.ClaimRow) error {
	done := s.run.Run(ctx, []string{argUpdate, rowID, argClaim, argAssignee, assigneeOf(row)})
	if done.ExitCode == 0 || refusedWith(done, textClaimed, textNotClaimable) {
		return nil
	}

	return refuse("store: cannot claim the row %s: %s", rowID, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes))
}

// refusedWith reports a bd refusal: exit 1 with one of the sentinel texts. Any other exit 1 is an error.
func refusedWith(done model.CommandResult, texts ...string) bool {
	return done.ExitCode == exitRefused && slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(done.Stderr, text) })
}

// clearStale gives a row back that holds nothing but cannot be taken: bd takes only an open row without an assignee.
// The reset is guarded by the assignee and status that were read. It reports false when another take got there first.
func (s *BeadsStore) clearStale(ctx context.Context, rowID string, shown *bdRow) (bool, error) {
	if shown.Status == statusProgress && shown.Assignee != "" || shown.Status == statusOpen && shown.Assignee == "" {
		return true, nil
	}

	guard := []string{argIfAssignee, shown.Assignee, argIfStatus, shown.Status}

	return s.updateIf(ctx, rowID, guard, argStatus, statusOpen, argAssignee, "")
}

// updateIf runs bd update with the guard arguments. It reports false when the guard did not match: bd then writes nothing and exits 13.
func (s *BeadsStore) updateIf(ctx context.Context, rowID string, guard []string, args ...string) (bool, error) {
	done := s.run.Run(ctx, slices.Concat([]string{argUpdate, rowID}, guard, args))
	if done.ExitCode == guardMismatch {
		return false, nil
	}

	if done.ExitCode != 0 {
		return false, refuse("store: cannot update the row %s (the guards need bd 1.3.2 or newer): %s", rowID, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes))
	}

	return true, nil
}

func (s *BeadsStore) createRow(ctx context.Context, rowID string, row model.ClaimRow) (*bdRow, error) {
	title := s.itemTitle(ctx, row.Item)
	rowTitle := strings.TrimRight(fmt.Sprintf("%s%s-%s %s", lockPrefix, model.ItemKind(row.Item), row.Item, firstRunes(title, maxTitleRunes)), " \t\r\n")

	s.run.Run(ctx, []string{argCreate, "--id", rowID, argTitle, rowTitle, argType, "task", "--priority", "3", "--silent", argDescription, claimNote(row)})

	shown, err := s.showRow(ctx, rowID)
	if err == nil && shown == nil {
		err = refuse("store: could not create the claim row %s", rowID)
	}

	return shown, err
}

// itemTitle returns the title of a beads item. A forge number has no beads title, and the row title stays bare.
func (s *BeadsStore) itemTitle(ctx context.Context, item string) string {
	if model.ItemKind(item) != model.KindBeads {
		return ""
	}

	shown, err := s.showRow(ctx, item)
	if err != nil || shown == nil {
		return ""
	}

	return derefText(shown.Title)
}

func claimNote(row model.ClaimRow) string {
	return fmt.Sprintf("Claim for %s. Held while in_progress; the assignee holds it. %s", row.Item, model.LeaseText(row.Lease))
}

// StoreLease writes the lease into the row description, only while the row is still the in_progress take of row.
func (s *BeadsStore) StoreLease(ctx context.Context, row model.ClaimRow) error {
	if err := s.requireGuards(ctx); err != nil {
		return err
	}

	matched, err := s.updateIf(ctx, s.rowID(row.Item), guardOf(row), argDescription, claimNote(row))
	if err != nil || matched {
		return err
	}

	return fmt.Errorf("store: the claim row of #%s is not the take of %s any more: %w", row.Item, row.Holder, model.ErrRowMoved)
}

// Free gives the row back as open and clears its holder, only while the row is still the take of row. It runs bd unclaim,
// which reopens the row so the next take can claim it. A mismatch writes nothing. The row is then free, or held by another take, which is ErrRowMoved.
func (s *BeadsStore) Free(ctx context.Context, row model.ClaimRow) error {
	if err := s.requireGuards(ctx); err != nil {
		return err
	}

	if released, err := s.release(ctx, row); err != nil || released {
		return err
	}

	state, err := s.Row(ctx, row.Item)
	if err != nil || !state.Held {
		return err
	}

	return fmt.Errorf("store: the claim row of #%s is held by %s now: %w", row.Item, state.Holder, model.ErrRowMoved)
}

// release runs bd unclaim for the take. It reports false when the row was not released: it is held by another take, or it holds nothing.
func (s *BeadsStore) release(ctx context.Context, row model.ClaimRow) (bool, error) {
	rowID := s.rowID(row.Item)

	done := s.run.Run(ctx, []string{argUnclaim, rowID, argIfAssignee, assigneeOf(row)})
	if done.ExitCode == 0 {
		return true, nil
	}

	if refusedWith(done, textMismatch, textClosed) {
		return false, nil
	}

	return false, s.missingIsFree(ctx, rowID, refuse("store: cannot release the row %s: %s", rowID, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes)))
}

// missingIsFree turns a failed update of a row that does not exist into success: nothing is held.
func (s *BeadsStore) missingIsFree(ctx context.Context, rowID string, failed error) error {
	shown, err := s.showRow(ctx, rowID)
	if err != nil {
		return err
	}

	if shown == nil {
		return nil
	}

	return failed
}
