package claim

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const (
	attempts       = 4
	argAssignee    = "--assignee"
	argUpdate      = "update"
	argStatus      = "--status"
	argDescription = "--description"
)

// Runner runs one outside command with the arguments and returns what it printed.
type Runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// Options holds what New needs. BD runs bd in the beads database and GH runs gh.
type Options struct {
	BD     Runner
	GH     Runner
	Prefix string
	Repo   string
	Lease  time.Duration
	Settle time.Duration
	Clock  func() time.Time
	Sleep  func(time.Duration)
	Out    io.Writer
	Warn   io.Writer
}

// Claims is the claim rows of one beads database. Prefix is the id prefix of that database and Repo is owner/name of the forge.
type Claims struct {
	bd     Runner
	gh     Runner
	prefix string
	repo   string
	lease  int64
	settle time.Duration
	clock  func() time.Time
	sleep  func(time.Duration)
	out    io.Writer
	warn   io.Writer
}

// New returns the claims of one beads database. It refuses a lease under one second and every missing dependency.
// A caller that has no setting takes model.DefaultClaimLease and model.DefaultClaimSettle.
func New(options Options) (*Claims, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}

	return &Claims{
		bd: options.BD, gh: options.GH, prefix: options.Prefix, repo: options.Repo,
		lease: int64(options.Lease / time.Second), settle: options.Settle,
		clock: options.Clock, sleep: options.Sleep, out: options.Out, warn: options.Warn,
	}, nil
}

func (o Options) validate() error {
	missing := map[string]bool{
		"BD runner": o.BD == nil, "GH runner": o.GH == nil, "clock": o.Clock == nil, "sleep function": o.Sleep == nil,
		"output writer": o.Out == nil, "warning writer": o.Warn == nil, "id prefix": o.Prefix == "",
	}

	for _, name := range slices.Sorted(maps.Keys(missing)) {
		if missing[name] {
			return refuse("claim: the %s is required", name)
		}
	}

	if o.Lease < model.MinClaimLease {
		return refuse("claim: the lease must be at least %s, got %s; a shorter lease lets a second run take a live hold", model.MinClaimLease, o.Lease)
	}

	if o.Settle < 0 {
		return refuse("claim: the settle pause must not be negative, got %s", o.Settle)
	}

	return nil
}

func say(w io.Writer, line string) {
	// A failed write to the output has no recovery.
	_, _ = fmt.Fprintln(w, line)
}

func (c *Claims) agentID(holder string) string {
	return c.prefix + agentMiddle + holder
}

// Beat stamps the heartbeat of the owner's agent bead and creates the bead first. It returns a problem text, or "" when all went well.
func (c *Claims) Beat(ctx context.Context, owner string) (string, error) {
	holder, err := OwnerOf(owner)
	if err != nil {
		return "", err
	}

	agent := c.agentID(holder)

	if problem, err := c.ensureAgent(ctx, agent, holder); problem != "" || err != nil {
		return problem, err
	}

	done := c.bd.Run(ctx, []string{"agent", "heartbeat", agent})
	if done.ExitCode == 0 {
		return "", nil
	}

	return fmt.Sprintf("heartbeat for %s failed: %s", agent, firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes)), nil
}

func (c *Claims) ensureAgent(ctx context.Context, agent, holder string) (string, error) {
	shown, err := c.show(ctx, agent)
	if err != nil || shown != nil {
		return "", err
	}

	c.bd.Run(ctx, []string{
		"create", "--id", agent, "--title", "lock owner " + holder, "--type", "agent", "--labels", "gt:agent",
		"--silent", argDescription, "Liveness for the claim: last_activity is this owner's last heartbeat.",
	})

	if shown, err = c.show(ctx, agent); shown == nil {
		return "could not create the agent bead " + agent, err
	}

	return "", err
}

// checked validates the owner token and the item and returns the holder name.
func checked(owner, item string) (string, error) {
	me, err := OwnerOf(owner)
	if err != nil {
		return "", err
	}

	_, err = ItemOf(item)

	return me, err
}

// Release gives the item back. Another owner's hold is a refusal with code 3.
func (c *Claims) Release(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	rows, err := c.heldRows(ctx)
	if err != nil {
		return err
	}

	mine, others := splitRows(rows, item, me)
	if len(mine) == 0 {
		return c.releaseNothing(item, me, uniqueSorted(others))
	}

	return c.closeRows(ctx, item, mine)
}

// splitRows returns the row ids the owner holds for the item and the names of the other holders.
func splitRows(rows []heldRow, item, me string) ([]string, []string) {
	var mine, others []string

	for _, r := range rows {
		switch {
		case r.item != item:
		case r.assignee == me:
			mine = append(mine, r.rowID)
		default:
			others = append(others, r.assignee)
		}
	}

	return mine, others
}

