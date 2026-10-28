package gate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/gate"
)

const (
	gateHead = "# Gate list: owner/name\n\n## Gate steps\nDerived from: `.github/workflows/ci.yaml`, jobs `lint` and `test`, at 1a2b3c4d.\n\n"
	gatePath = "gate-steps.md"
	jobDir   = "/job"
)

var gateList = gateHead + "| name | command |\n|---|---|\n" +
	"| fmt | `cargo fmt --all -- --check` |\n" +
	"| quality | `QUALITY_BASE=<base> task check:quality` |\n" +
	"| gen-verify | `gen verify` |\n" +
	"| each-feature | `cargo hack clippy --each-feature` |\n" +
	"| docs | command: see `.github/workflows/ci-docs.yaml` |\n\n" +
	"## Build host\n| not | a gate row |\n"

// fullJob runs every step. The generator runs by its pinned path, and the quality step fills its slot.
var fullJob = strings.Join([]string{
	"#!/bin/bash",
	`cargo fmt --all -- --check; echo "STEP_RESULT name=fmt rc=$?"`,
	`QUALITY_BASE=origin/main task check:quality; echo "STEP_RESULT name=quality rc=$?"`,
	`~/tools/gen-1.2.3/bin/gen verify; echo "STEP_RESULT name=gen-verify rc=$?"`,
	"cargo hack clippy \\",
	`  --each-feature; echo "STEP_RESULT name=each-feature rc=$?"`,
	`mkdocs build --strict; echo "STEP_RESULT name=docs rc=$?"`,
	`echo "DONE-x rc=$reds gate=full"`,
	"",
}, "\n")

// jobWith returns the full job without each line that holds one of the drops, then the added lines.
func jobWith(drops []string, add ...string) string {
	var kept []string

	for _, line := range strings.Split(strings.TrimSuffix(fullJob, "\n"), "\n") {
		hit := false

		for _, drop := range drops {
			hit = hit || strings.Contains(line, drop)
		}

		if !hit {
			kept = append(kept, line)
		}
	}

	return strings.Join(append(kept, add...), "\n") + "\n"
}

func gateOf(t *testing.T, script string, files map[string]string) gate.Result {
	t.Helper()

	return gateWith(t, script, gateList, files)
}

func gateWith(t *testing.T, script, list string, files map[string]string) gate.Result {
	t.Helper()

	read := func(path string) (string, bool) {
		text, ok := files[strings.TrimPrefix(path, jobDir+"/")]

		return text, ok
	}

	result, err := gate.Job(script, list, gatePath, jobDir, read)
	require.NoError(t, err)

	return result
}

func TestGateJob_PassesAJobThatRunsEveryStep(t *testing.T) {
	t.Parallel()

	got := gateOf(t, fullJob, nil)

	assert.True(t, got.OK())
	assert.Equal(t, "check-job: OK (steps 5, run 5, declared skips 0, unstated 1)", got.Summary())
	assert.Equal(t, 1, countPrefix(got.Lines, "UNSTATED docs:"))
}

func countPrefix(lines []string, prefix string) int {
	n := 0

	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}

	return n
}

