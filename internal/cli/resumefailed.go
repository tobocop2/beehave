package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	sinceFlag = "since"
	untilFlag = "until"
	limitFlag = "limit"
)

// errResumeShape is returned when the item and the --failed flag do not fit together.
var errResumeShape = errors.New("give an item, or --failed with no item")

// errNoFailed is returned when beehave resume --failed finds no failed run that fits the filter.
var errNoFailed = errors.New("no failed run matches")

// errNotResumed is returned when at least one run of beehave resume --failed refused.
var errNotResumed = errors.New("some runs did not resume")

// manyResume holds the flags that make beehave resume reopen every failed run that fits a filter.
type manyResume struct {
	failed, dryRun, onThisBuild bool
	since, until, project       string
	limit                       int
	// picked is the filter that the check of the arguments read from the flags.
	picked flow.FailedFilter
}

// addFlags adds the flags of beehave resume --failed.
func (m *manyResume) addFlags(command *cobra.Command) {
	command.Flags().BoolVar(&m.failed, "failed", false, "resume every failed run that fits the filter; give no item")
	command.Flags().StringVar(&m.since, sinceFlag, "", "with --failed: only runs that ended at or after this time, an RFC 3339 time or a duration such as 6h for that long ago")
	command.Flags().StringVar(&m.until, untilFlag, "", "with --failed: only runs that ended at or before this time, in the same forms as --since")
	command.Flags().StringVar(&m.project, projectFlag, "", "with --failed: only runs of this project")
	command.Flags().IntVar(&m.limit, limitFlag, 0, "with --failed: resume at most this many runs, the ones that ended first. 0 sets no limit")
	command.Flags().BoolVar(&m.dryRun, dryRunFlag, false, "with --failed: list the runs that would resume, and change nothing")
	command.Flags().BoolVar(&m.onThisBuild, "on-this-build", false, "move a run that another build started to this build")
}

// typed returns the filter flags that the owner typed, even with an empty value.
func typed(cmd *cobra.Command) []string {
	var names []string

	for _, name := range []string{sinceFlag, untilFlag, projectFlag, limitFlag, dryRunFlag} {
		if cmd.Flags().Changed(name) {
			names = append(names, "--"+name)
		}
	}

	return names
}

// resumeArgs checks the arguments of beehave resume: one item, or with --failed no item and a valid filter.
func (m *manyResume) resumeArgs(now func() time.Time, cmd *cobra.Command, args []string) error {
	names := typed(cmd)

	switch {
	case m.failed && len(args) > 0:
		return fmt.Errorf("%w: %q is an item, and --failed resumes every failed run", errResumeShape, args[0])
	case m.failed:
		var err error

		m.picked, err = m.filter(now(), cmd)

		return err
	case len(names) > 0:
		return fmt.Errorf("%w: %s needs --failed", errResumeShape, strings.Join(names, ", "))
	}

	return oneItem(cmd, args)
}

// filter reads the flags into the filter of the failed runs, and refuses a value that picks nothing or everything by mistake.
func (m *manyResume) filter(now time.Time, cmd *cobra.Command) (flow.FailedFilter, error) {
	if cmd.Flags().Changed(projectFlag) && m.project == "" {
		return flow.FailedFilter{}, fmt.Errorf("%w: --project is empty, and an empty filter would pick every run. Give a project or remove the flag", errResumeShape)
	}

	since, until, err := m.span(cmd, now)
	if err != nil {
		return flow.FailedFilter{}, err
	}

	if m.limit < 0 {
		return flow.FailedFilter{}, fmt.Errorf("%w: --limit is %d. Give 0 for no limit, or a count above 0", errResumeShape, m.limit)
	}

	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return flow.FailedFilter{}, fmt.Errorf("%w: --until is before --since: swap them", errResumeShape)
	}

	return flow.FailedFilter{Since: since, Until: until, Project: m.project, Limit: m.limit}, nil
}

// span reads --since and --until, and refuses a start in the future.
func (m *manyResume) span(cmd *cobra.Command, now time.Time) (since, until time.Time, err error) {
	since, err = bound(cmd, sinceFlag, m.since, now)
	if err == nil && since.After(now) {
		err = fmt.Errorf("%w: --since %s is in the future, so no run ended after it. Give a time in the past", errResumeShape, m.since)
	}

	until, untilErr := bound(cmd, untilFlag, m.until, now)

	return since, until, errors.Join(err, untilErr)
}