func (c *Claims) releaseNothing(item, me string, others []string) error {
	if len(others) > 0 {
		return held("REFUSED: #%s is held by %s, not %s", item, strings.Join(others, ", "), me)
	}

	say(c.out, fmt.Sprintf("#%s was not locked", item))

	return nil
}

func (c *Claims) closeRows(ctx context.Context, item string, rowIDs []string) error {
	for _, rowID := range rowIDs {
		c.bd.Run(ctx, []string{argUpdate, rowID, argStatus, statusClosed, argAssignee, ""})

		shown, err := c.show(ctx, rowID)
		if err != nil {
			return err
		}

		if shown == nil {
			return refuse("claim: release of #%s did not take effect on %s: the row is gone", item, rowID)
		}

		if shown.Status != statusClosed || shown.Assignee != "" {
			return refuse("claim: release of #%s did not take effect on %s: status %q, assignee %q", item, rowID, shown.Status, shown.Assignee)
		}
	}

	say(c.out, "released #"+item)

	return nil
}

// Take claims the item for the owner. Another live holder is a refusal with code 3.
func (c *Claims) Take(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	if err = c.beatOrWarn(ctx, owner); err != nil {
		return err
	}

	if done, err := c.prepare(ctx, me, item); done || err != nil {
		return err
	}

	rowID := fmt.Sprintf("%s-lock-%s-%s", c.prefix, KindOf(item), item)
	if err = c.ensureRow(ctx, rowID, item); err != nil {
		return err
	}

	return c.claimRow(ctx, me, item, rowID)
}

// prepare refuses a live holder and reopens expired rows. It reports true when the owner holds the item already.
func (c *Claims) prepare(ctx context.Context, me, item string) (bool, error) {
	holders, err := c.holders(ctx, item)
	if err != nil {
		return false, err
	}

	if len(holders) == 1 && holders[0] == me {
		say(c.out, "#"+item+" already held by you")

		return true, nil
	}

	return false, c.clearExpired(ctx, me, item, holders)
}

func (c *Claims) beatOrWarn(ctx context.Context, owner string) error {
	problem, err := c.Beat(ctx, owner)
	if problem != "" {
		say(c.warn, fmt.Sprintf("WARNING: %s. Other runs will read this owner's claims as expired after %s.", problem, Age(float64(c.lease))))
	}

	return err
}

// EnsureFree refuses when another owner holds the item. It writes nothing.
func (c *Claims) EnsureFree(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	holders, err := c.holders(ctx, item)
	if err != nil {
		return err
	}

	_, _, err = c.refuseLive(ctx, me, item, holders)

	return err
}

// refuseLive refuses a live holder. It returns the other holders and when each was last seen.
func (c *Claims) refuseLive(ctx context.Context, me, item string, holders []string) ([]string, map[string]float64, error) {
	others := slices.DeleteFunc(slices.Clone(holders), func(who string) bool { return who == me })
	if len(others) == 0 {
		return nil, nil, nil
	}

	seen, rows, err := c.liveness(ctx)
	if err != nil {
		return nil, nil, err
	}

	leases := leasesOf(rows, item)
	live := slices.DeleteFunc(slices.Clone(others), func(who string) bool { return c.expired(who, seen, leases) })

	if len(live) > 0 {
		return nil, nil, held("REFUSED: #%s is held by %s. Do not touch it.", item, strings.Join(live, ", "))
	}

	return others, seen, nil
}

// liveness reads when each owner was last seen and every held row.
func (c *Claims) liveness(ctx context.Context) (map[string]float64, []heldRow, error) {
	seen, err := c.lastSeen(ctx)
	if err != nil {
		return nil, nil, err
	}

	rows, err := c.heldRows(ctx)

	return seen, rows, err
}

// clearExpired refuses a live holder and reopens the rows of holders whose lease ran out.
func (c *Claims) clearExpired(ctx context.Context, me, item string, holders []string) error {
	others, seen, err := c.refuseLive(ctx, me, item, holders)
	if err != nil || len(others) == 0 {
		return err
	}

	rows, err := c.heldRows(ctx)
	if err != nil {
		return err
	}

	leases := leasesOf(rows, item)

	for _, r := range rows {
		if err = c.reopenIfExpired(ctx, r, item, others, seen, leases); err != nil {
			return err
		}
	}

	return nil
}

func (c *Claims) reopenIfExpired(ctx context.Context, r heldRow, item string, others []string, seen map[string]float64, leases map[string]int64) error {
	if r.item != item || !slices.Contains(others, r.assignee) {
		return nil
	}

	return c.reopen(ctx, r, item, seen, leases)
}

