package remedy_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/model"
)

var takeNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type fields = map[string]any

func hoursAgo(hours float64) string {
	return takeNow.Add(-time.Duration(hours * float64(time.Hour))).Format("2006-01-02T15:04:05Z")
}

func person(name string) fields { return fields{"login": name} }

// takeWorld is a fake forge for one takeover: one pull request, its review comments and the notices posted on it.
type takeWorld struct {
	forgeWorld
	pr       fields
	viewErr  error
	rowsErr  error
	postErr  error
	posted   []string
	numberAt []int
}

// pullOf is the pull request of somebody else: the last human comment is `age` hours old, we asked for changes, and a notice of ours is `notice` hours old (0 for none).
func pullOf(author string, age, notice float64, replies ...fields) fields {
	comments := []fields{{"author": person("them"), "createdAt": hoursAgo(age), "body": "work in progress"}}
	if notice != 0 {
		comments = append(comments, fields{"author": person("me"), "createdAt": hoursAgo(notice), "body": "Taking over this pull request: it is idle"})
	}

	comments = append(comments, replies...)

	return fields{
		"number": 7, "state": "OPEN", "author": person(author), "assignees": []fields{}, "headRefOid": "aaa",
		"commits": []fields{}, "comments": comments,
		"reviews": []fields{{"author": person("me"), "state": "CHANGES_REQUESTED", "submittedAt": "2020-01-01T00:00:00Z", "body": "x"}},
	}
}

func (w *takeWorld) PRView(_ context.Context, number int, _ ...string) (model.PullRequest, error) {
	w.numberAt = append(w.numberAt, number)

	if w.viewErr != nil {
		return model.PullRequest{}, w.viewErr
	}

	data, err := json.Marshal(w.pr)
	if err != nil {
		return model.PullRequest{}, err
	}

	return model.Decode[model.PullRequest]("pr view", string(data))
}

func (w *takeWorld) ReviewComments(context.Context, int) ([]model.Comment, error) {
	return nil, w.rowsErr
}

func (w *takeWorld) PostComment(_ context.Context, _ int, body string) error {
	w.posted = append(w.posted, body)

	return w.postErr
}

// claimStore is a fake claim store. heldBy names the owner that holds each item.
type claimStore struct {
	heldBy    map[string]string
	checkedBy []string
	repos     []string

	checkedItem []string
}

func (c *claimStore) refuse(owner, item string) error {
	if other, found := c.heldBy[item]; found && other != owner {
		return &claim.Error{Code: claim.CodeHeld, Message: "REFUSED: #" + item + " is held by " + other + ". Do not touch it."}
	}

	return nil
}

func (c *claimStore) EnsureFree(_ context.Context, owner, item string) error {
	c.checkedBy = append(c.checkedBy, owner)
	c.checkedItem = append(c.checkedItem, item)

	return c.refuse(owner, item)
}

func newClaimStore() *claimStore { return &claimStore{heldBy: map[string]string{}} }

func takeDeps(world *takeWorld) remedy.Deps {
	store := newClaimStore()

	return remedy.Deps{
		Git:    origin("git@github.com:owner/name.git", nothing),
		Dir:    ".",
		Forge:  func(string) (remedy.Forge, error) { return world, nil },
		Now:    func() time.Time { return takeNow },
		Claims: func(string) (remedy.Claims, error) { return store, nil },
	}
}

func claimsOf(store *claimStore) func(string) (remedy.Claims, error) {
	return func(repo string) (remedy.Claims, error) {
		store.repos = append(store.repos, repo)

		return store, nil
	}
}

func TestTake_TakeReadsTheClaimAndWritesNoneAndSaysTheRunHoldsIt(t *testing.T) {
	t.Parallel()

	store := newClaimStore()
	world := &takeWorld{pr: pullOf("them", 9, 5)}
	deps := takeDeps(world)
	deps.Claims = claimsOf(store)

	code, out, _ := run(t, deps, takeArgs("--take")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "#7 may be taken now")
	assert.Contains(t, out, "The run of the item holds the claim.")
	assert.NotContains(t, out, "TAKEN")
	assert.Empty(t, store.heldBy)
	assert.Equal(t, []string{"remedy"}, store.checkedBy)
	assert.Equal(t, []string{"owner/name"}, store.repos)
}

func TestTake_ClaimStoreThatDoesNotOpenNeverHidesARefusalOfAGuard(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"--apply", "--take"} {
		world := &takeWorld{pr: pullOf("me", 9, 5)}
		deps := takeDeps(world)
		deps.Claims = func(string) (remedy.Claims, error) { return nil, errDown }

		code, out, errOut := run(t, deps, takeArgs(mode)...)

		assert.Equal(t, remedy.ExitRefused, code, mode)
		assert.Contains(t, out, "REFUSE: #7 is yours", mode)
		assert.NotContains(t, errOut, "claim store", mode)
	}
}