func TestGateJob_Steps(t *testing.T) {
	t.Parallel()

	skipFmt := []string{`echo "GATE-SKIP fmt: the diff holds no Rust file"`, `echo "DONE-x rc=$reds gate=partial:1"`}
	noEach := []string{"cargo hack", "--each-feature"}

	tests := []struct {
		name    string
		script  string
		ok      bool
		want    []string
		notWant []string
	}{
		{"step_with_no_line_is_refused_by_name", jobWith([]string{"cargo fmt"}), false, []string{"does not run the step fmt"}, []string{"step quality"}},
		{"command_with_a_later_part_missing_does_not_match", jobWith(noEach, "cargo hack clippy"), false, []string{"does not run the step each-feature"}, nil},
		{"command_in_a_comment_is_not_a_step", jobWith([]string{"cargo fmt"}, "# cargo fmt --all -- --check"), false, []string{"does not run the step fmt"}, nil},
		{"slot_filled_parts_out_of_order_do_not_match", jobWith([]string{"QUALITY_BASE"}, "task check:quality QUALITY_BASE=origin/main"), false, []string{"does not run the step quality"}, nil},
		{"declared_skip_passes_when_marker_says_partial", jobWith([]string{"cargo fmt"}, skipFmt...), true, []string{"run 4, declared skips 1"}, nil},
		{"skip_line_is_not_the_command_it_quotes", jobWith([]string{"cargo fmt"}, `echo "GATE-SKIP fmt: cargo fmt --all -- --check is not needed"`, `echo "DONE-x rc=$reds gate=partial:1"`), true, []string{"run 4, declared skips 1"}, nil},
		{"step_run_on_the_line_of_its_skip_counts_the_run_and_records_the_skip", jobWith([]string{"cargo fmt"}, `cargo fmt --all -- --check; echo "GATE-SKIP fmt: x"`, skipFmt[1]), true, []string{"run 5, declared skips 1"}, nil},
		{"skip_with_no_partial_marker_is_refused", jobWith([]string{"cargo fmt"}, skipFmt[0]), false, []string{"gate=partial:<count>"}, nil},
		{"skip_with_no_reason_is_refused", jobWith([]string{"cargo fmt"}, `echo "GATE-SKIP fmt: "`, skipFmt[1]), false, []string{"GATE-SKIP fmt has no reason"}, nil},
		{"skip_that_names_no_step_is_refused", jobWith(nil, `echo "GATE-SKIP format: the diff holds no Rust file"`, skipFmt[1]), false, []string{"GATE-SKIP format names no step"}, nil},
		{"skip_line_without_a_name_is_refused", jobWith(nil, "echo GATE-SKIP", skipFmt[1]), false, []string{"a GATE-SKIP line names no step: echo GATE-SKIP"}, nil},
		{"skip_for_a_missing_tool_is_refused_until_verified", jobWith(noEach, `echo "GATE-SKIP each-feature: the host does not have cargo-hack"`, skipFmt[1]), false, []string{"GATE-SKIP each-feature names a missing tool"}, nil},
		{"skip_for_a_missing_tool_passes_once_verified", jobWith(noEach, `echo "GATE-SKIP each-feature: the host does not have cargo-hack"`, skipFmt[1], "command -v cargo-hack; cargo hack --version"), true, nil, nil},
		{"step_run_on_one_branch_and_skipped_on_the_other", jobWith(noEach, "if command -v cargo-hack > /dev/null; then", `  cargo hack clippy --each-feature; echo "STEP_RESULT name=each-feature rc=$?"`, "else", `  echo "GATE-SKIP each-feature: cargo-hack is not installed"; skips=$((skips + 1))`, "fi", `echo "DONE-x rc=$reds gate=partial:$skips"`), true, []string{"run 5, declared skips 1"}, nil},
		{"unstated_step_needs_its_name_as_a_whole_word", jobWith([]string{"name=docs"}, `mkdocs build --strict; echo "STEP_RESULT name=docs-site rc=$?"`), false, []string{"names no step docs"}, nil},
		{"unstated_step_name_followed_by_a_dot_does_not_count", jobWith([]string{"name=docs"}, `mkdocs build --strict; echo "STEP_RESULT name=docs.x rc=$?"`), false, []string{"names no step docs"}, nil},
		{"unstated_step_name_followed_by_an_underscore_does_not_count", jobWith([]string{"name=docs"}, `mkdocs build --strict; echo "STEP_RESULT name=docs_x rc=$?"`), false, []string{"names no step docs"}, nil},
		{"unstated_step_name_followed_by_a_colon_does_not_count", jobWith([]string{"name=docs"}, `mkdocs build --strict; echo "STEP_RESULT name=docs:x rc=$?"`), false, []string{"names no step docs"}, nil},
		{"unstated_step_is_found_after_a_longer_name", jobWith([]string{"name=docs"}, `echo "name=docs-site name=docs rc=$?"`), true, nil, nil},
		{"unstated_step_name_ends_the_text", jobWith([]string{"name=docs"}, "echo name=docs"), true, nil, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := gateOf(t, test.script, nil)
			joined := strings.Join(got.Lines, "\n")

			assert.Equal(t, test.ok, got.OK(), joined)

			for _, want := range test.want {
				assert.Contains(t, joined+got.Summary(), want)
			}

			for _, not := range test.notWant {
				assert.NotContains(t, joined, not)
			}
		})
	}
}