// bound reads one time flag: an RFC 3339 time, or a duration that counts back from now. A flag that the owner did not type is the zero time.
func bound(cmd *cobra.Command, name, text string, now time.Time) (time.Time, error) {
	if !cmd.Flags().Changed(name) {
		return time.Time{}, nil
	}

	if at, err := time.Parse(time.RFC3339, text); err == nil {
		return at, nil
	}

	if span, err := time.ParseDuration(text); err == nil && span >= 0 {
		return now.Add(-span), nil
	}

	return time.Time{}, fmt.Errorf("%w: --%s %q is not a time. Give an RFC 3339 time such as 2026-03-01T10:00:00Z, or a duration such as 6h for that long ago", errResumeShape, name, text)
}

// describe names the filter in words, for the error of an empty list.
func describe(filter flow.FailedFilter) string {
	var parts []string

	if !filter.Since.IsZero() {
		parts = append(parts, "ended since "+filter.Since.UTC().Format(time.RFC3339))
	}

	if !filter.Until.IsZero() {
		parts = append(parts, "ended until "+filter.Until.UTC().Format(time.RFC3339))
	}

	if filter.Project != "" {
		parts = append(parts, "project "+filter.Project)
	}

	if len(parts) == 0 {
		return "the store holds no failed run"
	}

	return "no failed run has " + strings.Join(parts, ", ")
}

// resumeMany lists the failed runs that fit the filter and resumes each one through the resume of a single item.
// It prints one line for each run and goes on after a refusal. A dry run prints the lines and resumes nothing.
func (m *manyResume) resumeMany(out io.Writer, client Steering, filter flow.FailedFilter) error {
	runs, err := client.FailedRuns(filter)
	if err != nil {
		return err
	}

	if len(runs) == 0 {
		return fmt.Errorf("%w: %s. Run beehave status to see the runs", errNoFailed, describe(filter))
	}

	if m.dryRun {
		listOnly(out, runs)

		return nil
	}

	return m.resumeAll(out, client, runs)
}

// listOnly prints each run as one that would resume, and the count.
func listOnly(out io.Writer, runs []flow.FailedRun) {
	for _, failed := range runs {
		printFailed(out, failed, "would resume")
	}

	_, _ = fmt.Fprintf(out, "%d would resume\n", len(runs))
}

// resumeAll resumes each run in turn, prints the counts, and returns an error that says how many runs refused.
func (m *manyResume) resumeAll(out io.Writer, client Steering, runs []flow.FailedRun) error {
	tally := tallyResumes{}
	refused := countRefused(runs, func(failed flow.FailedRun) bool { return tally.resume(out, client, failed, m.onThisBuild) })

	_, _ = fmt.Fprintf(out, "%d resumed, %d skipped, %d refused\n", tally.resumed, tally.skipped, refused)

	if refused > 0 {
		return fmt.Errorf("%w: %d of %d runs refused", errNotResumed, refused, len(runs))
	}

	return nil
}

// tallyResumes counts the runs that resumed and the runs that another process had already resumed.
type tallyResumes struct{ resumed, skipped int }

// resume resumes one failed run, prints its line, and reports whether it did not refuse. A run in the wrong state is skipped: another process resumed it after the list.
func (t *tallyResumes) resume(out io.Writer, client Steering, failed flow.FailedRun, onThisBuild bool) bool {
	err := resumeItem(client, failed.Item, onThisBuild)

	switch {
	case err == nil:
		t.resumed++

		printFailed(out, failed, "resumed")
	case errors.Is(err, flow.ErrWrongState):
		t.skipped++

		printFailed(out, failed, "skipped: "+model.OneLine(err.Error()))
	default:
		printFailed(out, failed, "refused: "+model.OneLine(err.Error()))

		return false
	}

	return true
}

// printFailed writes the line of one failed run: item, run id, end time and what happened to it.
func printFailed(out io.Writer, failed flow.FailedRun, outcome string) {
	_, _ = fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", failed.Item, failed.ID, failed.Ended.UTC().Format(time.RFC3339), outcome)
}

// countRefused calls take for each item in turn and returns how many items it did not take.
func countRefused[T any](items []T, take func(T) bool) int {
	refused := 0

	for _, item := range items {
		if !take(item) {
			refused++
		}
	}

	return refused
}
