package remedy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const integrationSyntax = "integration --base <round branch> --into <trunk> [--pr <number>] [--label <name>]"

// listLimit is the most pull requests that one list call reads.
const listLimit = 100

// errFullList is the refusal of a list of merged pull requests that reached listLimit, so some can be missing from it.
var errFullList = errors.New("the list of merged pull requests is full")

// Integrator is the forge of the integration subcommand: the reads of the pull requests, the opening of a draft pull request and the write of a body.
// A forge that lacks it refuses the subcommand. Every write goes through it, so no tool calls the forge command line itself.
type Integrator interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	CreatePR(ctx context.Context, req model.NewPull) (int, error)
	SetBody(ctx context.Context, pr int, body string) error
}

// integrationFlags are the values of the flags of integration.
type integrationFlags struct {
	base, into, label string
	pr                int
}

func parseIntegration(args []string, errOut io.Writer) (integrationFlags, bool) {
	var given integrationFlags

	flags := flag.NewFlagSet("remedy integration", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&given.base, "base", "", "the round branch: the head of the integration pull request")
	flags.StringVar(&given.into, "into", "", "the trunk: the base of the integration pull request")
	flags.StringVar(&given.label, "label", defaultLabel, "the label of the integration pull request when the tool opens it; empty adds none")
	flags.IntVar(&given.pr, "pr", 0, "the number of the integration pull request; 0 finds it, and opens it when there is none")

	if flags.Parse(args) != nil || flags.NArg() > 0 || given.base == "" || given.into == "" || given.pr < 0 {
		say(errOut, "usage: remedy %s\n", integrationSyntax)

		return given, false
	}

	return given, true
}

// integrationCommand finds the integration pull request, or opens it, writes its body again from the pull requests that merged into the round branch,
// and prints `pr=<n> state=<state>`. A failed read or write is exit code 2.
func integrationCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseIntegration(args, s.err)
	if !ok {
		return ExitRefused
	}

	pull, state, err := integrate(ctx, deps, given)
	if err != nil {
		return refused(s, "integration", err)
	}

	say(s.out, "pr=%d state=%s\n", pull, state)

	return ExitPass
}

// integrate opens the forge of the origin remote and runs the integration on it.
func integrate(ctx context.Context, deps Deps, given integrationFlags) (int, string, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return 0, "", err
	}

	opened, err := deps.Forge(repo)
	if err != nil {
		return 0, "", err
	}

	forge, ok := opened.(Integrator)
	if !ok {
		return 0, "", errNoIntegrator
	}

	return integrateOn(ctx, forge, given)
}

// integrateOn finds the integration pull request or opens it, and makes its body again from the pull requests merged into the round branch.
func integrateOn(ctx context.Context, forge Integrator, given integrationFlags) (int, string, error) {
	pull, err := findIntegration(ctx, forge, given)
	if err != nil {
		return 0, "", err
	}

	if state := stateOf(pull); pull.Number != 0 && state != stateOpen {
		return pull.Number, state, nil
	}

	merged, err := mergedInto(ctx, forge, given.base)
	if err != nil {
		return 0, "", err
	}

	if pull.Number == 0 {
		return openIntegration(ctx, forge, given, merged)
	}

	return pull.Number, stateOpen, writeBody(ctx, forge, given, pull, merged)
}

// mergedInto lists the pull requests that merged into the round branch. A full list can hold only a part of them,
// and a body made from it would leave out the rest with no sign, so a full list is a refusal.
func mergedInto(ctx context.Context, forge Integrator, base string) ([]model.PullRequest, error) {
	merged, err := forge.PRList(ctx, listLimit, "--state", "merged", "--base", base, "--json", "number,title,state,headRefName")
	if err != nil {
		return nil, err
	}

	if len(merged) >= listLimit {
		return nil, fmt.Errorf("%w: %s has %d or more merged pull requests. Split the round into smaller rounds, then run the tool again", errFullList, base, listLimit)
	}

	return merged, nil
}

// writeBody makes the body of the pull request again from the merged pull requests, and writes it only when it differs from the body now.
func writeBody(ctx context.Context, forge Integrator, given integrationFlags, pull model.PullRequest, merged []model.PullRequest) error {
	body := roundBody(given.base, given.into, merged)
	if body == pull.Body {
		return nil
	}

	return forge.SetBody(ctx, pull.Number, body)
}

// findIntegration reads the pull request that the number names, or looks for the one from the round branch into the trunk: an open one first, then a merged one.
// A zero Number in the result means there is none.
func findIntegration(ctx context.Context, forge Integrator, given integrationFlags) (model.PullRequest, error) {
	if given.pr != 0 {
		pull, err := forge.PRView(ctx, given.pr, "number", "state", "body")
		pull.Number = given.pr

		return pull, err
	}

	found, err := forge.PRList(ctx, listLimit, "--state", "all", "--head", given.base, "--base", given.into, "--json", "number,state,body")
	if err != nil {
		return model.PullRequest{}, err
	}

	return firstOpenOrMerged(found), nil
}

