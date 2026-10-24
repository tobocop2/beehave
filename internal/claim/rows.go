package claim

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const (
	bdPage          = 5000
	statusProgress  = "in_progress"
	statusClosed    = "closed"
	statusOpen      = "open"
	agentMiddle     = "-agent-"
	rowsHeldLabel   = "the held rows"
	rowsAgentsLabel = "the agent beads"
)

type row struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Title        string `json:"title"`
	Assignee     string `json:"assignee"`
	Description  string `json:"description"`
	LastActivity string `json:"last_activity"`
}

type heldRow struct {
	item     string
	rowID    string
	assignee string
	lease    int64
}

// list reads the rows of bd list. A bad or full page is no answer.
func (c *Claims) list(ctx context.Context, what string, args ...string) ([]row, error) {
	done := c.bd.Run(ctx, append(append([]string{"list"}, args...), "--limit", strconv.Itoa(bdPage), "--json"))

	rows, err := model.Decode[[]row]("bd list", done.Stdout)
	if err != nil {
		return nil, refuseBecause(err, "claim: cannot read %s from `bd list` (rc=%d); refusing to answer. %s",
			what, done.ExitCode, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes))
	}

	if len(rows) >= bdPage {
		return nil, refuse("claim: %s returned %d rows, the list may be cut off; refusing to answer", what, bdPage)
	}

	return rows, nil
}

func firstRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		runes = runes[:limit]
	}

	return string(runes)
}

// heldRows returns every in_progress claim row.
func (c *Claims) heldRows(ctx context.Context) ([]heldRow, error) {
	rows, err := c.list(ctx, rowsHeldLabel, "--status="+statusProgress)
	if err != nil {
		return nil, err
	}

	var held []heldRow

	for _, r := range rows {
		item, ok := claimedItem(r.Title)
		if ok && r.ID != "" {
			held = append(held, heldRow{item: item, rowID: r.ID, assignee: r.Assignee, lease: storedLease(r.Description)})
		}
	}

	return held, nil
}

// claimedItem returns the item a claim row title names, or false when the title is not a claim row of a known kind.
func claimedItem(title string) (string, bool) {
	if !strings.HasPrefix(title, forgeTitlePrefix) && !strings.HasPrefix(title, beadsTitlePrefix) {
		return "", false
	}

	word, _, _ := strings.Cut(strings.TrimPrefix(title, rowTitlePrefix), " ")
	kind, item, _ := strings.Cut(word, string(idSeparator))

	return item, KindOf(item) == kind
}

// holders returns the sorted owners that hold the item.
func (c *Claims) holders(ctx context.Context, item string) ([]string, error) {
	rows, err := c.heldRows(ctx)
	if err != nil {
		return nil, err
	}

	return holdersOf(rows, item), nil
}

func holdersOf(rows []heldRow, item string) []string {
	var names []string

	for _, r := range rows {
		if r.item == item {
			names = append(names, r.assignee)
		}
	}

	return uniqueSorted(names)
}

func uniqueSorted(names []string) []string {
	seen := map[string]bool{}
	unique := []string{}

	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}

	slices.Sort(unique)

	return unique
}

// leasesOf returns the lease each holder stored in its claim row for the item.
func leasesOf(rows []heldRow, item string) map[string]int64 {
	leases := map[string]int64{}

	for _, r := range rows {
		if r.item == item && r.lease != leaseNone {
			leases[r.assignee] = r.lease
		}
	}

	return leases
}

// lastSeen returns the seconds since the last heartbeat of every owner that has an agent bead.
func (c *Claims) lastSeen(ctx context.Context) (map[string]float64, error) {
	rows, err := c.list(ctx, rowsAgentsLabel, "--type", "agent")
	if err != nil {
		return nil, err
	}

	prefix := c.prefix + agentMiddle
	now := c.clock()
	seen := map[string]float64{}

	for _, r := range rows {
		stamp, err := time.Parse(time.RFC3339Nano, r.LastActivity)
		if strings.HasPrefix(r.ID, prefix) && err == nil {
			seen[strings.TrimPrefix(r.ID, prefix)] = now.Sub(stamp).Seconds()
		}
	}

	return seen, nil
}

// expired reports whether the holder was last seen longer ago than its lease. An unreadable lease never expires.
func (c *Claims) expired(holder string, seen map[string]float64, leases map[string]int64) bool {
	lease, found := leases[holder]
	if !found {
		lease = c.lease
	}

	return lease != leaseUnreadable && seen[holder] > float64(lease)
}

// show returns the row with exactly this id, or nil when it does not exist.
func (c *Claims) show(ctx context.Context, rowID string) (*row, error) {
	done := c.bd.Run(ctx, []string{"show", rowID, "--json"})
	if done.ExitCode != 0 {
		return nil, nil //nolint:nilnil // a missing row is an answer, not an error
	}

	shown, err := parseShown(done.Stdout)
	if err != nil {
		return nil, refuseBecause(err, "claim: cannot parse `bd show %s` (%v); refusing to answer", rowID, err)
	}

	// bd show resolves a partial id by prefix, so a short id returns the longer row.
	if len(shown) == 0 || shown[0].ID != rowID {
		return nil, nil //nolint:nilnil // a missing row is an answer, not an error
	}

	return &shown[0], nil
}

// parseShown reads the answer of bd show, which is one object or a list of rows.
func parseShown(text string) ([]row, error) {
	if strings.HasPrefix(strings.TrimSpace(text), "[") {
		return model.Decode[[]row]("bd show", text)
	}

	one, err := model.Decode[row]("bd show", text)

	return []row{one}, err
}

func (c *Claims) showOrEmpty(ctx context.Context, rowID string) (row, error) {
	shown, err := c.show(ctx, rowID)
	if err != nil || shown == nil {
		return row{}, err
	}

	return *shown, nil
}

// Who returns one line: who holds the item, if anyone.
func (c *Claims) Who(ctx context.Context, item string) (string, error) {
	if _, err := ItemOf(item); err != nil {
		return "", err
	}

	rows, err := c.heldRows(ctx)
	if err != nil {
		return "", err
	}

	names := holdersOf(rows, item)

	switch len(names) {
	case 0:
		return fmt.Sprintf("#%s unowned", item), nil
	case 1:
		return c.whoHolds(ctx, item, names[0], leasesOf(rows, item))
	default:
		return fmt.Sprintf("#%s held by %s (MORE THAN ONE HOLDER: resolve by hand)", item, strings.Join(names, ", ")), nil
	}
}

func (c *Claims) whoHolds(ctx context.Context, item, holder string, leases map[string]int64) (string, error) {
	seen, err := c.lastSeen(ctx)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("#%s held by %s", item, c.label(holder, seen, leases)), nil
}

func (c *Claims) label(holder string, seen map[string]float64, leases map[string]int64) string {
	if leases[holder] == leaseUnreadable {
		return holder + " (lease unreadable: clear the row by hand)"
	}

	if !c.expired(holder, seen, leases) {
		return holder
	}

	return fmt.Sprintf("%s (LEASE EXPIRED: last seen %s ago; `take` reclaims it)", holder, Age(seen[holder]))
}
