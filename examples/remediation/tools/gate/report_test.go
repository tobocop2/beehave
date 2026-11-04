package gate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

const (
	reportHead   = "abcdef1234567890abcdef1234567890abcdef12"
	noBackground = "no background commands left"
	redLine      = "test empty_name_refused ... FAILED"
)

var goodReport = "# Report\n\n## Claims\n| Claim | Where | Evidence | Verified at |\n|---|---|---|---|\n" +
	"| refuses an empty name | body | test empty_name_refused, red at base | " + reportHead[:12] + " |\n" +
	"| closes #12 | closing | the issue's repro fails at base, passes at head | " + reportHead + " |\n\n" +
	"## Hunks\n| Hunk | Arm | Red test line |\n|---|---|---|\n" +
	"| src/a.rs:10-14 | handler never calls validate() | " + redLine + " |\n\n" +
	"## Inputs\n| Input class | Base | Head | Test |\n|---|---|---|---|\n" +
	"| empty string | accepted | refused 400 | empty_name_refused |\n" +
	"| a \\| in the name | accepted | accepted | pipe_name_kept |\n\n" +
	"## Siblings\n| Site | Same behavior | Updated |\n|---|---|---|\n" +
	"| src/b.rs:40 create route | validates the same name | yes |\n" +
	"| src/c.rs:9 import | names come from a trusted file | no: not user input |\n\n" +
	noBackground + "\n"

const (
	siblingB = "| src/b.rs:40 create route | validates the same name | yes |"
	siblingC = "| src/c.rs:9 import | names come from a trusted file | no: not user input |"
)