func TestGateJob_RefusesAGateListThatCannotBeRead(t *testing.T) {
	t.Parallel()

	derived := "## Gate steps\nDerived from: ci.yaml at 1a2b3c4d\n| name | command |\n|---|---|\n"

	tests := []struct {
		name, list, message string
	}{
		{"no_gate_section", "# Gate list\n\n## Build host\n| fmt | cargo fmt |\n", `no "## Gate steps" section`},
		{"no_derived_from_line", strings.Replace(gateList, "Derived from:", "Source:", 1), "Derived from:"},
		{"listed_twice", "\n" + derived + "| fmt | a |\n| fmt | b |\n| lint | |\n", "fmt is listed twice"},
		{"no_command", "\n" + derived + "| fmt | a |\n| fmt | b |\n| lint | |\n", "lint has no command"},
		{"source_names_no_commit", strings.Replace(gateList, ", at 1a2b3c4d.", ".", 1), "names no commit"},
		{"two_sections", gateList + "\n## Gate steps\nDerived from: x at 1a2b3c4\n| a | b |\n", `2 "## Gate steps"`},
		{"three_columns", strings.Replace(gateList, "| fmt | `cargo fmt --all -- --check` |", "| fmt | a | b |", 1), "has 3 columns"},
		{"bad_name", strings.Replace(gateList, "| fmt |", "| f m |", 1), `"f m" is not a step name`},
		{"no_rows", derived, "has no rows"},
		{"empty_name", strings.Replace(gateList, "| fmt |", "| |", 1), `"" is not a step name`},
		{"empty_row", derived + "|\n", "has 0 columns"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			refusal := refusalOf(t, test.list)

			assert.Contains(t, refusal.Message, test.message)
			assert.Contains(t, refusal.Error(), failPrefixText)
		})
	}
}

const failPrefixText = "FAIL: "

// refusalOf returns the refusal that reading the gate list gives.
func refusalOf(t *testing.T, list string) *check.Refusal {
	t.Helper()

	_, err := gate.Steps(list, gatePath)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)

	return refusal
}

func TestGateJob_RefusalKeepsTheJobDefectsFoundFirst(t *testing.T) {
	t.Parallel()

	_, err := gate.Job(jobWith(nil, `echo "GATE-SKIP fmt: "`), "# nothing\n", gatePath, jobDir, nil)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, []string{"FAIL: GATE-SKIP fmt has no reason. Write GATE-SKIP fmt: <reason>"}, refusal.Lines)
}

func TestGateSteps_ListsOneNameAndCommandForEachStep(t *testing.T) {
	t.Parallel()

	steps, err := gate.Steps(gateList, gatePath)

	require.NoError(t, err)
	require.Len(t, steps, 5)
	assert.Equal(t, gate.Step{Name: "fmt", Command: "cargo fmt --all -- --check"}, steps[0])
	assert.Equal(t, gate.Step{Name: "docs", Command: "command: see .github/workflows/ci-docs.yaml"}, steps[4])
}

func TestGateSteps_ReadsAnEscapedPipeAndAlignedSeparators(t *testing.T) {
	t.Parallel()

	list := "## Gate steps\nDerived from: ci.yaml at 1a2b3c4d\n| name | command |\n|:--|--:|\n| pipe | `a \\| b` |\n"

	steps, err := gate.Steps(list, gatePath)

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{{Name: "pipe", Command: "a | b"}}, steps)
}

func TestGateJob_ASlotAtTheEdgeOfACommandMatchesAnyText(t *testing.T) {
	t.Parallel()

	list := gateHead + "| name | command |\n|---|---|\n| lint | `<env> lint <flags>` |\n"

	run := gateWith(t, "FOO=1 lint --all\n", list, nil)
	other := gateWith(t, "echo lint\n", list, nil)

	assert.True(t, run.OK())
	assert.True(t, other.OK())
	assert.Equal(t, "check-job: OK (steps 1, run 1, declared skips 0, unstated 0)", run.Summary())
}

// oneStepList is a gate list with one step, s, whose command is the row text.
func oneStepList(row string) string {
	return gateHead + "| name | command |\n|---|---|\n| s | `" + row + "` |\n"
}

