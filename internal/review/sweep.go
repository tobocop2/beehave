package review

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tobocop2/obee/internal/model"
)

// SweepHeader is the first line of the sweep.
const SweepHeader = "PR      DRAFT     REVIEWS                UNRESOLVED THREADS"

const unknownMark = "?"

// Settings are the sweep inputs: our login, and the base, integration and default branch that explain a base branch.
// IntegrationSet tells an integration branch that is empty from one that is not named.
type Settings struct {
	Me             string
	Base           string
	Integration    string
	IntegrationSet bool
	DefaultBranch  string
}

// SweepRow is one open pull request of ours. Reviews is - for none, ? for unknown, or CR: and the change requests.
// Threads is the count of open threads, or ? for unknown. Flag says what to do, and may be empty.
type SweepRow struct {
	Number  int    `json:"number"`
	Draft   bool   `json:"draft"`
	Reviews string `json:"reviews"`
	Threads string `json:"threads"`
	Flag    string `json:"flag,omitempty"`
}

// String prints the row under SweepHeader.
func (r SweepRow) String() string {
	return fmt.Sprintf("%-7s %-9s %-22s %s%s", "#"+strconv.Itoa(r.Number), strconv.FormatBool(r.Draft), r.Reviews, r.Threads, r.Flag)
}

// Sweeping is the result of a sweep: the warnings about the settings and one row for each open pull request of ours.
type Sweeping struct {
	Warnings []string
	Rows     []SweepRow
}

// Sweep lists our open pull requests with their change requests, open threads and base. One batched read serves every row.
func Sweep(ctx context.Context, own Own, stacks Stacks, s Settings) (Sweeping, error) {
	if s.Base != "" && s.Integration != "" && s.Base != s.Integration {
		return Sweeping{}, unread(nil, "the base branch (%s) and the integration branch (%s) differ. Set both to the same branch, or leave the integration branch empty.", s.Base, s.Integration)
	}

	s.DefaultBranch = defaultBranch(ctx, stacks, s)
	done := Sweeping{Warnings: warnings(s), Rows: []SweepRow{}}

	pulls, err := own.OwnPulls(ctx, s.Me)
	if err != nil {
		return Sweeping{}, unread(err, "cannot read our open pull requests")
	}

	for _, pull := range pulls {
		reviews, threads := reviewsOf(pull), unresolvedOf(pull)
		flag := openFlags(pull, threads) + baseFlag(ctx, stacks, s, pull)
		done.Rows = append(done.Rows, SweepRow{Number: pull.Number, Draft: pull.Draft, Reviews: reviews, Threads: threads, Flag: flag})
	}

	return done, nil
}

func reviewsOf(pull model.OwnPull) string {
	switch {
	case !pull.ChangesKnown:
		return unknownMark
	case len(pull.ChangeRequests) == 0:
		return none
	}

	return "CR:" + model.JoinChangeRequests(pull.ChangeRequests)
}

func unresolvedOf(pull model.OwnPull) string {
	if !pull.ThreadsKnown {
		return unknownMark
	}

	return strconv.Itoa(pull.OpenThreads)
}

func openFlags(pull model.OwnPull, threads string) string {
	flag := ""

	switch {
	case !pull.ChangesKnown:
		flag = " <-- REVIEWS UNREADABLE"
	case len(pull.ChangeRequests) > 0:
		flag = " <-- ANSWER THIS"
	}

	if threads == "0" {
		return flag
	}

	return flag + " <-- " + threads + " OPEN THREAD(S)"
}

// defaultBranch returns the named default branch, or reads it when the integration check needs it. A branch that cannot be read is unknown, shown as empty.
func defaultBranch(ctx context.Context, stacks Stacks, s Settings) string {
	if s.DefaultBranch != "" || (s.Integration == "" && s.Base != "") {
		return s.DefaultBranch
	}

	name, err := stacks.DefaultBranch(ctx)
	if err != nil {
		return ""
	}

	return name
}

func warnings(s Settings) []string {
	switch {
	case s.IntegrationSet && s.Integration == "":
		return []string{"WARNING: the integration branch is empty, so the integration-branch check is OFF. Leave it unset, or name the branch."}
	case s.IntegrationSet && s.Integration == s.DefaultBranch:
		return []string{"WARNING: the integration branch is the default branch " + s.DefaultBranch + ", so the integration-branch check is OFF. Name a branch that is not the default branch."}
	}

	return nil
}

// trunkFlag is the flag of a pull request on a trunk: none unless the round has an integration branch.
func trunkFlag(base, integration string) string {
	if integration == "" {
		return ""
	}

	return " <-- BASED ON " + base + ", NOT THE INTEGRATION BRANCH " + integration
}

// baseFlag says why a base branch is wrong: it is a trunk when the round has an integration branch, or it is in no stack.
func baseFlag(ctx context.Context, stacks Stacks, s Settings, pull model.OwnPull) string {
	if s.Integration != "" && pull.Base == s.Integration {
		return ""
	}

	if pull.Base != "" && (pull.Base == s.Base || pull.Base == s.DefaultBranch) {
		return trunkFlag(pull.Base, s.Integration)
	}

	return stackFlag(ctx, stacks, s, pull)
}

// stackFlag says why a pull request that is on no trunk is wrong: its stack cannot be read, or it has none.
func stackFlag(ctx context.Context, stacks Stacks, s Settings, pull model.OwnPull) string {
	stack, err := stacks.StackID(ctx, pull.Number)

	switch {
	case err != nil:
		return " <-- BASED ON " + pull.Base + ", STACK UNREADABLE"
	case stack != "":
		return ""
	case s.Integration != "" && s.DefaultBranch == "":
		return " <-- BASED ON " + pull.Base + ", NOT THE INTEGRATION BRANCH " + s.Integration +
			" (the default branch is unknown; if this is not the default branch, the pull request is in no stack)"
	}

	return " <-- BASED ON " + pull.Base + " BUT IN NO STACK"
}