func TestTake_HeadThatMovedIsARefusalOfExitCodeTwoAndTheMatchingHeadPasses(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, takeDeps(&takeWorld{pr: pullOf("them", 9, 0)}), takeArgs("--head", "bbb")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "The author pushed. Start from the new head.")

	code, out, _ = run(t, takeDeps(&takeWorld{pr: pullOf("them", 9, 0)}), takeArgs("--head", "aaa")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "WOULD POST")
}

func TestTake_ClaimThatAnotherOwnerHoldsRefusesTheTakeAndPostsNothing(t *testing.T) {
	t.Parallel()

	for mode, pull := range map[string]fields{"--apply": pullOf("them", 9, 0), "--take": pullOf("them", 9, 5)} {
		store := newClaimStore()
		store.heldBy["12"] = "session-beehaveabc"
		world := &takeWorld{pr: pull}
		deps := takeDeps(world)
		deps.Claims = claimsOf(store)

		code, out, errOut := run(t, deps, takeArgs(mode)...)

		assert.Equal(t, remedy.ExitRefused, code, mode)
		assert.NotContains(t, out, "TAKEN", mode)
		assert.NotContains(t, out, "notice posted", mode)
		assert.Contains(t, errOut, "held by session-beehaveabc", mode)
		assert.Empty(t, world.posted, mode)
	}
}

func TestTake_ClaimStoreThatDoesNotOpenIsARefusalInPlainWords(t *testing.T) {
	t.Parallel()

	for mode, pull := range map[string]fields{"--apply": pullOf("them", 9, 0), "--take": pullOf("them", 9, 5)} {
		world := &takeWorld{pr: pull}
		deps := takeDeps(world)
		deps.Claims = func(string) (remedy.Claims, error) { return nil, errDown }

		code, out, errOut := run(t, deps, takeArgs(mode)...)

		assert.Equal(t, remedy.ExitRefused, code, mode)
		assert.Empty(t, out, mode)
		assert.Contains(t, errOut, "remedy take: cannot open the claim store", mode)
		assert.Empty(t, world.posted, mode)
	}
}

func TestTake_DryReadOpensNoClaimStore(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}
	deps := takeDeps(world)
	deps.Claims = func(string) (remedy.Claims, error) { return nil, errDown }

	code, _, _ := run(t, deps, takeArgs()...)

	assert.Equal(t, remedy.ExitPass, code)
}

func takeArgs(extra ...string) []string {
	return append([]string{"take", "7", "--me", "me", "--idle", "1", "--grace", "1", "--item", "12"}, extra...)
}

func TestTake_ClaimReadUsesTheItemOfTheFlagAndNotThePullRequest(t *testing.T) {
	t.Parallel()

	store := newClaimStore()
	store.heldBy["7"] = "session-beehaveabc"
	deps := takeDeps(&takeWorld{pr: pullOf("them", 9, 0)})
	deps.Claims = claimsOf(store)

	code, _, _ := run(t, deps, takeArgs("--apply")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, []string{"12"}, store.checkedItem)
}

func TestTake_ApplyAndTakeWithNoItemAreRefusedInPlainWords(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"--apply", "--take"} {
		world := &takeWorld{pr: pullOf("them", 9, 5)}
		store := newClaimStore()
		deps := takeDeps(world)
		deps.Claims = claimsOf(store)

		code, _, errOut := run(t, deps, "take", "7", "--me", "me", "--idle", "1", "--grace", "1", mode)

		assert.Equal(t, remedy.ExitRefused, code, mode)
		assert.Contains(t, errOut, "needs --item", mode)
		assert.Empty(t, store.checkedItem, mode)
		assert.Empty(t, world.posted, mode)
	}
}

func TestTake_DryReadNeedsNoItem(t *testing.T) {
	t.Parallel()

	code, _, _ := run(t, takeDeps(&takeWorld{pr: pullOf("them", 9, 0)}), "take", "7", "--me", "me", "--idle", "1", "--grace", "1")

	assert.Equal(t, remedy.ExitPass, code)
}

func TestTake_ClaimOwnerOfTheReadIsTheSameForEveryLogin(t *testing.T) {
	t.Parallel()

	for _, login := range []string{"my-login", "a_b", "name_acme"} {
		store := newClaimStore()
		pull := pullOf("them", 9, 0)
		pull["reviews"] = []fields{{"author": person(login), "state": "CHANGES_REQUESTED", "submittedAt": "2020-01-01T00:00:00Z", "body": "x"}}
		deps := takeDeps(&takeWorld{pr: pull})
		deps.Claims = claimsOf(store)

		code, _, _ := run(t, deps, "take", "7", "--me", login, "--idle", "1", "--grace", "1", "--item", "12", "--apply")

		assert.Equal(t, remedy.ExitPass, code, login)
		assert.Equal(t, []string{"remedy"}, store.checkedBy, login)
	}
}

func TestTake_DryReadSaysWhatItWouldPostAndWritesNothing(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}

	code, out, _ := run(t, takeDeps(world), takeArgs()...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "WOULD POST: Taking over this pull request")
	assert.Empty(t, world.posted)
	assert.Equal(t, []int{7}, world.numberAt[:1])
}