func TestGateJob_ACommandThatSpansSeveralSimpleCommandsMatchesItsJobLine(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, row, job string }{
		{"and_list", "cargo fmt --all -- --check && cargo clippy", "cargo fmt --all -- --check && cargo clippy"},
		{"pipe", `cargo test \| tee out`, "cargo test | tee out"},
		{"semicolon", "cargo fmt; cargo clippy", "cargo fmt; cargo clippy"},
		{"subshell", "(cd x && make)", "(cd x && make)"},
		{"cd_and_make", "cd sub && make", "cd sub && make"},
		{"redirect", "cargo test 2>&1", "cargo test 2>&1"},
		{"inside_an_if", "cd sub && make", "if true; then\n  cd sub && make\nfi"},
		{"continued_line", "cargo fmt && cargo clippy", "cargo fmt && \\\n  cargo clippy"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := gateWith(t, test.job+"\n", oneStepList(test.row), nil)

			assert.True(t, got.OK(), strings.Join(got.Lines, "\n"))
		})
	}
}

func TestGateJob_ACompoundCommandKeepsItsKeywordOnOneLine(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, row, job string }{
		{"time", "time cargo fmt", "time cargo fmt"},
		{"time_with_a_pipe", `time cargo test \| tee out`, "time cargo test | tee out"},
		{"negation", "! cargo fmt", "! cargo fmt"},
		{"group", "{ cargo fmt; }", "{ cargo fmt; }"},
		{"if", "if cargo fmt; then :; fi", "if cargo fmt; then :; fi"},
		{"while", "while cargo fmt; do :; done", "while cargo fmt; do :; done"},
		{"for", "for x in a; do cargo fmt; done", "for x in a; do cargo fmt; done"},
		{"case", "case x in x) cargo fmt;; esac", "case x in x) cargo fmt;; esac"},
		{"coproc", "coproc cargo fmt", "coproc cargo fmt"},
		{"time_in_a_list", "time cargo fmt && cargo clippy", "time cargo fmt && cargo clippy"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := gateWith(t, test.job+"\n", oneStepList(test.row), nil)

			assert.True(t, got.OK(), strings.Join(got.Lines, "\n"))
		})
	}
}

func TestGateJob_AStatementWithOnlyARedirectIsNotACommand(t *testing.T) {
	t.Parallel()

	got := gateWith(t, ">out\n", oneStepList(">out"), nil)

	assert.False(t, got.OK())
	assert.Contains(t, strings.Join(got.Lines, "\n"), "does not run the step s")
}

func TestGateJob_ACompoundCommandOnSeveralLinesIsNotOneCommand(t *testing.T) {
	t.Parallel()

	got := gateWith(t, "if true; then\n  cargo fmt\n  cargo clippy\nfi\n", oneStepList("cargo fmt <x> cargo clippy"), nil)

	assert.False(t, got.OK())
	assert.Contains(t, strings.Join(got.Lines, "\n"), "does not run the step s")
}

func TestGateJob_ACommandSplitOverTwoJobLinesDoesNotMatch(t *testing.T) {
	t.Parallel()

	got := gateWith(t, "cargo fmt\ncargo clippy\n", oneStepList("cargo fmt && cargo clippy"), nil)

	assert.False(t, got.OK())
	assert.Contains(t, strings.Join(got.Lines, "\n"), "does not run the step s")
}

func TestGateJob_OnlyBlankAndTabJoinTheWordsOfACommand(t *testing.T) {
	t.Parallel()

	for _, space := range []string{" ", "\f", "\v", " "} {
		got := gateWith(t, "cargo"+space+"fmt --all -- --check\n", oneStepList("cargo fmt --all -- --check"), nil)

		assert.False(t, got.OK(), space)
		assert.Contains(t, strings.Join(got.Lines, "\n"), "does not run the step s", space)
	}

	got := gateWith(t, "cargo \t  fmt\t--all -- --check\n", oneStepList("cargo fmt --all -- --check"), nil)

	assert.True(t, got.OK())
}