func dropLines(text string, marks ...string) string {
	var kept []string

	for _, line := range strings.Split(text, "\n") {
		drop := false

		for _, mark := range marks {
			drop = drop || strings.Contains(line, mark)
		}

		if !drop {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

func TestReport_Passes(t *testing.T) {
	t.Parallel()

	got := gate.Report(goodReport, reportHead)

	assert.True(t, got.OK())
	assert.Equal(t, "check-report: OK at abcdef123456 (claims 2, hunks 1, inputs 2, siblings 2)", got.Summary(reportHead))
	assert.True(t, gate.Report(goodReport, reportHead[:8]).OK(), "a short head is a prefix of the longer verified form")
}

func TestReport_NoBackgroundForms(t *testing.T) {
	t.Parallel()

	for _, form := range []string{
		"no background commands left", "no background commands left.", "`no background commands left`", "`no background commands left`.",
		"**no background commands left**", "**no background commands left.**", "- no background commands left",
		"- `no background commands left`.", "* **no background commands left**", "No Background Commands Left.",
	} {
		assert.Empty(t, gate.Report(strings.Replace(goodReport, noBackground, form, 1), reportHead).Failures, form)
	}

	without := strings.Replace(goodReport, noBackground, "", 1)
	failures := gate.Report(without, reportHead).Failures
	assert.Len(t, failures, 1)
	assert.Contains(t, failures[0], noBackground)

	mentions := without + "I cannot say that there are no background commands left, one is alive.\nno background commands left, except the wait loop\n"
	assert.Len(t, gate.Report(mentions, reportHead).Failures, 1)

	elsewhere := without + "# Lane end\nNo Background Commands Left.\n"
	assert.Empty(t, gate.Report(elsewhere, reportHead).Failures)
}

func TestReport_Failures(t *testing.T) {
	t.Parallel()

	beforeSiblings, _, _ := strings.Cut(goodReport, "## Siblings")
	oneNone := strings.Replace(dropLines(goodReport, "src/b.rs:40"), siblingC, "| none: no other site renders names | - | - |", 1)

	cases := []struct {
		name, text string
		want       []string
	}{
		{"missing_section", beforeSiblings + noBackground, []string{"no ## Siblings section"}},
		{"stale_claim", strings.Replace(goodReport, "| "+reportHead+" |", "| 0123456789ab |", 1), []string{`Claims row 2: verified at 0123456789ab, not at the head abcdef123456 ("closes #12")`}},
		{"six_characters_stale", strings.Replace(goodReport, "| "+reportHead+" |", "| "+reportHead[:6]+" |", 1), []string{`Claims row 2: verified at abcdef, not at the head abcdef123456 ("closes #12")`}},
		{"uncovered", strings.Replace(goodReport, redLine, "UNCOVERED: suite stayed green", 1), []string{"Hunks row 1: src/a.rs:10-14 has no red test (UNCOVERED: suite stayed green)"}},
		{"bare_uncovered", strings.Replace(goodReport, redLine, "UNCOVERED", 1), []string{"Hunks row 1: src/a.rs:10-14 has no red test (UNCOVERED)"}},
		{"not_run", strings.Replace(goodReport, redLine, "not run: cargo-mutants missing", 1), []string{"Hunks row 1: src/a.rs:10-14 has no red test (not run: cargo-mutants missing)"}},
		{"green_in_capitals", strings.Replace(goodReport, redLine, "GREEN: the arm passed", 1), []string{"Hunks row 1: src/a.rs:10-14 has no red test (GREEN: the arm passed)"}},
		{"unquoted_not_run_beside_quoted", strings.Replace(goodReport, redLine, "not run: `cargo mutants` was missing", 1), []string{"Hunks row 1: src/a.rs:10-14 has no red test (not run: `cargo mutants` was missing)"}},
		{"empty_cell", strings.Replace(goodReport, "| accepted | refused 400 |", "|  | refused 400 |", 1), []string{"Inputs row 1: empty cell in column 2"}},
		{"placeholder", strings.Replace(goodReport, "names come from a trusted file", "TODO", 1), []string{"Siblings row 2: placeholder in column 2: TODO"}},
		{"sibling_needs_reason", strings.Replace(goodReport, "| no: not user input |", "| no |", 1), []string{`Siblings row 2: Updated must be "yes" or "no: <reason>", got "no"`}},
		{"none_beside_rows", strings.Replace(goodReport, siblingB, "| none: nothing | - | - |\n"+siblingB, 1), []string{`## Siblings mixes a "none:" row with other rows`}},
		{"wrong_column_count", strings.Replace(goodReport, siblingB, "| src/b.rs:40 | yes |", 1), []string{"Siblings row 1: 2 columns, expected 3"}},
		{"bare_pipe_row", strings.Replace(goodReport, siblingB, "|", 1), []string{"Siblings row 1: 0 columns, expected 3"}},
		{"empty_verified_cell", strings.Replace(goodReport, "red at base | "+reportHead[:12]+" |", "red at base |  |", 1), []string{"Claims row 1: empty cell in column 4"}},
		{"empty_table", dropLines(goodReport, "src/b.rs:40", "src/c.rs:9"), []string{`## Siblings has no rows. A table with nothing to list has one "none: <reason>" row`}},
		{"table_outside_sections_ignored", strings.Replace(goodReport, "# Report\n", "# Report\n\n## Notes\n| a | b |\n|--|--|\n| TODO | x |\n", 1), nil},
		{"one_none_row_passes", oneNone, nil},
		{"quoted_output_is_evidence", strings.Replace(goodReport, redLine, redLine+": `child test did not run exactly one passing test`", 1), nil},
		{"green_inside_a_name", strings.Replace(goodReport, redLine, "test green_path_refused ... FAILED", 1), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, gate.Report(tc.text, reportHead).Failures)
		})
	}
}

func TestReport_CarriageReturnsStayInTheText(t *testing.T) {
	t.Parallel()

	got := gate.Report(strings.ReplaceAll(goodReport, "\n", "\r\n"), reportHead)

	assert.Equal(t, []string{
		"no ## Claims section",
		"no ## Hunks section",
		"no ## Inputs section",
		"no ## Siblings section",
		`no line says "no background commands left". Stop every background command you started, by task id, then say so`,
	}, got.Failures)
}

func TestReport_SummaryCutsAHeadByCharacters(t *testing.T) {
	t.Parallel()

	assert.Contains(t, gate.Report(goodReport, reportHead).Summary("héad"), "OK at héad (")
}