// firstOpenOrMerged returns the first open pull request of the list, else the first merged one, else a pull request with a zero Number.
func firstOpenOrMerged(found []model.PullRequest) model.PullRequest {
	for _, want := range []string{stateOpen, "MERGED"} {
		for _, pull := range found {
			if stateOf(pull) == want {
				return pull
			}
		}
	}

	return model.PullRequest{}
}

// openIntegration opens the draft pull request from the round branch into the trunk, with the body of the pull requests that merged so far.
func openIntegration(ctx context.Context, forge Integrator, given integrationFlags, merged []model.PullRequest) (int, string, error) {
	name := given.base[strings.LastIndex(given.base, "/")+1:]

	number, err := forge.CreatePR(ctx, model.NewPull{Head: given.base, Base: given.into, Title: "Integration round " + name, Body: roundBody(given.base, given.into, merged), Label: given.label})
	if err != nil {
		return 0, "", err
	}

	return number, stateOpen, nil
}

func stateOf(pull model.PullRequest) string {
	if pull.State == nil {
		return "UNKNOWN"
	}

	return *pull.State
}

var digits = regexp.MustCompile(`^\d+$`)

const (
	stateOpen    = "OPEN"
	tableHeading = "## Pull requests merged into this branch"
	tableHead    = "| PR | Change |"
	tableRule    = "|---|---|"
	syncHeading  = "## Syncs with the trunk"
	closeHeading = "## Issues this batch closes"
	syncPrefix   = "sync/"
)

// roundBody is the whole body of the integration pull request, made from the pull requests that merged into the round branch.
// It names the round and what the pull request collects, then lists the item pull requests as rows, the syncs with the trunk as one line of numbers, and the issues of the items as `- Closes #N` lines.
// A sync is a pull request from a branch under sync/<round branch>/. It brings trunk commits and adds no work, so it is no row. Each list is in the order of the pull request numbers.
func roundBody(base, into string, merged []model.PullRequest) string {
	sorted := slices.SortedFunc(slices.Values(merged), func(a, b model.PullRequest) int { return a.Number - b.Number })

	var items, syncs []model.PullRequest

	for _, pull := range sorted {
		if isSync(pull, base) {
			syncs = append(syncs, pull)
		} else {
			items = append(items, pull)
		}
	}

	name := base[strings.LastIndex(base, "/")+1:]
	parts := []string{fmt.Sprintf("Round %s collects the item pull requests merged into %s. The engine merges this pull request into %s when the round lands.", name, base, into)}

	parts = append(parts, itemsSection(items), syncsSection(syncs), closesSection(items))

	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), "\n\n") + "\n"
}

// isSync reports a pull request from a sync branch of the round branch.
func isSync(pull model.PullRequest, base string) bool {
	return pull.HeadRefName != nil && strings.HasPrefix(*pull.HeadRefName, syncPrefix+base+"/")
}

// itemsSection is the table of the item pull requests, or one line that says there are none.
func itemsSection(items []model.PullRequest) string {
	if len(items) == 0 {
		return "No item pull request has merged into this branch yet."
	}

	lines := []string{fmt.Sprintf("%s %s", tableHeading, figureOf(len(items))), "", tableHead, tableRule}

	for _, pull := range items {
		lines = append(lines, fmt.Sprintf("| #%d | %s |", pull.Number, strings.ReplaceAll(pull.Title, "|", `\|`)))
	}

	return strings.Join(lines, "\n")
}

// syncsSection is the count of the syncs and their numbers on one line, or an empty text when there are none.
func syncsSection(syncs []model.PullRequest) string {
	if len(syncs) == 0 {
		return ""
	}

	numbers := make([]string, len(syncs))
	for i, pull := range syncs {
		numbers[i] = fmt.Sprintf("#%d", pull.Number)
	}

	return fmt.Sprintf("%s %s\n\n%s", syncHeading, figureOf(len(syncs)), strings.Join(numbers, ", "))
}

// closesSection is one `- Closes #N` line for each issue that an item resolves, or an empty text when none does. The item id of a pull request is its head branch, and an id of digits is the number of the issue.
// An issue shows once, however many pull requests name it.
func closesSection(items []model.PullRequest) string {
	var issues []string

	for _, pull := range items {
		issue := issueOf(pull)
		if issue != "" && !slices.Contains(issues, "- Closes #"+issue) {
			issues = append(issues, "- Closes #"+issue)
		}
	}

	if len(issues) == 0 {
		return ""
	}

	return closeHeading + "\n\n" + strings.Join(issues, "\n")
}

// issueOf returns the issue number that the head branch of the pull request names, or an empty text when the branch is not an item id of digits.
func issueOf(pull model.PullRequest) string {
	if pull.HeadRefName == nil || !digits.MatchString(*pull.HeadRefName) {
		return ""
	}

	return *pull.HeadRefName
}

func figureOf(rows int) string {
	if rows == 1 {
		return "(1 pull request)"
	}

	return fmt.Sprintf("(%d pull requests)", rows)
}