func TestGateJob_ALineOfAHereDocumentCountsAsALineOfTheJob(t *testing.T) {
	t.Parallel()

	unstated := gateHead + "| name | command |\n|---|---|\n| docs | command: see ci.yaml |\n"

	tests := []struct {
		name, list, job string
		ok              bool
		summary         string
	}{
		{"step_command", oneStepList("cargo fmt --all -- --check"), "cat > run.sh <<'EOF'\ncargo fmt --all -- --check\nEOF\nbash run.sh\n", true, "run 1"},
		{"step_name", unstated, "cat <<EOF\nSTEP_RESULT name=docs rc=0\nEOF\n", true, "unstated 1"},
		{"skip_and_partial_marker", oneStepList("cargo fmt"), "cat <<EOF\nGATE-SKIP s: the diff holds no Rust file\nDONE-x rc=$rc gate=partial:1\nEOF\n", true, "declared skips 1"},
		{"skip_without_partial_marker", oneStepList("cargo fmt"), "cat <<EOF\nGATE-SKIP s: the diff holds no Rust file\nEOF\n", false, ""},
		{"skip_for_a_missing_tool_verified_in_the_body", oneStepList("cargo fmt"), "cat <<EOF\nGATE-SKIP s: cargo is missing\ncommand -v cargo\nDONE-x rc=$rc gate=partial:1\nEOF\n", true, "declared skips 1"},
		{"skip_that_names_no_step", oneStepList("cargo fmt"), "cat <<EOF\nGATE-SKIP\nEOF\n", false, ""},
		{"commented_line_is_not_a_step", oneStepList("cargo fmt"), "cat <<EOF\n# cargo fmt\nEOF\n", false, ""},
		{"continued_line", oneStepList("cargo fmt --all"), "cat <<EOF\ncargo fmt \\\n  --all\nEOF\n", true, "run 1"},
		{"commented_line_after_a_statement_on_the_line_of_the_redirect", oneStepList("cargo fmt"), "echo ok; cat <<EOF\n# cargo fmt\nEOF\n", false, ""},
		{"commented_line_after_a_follower_of_the_redirect", oneStepList("cargo fmt"), "cat <<EOF; echo ok\n# cargo fmt\nEOF\n", false, ""},
		{"commented_line_in_a_group_followed_by_a_statement_on_its_last_line", oneStepList("cargo fmt"), "{ cat <<EOF\n# cargo fmt\nEOF\n}; echo ok\n", false, ""},
		{"commented_line_in_a_group_that_follows_a_statement", oneStepList("cargo fmt"), "echo ok; { cat <<EOF\n# cargo fmt\nEOF\n}\n", false, ""},
		{"commented_line_in_a_pipeline_that_ends_with_the_redirect", oneStepList("cargo fmt"), "echo x | cat <<EOF\n# cargo fmt\nEOF\n", false, ""},
		{"commented_line_in_a_subshell", oneStepList("cargo fmt"), "(cat <<EOF\n# cargo fmt\nEOF\n)\n", false, ""},
		{"commented_line_after_a_pipeline_with_a_redirect", oneStepList("cargo fmt"), "cat <<EOF | grep x\n# cargo fmt\nEOF\n", false, ""},
		{"step_command_after_the_redirect_on_its_line", oneStepList("cargo fmt"), "cat <<EOF; cargo fmt\nbody\nEOF\n", true, "run 1"},
		{"step_command_before_the_redirect_on_its_line", oneStepList("cargo fmt"), "cargo fmt; cat <<EOF\nbody\nEOF\n", true, "run 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := gateWith(t, test.job, test.list, nil)

			assert.Equal(t, test.ok, got.OK(), strings.Join(got.Lines, "\n"))
			assert.Contains(t, got.Summary()+strings.Join(got.Lines, "\n"), test.summary)
		})
	}
}

func TestGateJob_CarriageReturnsKeepTheirLines(t *testing.T) {
	t.Parallel()

	for _, ending := range []string{"\r\n", "\r"} {
		got := gateOf(t, strings.ReplaceAll(fullJob, "\n", ending), nil)

		assert.False(t, got.OK(), ending)
		assert.Contains(t, strings.Join(got.Lines, "\n"), "FAIL: the job does not run the step each-feature", ending)

		refusal := refusalOf(t, strings.ReplaceAll(gateList, "\n", ending))

		assert.Contains(t, refusal.Message, `has no "## Gate steps" section`, ending)
	}
}