// reopen clears the row only when the expired holder still holds it: a row claimed since the read is not expired.
func (c *Claims) reopen(ctx context.Context, r heldRow, item string, seen map[string]float64, leases map[string]int64) error {
	shown, err := c.showOrEmpty(ctx, r.rowID)
	if err != nil || shown.Status != statusProgress || shown.Assignee != r.assignee {
		return err
	}

	c.bd.Run(ctx, []string{argUpdate, r.rowID, argStatus, statusOpen, argAssignee, ""})

	lease, found := leases[r.assignee]
	if !found {
		lease = c.lease
	}

	say(c.out, fmt.Sprintf("reclaiming #%s from %s: last seen %s ago, lease %s", item, r.assignee, Age(seen[r.assignee]), Age(float64(lease))))

	return nil
}

func (c *Claims) itemTitle(ctx context.Context, item string) (string, error) {
	if KindOf(item) == kindForge {
		done := c.gh.Run(ctx, []string{"issue", "view", item, "--repo", c.repo, "--json", "title", "--jq", ".title"})
		if done.ExitCode != 0 {
			return "", nil
		}

		return strings.TrimSpace(done.Stdout), nil
	}

	shown, err := c.showOrEmpty(ctx, item)

	return shown.Title, err
}

func (c *Claims) ensureRow(ctx context.Context, rowID, item string) error {
	shown, err := c.show(ctx, rowID)
	if err != nil {
		return err
	}

	if shown == nil {
		if shown, err = c.createRow(ctx, rowID, item); err != nil {
			return err
		}
	}

	if shown.Status != statusProgress && shown.Assignee != "" {
		// Not in_progress, so it holds nothing, but a stale assignee refuses every claim.
		c.bd.Run(ctx, []string{argUpdate, rowID, argAssignee, ""})
	}

	return nil
}

func (c *Claims) createRow(ctx context.Context, rowID, item string) (*row, error) {
	title, err := c.itemTitle(ctx, item)
	if err != nil {
		return nil, err
	}

	rowTitle := strings.TrimRight(fmt.Sprintf("%s%s-%s %s", rowTitlePrefix, KindOf(item), item, firstRunes(title, maxTitleRunes)), " \t\r\n")

	c.bd.Run(ctx, []string{
		"create", "--id", rowID, "--title", rowTitle, "--type", "task", "--priority", "3",
		"--silent", argDescription, c.description(item),
	})

	shown, err := c.show(ctx, rowID)
	if err == nil && shown == nil {
		err = refuse("claim: could not create the claim row %s", rowID)
	}

	return shown, err
}

func (c *Claims) description(item string) string {
	return fmt.Sprintf("Claim for %s. Held while in_progress; the assignee holds it. lease=%s", item, strconv.FormatInt(c.lease, 10))
}

func (c *Claims) claimRow(ctx context.Context, me, item, rowID string) error {
	for range attempts {
		c.bd.Run(ctx, []string{argUpdate, rowID, "--claim", argAssignee, me})

		settled, shown, err := c.settleRow(ctx, me, rowID)
		if err != nil {
			return err
		}

		if settled {
			return c.finishTake(ctx, me, item, rowID)
		}

		if shown.Assignee != "" && shown.Assignee != me {
			return held("REFUSED: #%s was claimed by %s in the same window.", item, shown.Assignee)
		}
		// Unassigned: a concurrent create reset the row between the claim and the read.
	}

	return refuse("claim: could not settle ownership of #%s after %d attempts; check `obee claim who %s`", item, attempts, item)
}

// settleRow reads the row back, waits, and reads it again. It reports whether the row is still ours.
func (c *Claims) settleRow(ctx context.Context, me, rowID string) (bool, row, error) {
	shown, err := c.showOrEmpty(ctx, rowID)
	if err != nil || !isMine(shown, me) {
		return false, shown, err
	}

	c.sleep(c.settle)

	shown, err = c.showOrEmpty(ctx, rowID)

	return err == nil && isMine(shown, me), shown, err
}

func isMine(r row, me string) bool {
	return r.Status == statusProgress && r.Assignee == me
}

// finishTake gives the row back when a second holder appeared after ours settled, and stores the lease otherwise.
func (c *Claims) finishTake(ctx context.Context, me, item, rowID string) error {
	holders, err := c.holders(ctx, item)
	if err != nil {
		return err
	}

	if late := slices.DeleteFunc(slices.Clone(holders), func(who string) bool { return who == me }); len(late) > 0 {
		c.bd.Run(ctx, []string{argUpdate, rowID, argStatus, statusClosed, argAssignee, ""})

		return held("REFUSED: #%s is also held by %s; yielded.", item, strings.Join(late, ", "))
	}

	c.bd.Run(ctx, []string{argUpdate, rowID, argDescription, c.description(item)})
	say(c.out, fmt.Sprintf("TAKEN #%s by %s", item, me))

	return nil
}