func TestTake_ApplyPostsTheNoticeAndPasses(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}

	code, out, _ := run(t, takeDeps(world), takeArgs("--apply")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "notice posted on #7 (basis: idle)")
	require.Len(t, world.posted, 1)
	assert.Contains(t, world.posted[0], "9.00h")
}

func TestTake_TakeAfterTheGracePeriodPasses(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 5)}

	code, out, _ := run(t, takeDeps(world), takeArgs("--take")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "#7 may be taken now (basis: idle)")
	assert.Empty(t, world.posted)
}

func TestTake_RefusalOfARuleIsExitCodeTwoWithTheLinesOnTheOutput(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}

	code, out, _ := run(t, takeDeps(world), takeArgs("--take")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "REFUSE: no notice has been posted on #7")
}

func TestTake_PullRequestOfOursIsAGuardRefusalAndExitCodeTwo(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("me", 9, 0)}

	code, out, _ := run(t, takeDeps(world), takeArgs("--apply")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "REFUSE: #7 is yours")
	assert.Empty(t, world.posted)
}

func TestTake_IdleAndGraceFlagsAreHours(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}
	args := []string{"take", "7", "--me", "me", "--idle", "12", "--grace", "3"}

	code, out, _ := run(t, takeDeps(world), args...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "inside the 12h threshold")

	world = &takeWorld{pr: pullOf("them", 9, 0)}
	args = []string{"take", "7", "--me", "me", "--idle", "1", "--grace", "3", "--item", "12", "--apply"}

	run(t, takeDeps(world), args...)

	require.Len(t, world.posted, 1)
	assert.Contains(t, world.posted[0], "3h")
}

func TestTake_RepliesThatWereNotReadRefuseAndReplyReadLetsTheTakeGoOn(t *testing.T) {
	t.Parallel()

	pull := func() fields {
		return pullOf("them", 9, 5, fields{"author": person("them"), "createdAt": hoursAgo(2), "body": "please wait"})
	}

	code, out, _ := run(t, takeDeps(&takeWorld{pr: pull()}), takeArgs("--take")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "re-running: --take --reply-read")

	code, out, _ = run(t, takeDeps(&takeWorld{pr: pull()}), takeArgs("--take", "--reply-read")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "#7 may be taken now")
}

func TestTake_ClockIsTheClockOfTheDeps(t *testing.T) {
	t.Parallel()

	world := &takeWorld{pr: pullOf("them", 9, 0)}
	deps := takeDeps(world)
	deps.Now = func() time.Time { return takeNow.Add(10 * time.Hour) }

	_, out, _ := run(t, deps, takeArgs()...)

	assert.Contains(t, out, "19.00h ago")
}

func TestTake_ReadThatFailsIsExitCodeTwoWithTheCauseOnTheErrorOutput(t *testing.T) {
	t.Parallel()

	world := &takeWorld{viewErr: errDown}

	code, out, errOut := run(t, takeDeps(world), takeArgs()...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy take: ")
	assert.Contains(t, errOut, "cannot read #7")
}

func TestTake_ArgumentsThatAreMissingOrUnknownAreARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no number":        {"take", "--me", "me", "--idle", "1", "--grace", "1"},
		"bad number":       {"take", "x", "--me", "me", "--idle", "1", "--grace", "1"},
		"zero number":      {"take", "0", "--me", "me", "--idle", "1", "--grace", "1"},
		"no login":         {"take", "7", "--idle", "1", "--grace", "1"},
		"no idle":          {"take", "7", "--me", "me", "--grace", "1"},
		"no grace":         {"take", "7", "--me", "me", "--idle", "1"},
		"negative idle":    {"take", "7", "--me", "me", "--idle", "-2", "--grace", "1"},
		"apply and take":   takeArgs("--apply", "--take"),
		"extra argument":   takeArgs("8"),
		"unknown flag":     takeArgs("--branch", "aaa"),
		"head with no sha": takeArgs("--head"),
		"bad hours value":  {"take", "7", "--me", "me", "--idle", "soon", "--grace", "1"},
	} {
		world := &takeWorld{pr: pullOf("them", 9, 0)}

		code, out, errOut := run(t, takeDeps(world), args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.NotEmpty(t, errOut, name)
		assert.Empty(t, world.numberAt, name)
	}
}

func TestTake_UsageNamesTheCommand(t *testing.T) {
	t.Parallel()

	_, _, errOut := run(t, takeDeps(&takeWorld{}), "take")

	assert.Contains(t, errOut, "usage: remedy take <pr> --me <login> --idle <hours> --grace <hours>")
}

func TestTake_OriginThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	deps := takeDeps(&takeWorld{})
	deps.Git = git(func([]string) model.CommandResult { return model.CommandResult{ExitCode: 1} })

	code, _, errOut := run(t, deps, takeArgs()...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "origin remote")
}

func TestTake_ForgeThatCannotOpenIsARefusal(t *testing.T) {
	t.Parallel()

	deps := takeDeps(&takeWorld{})
	deps.Forge = func(string) (remedy.Forge, error) { return nil, errDown }

	code, _, errOut := run(t, deps, takeArgs()...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "down")
}
