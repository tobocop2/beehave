package check_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const doneR = `echo "DONE-x rc=$R"`

// markerOf is the full job with the lines in place of its marker line.
func markerOf(t *testing.T, files map[string]string, lines ...string) (bool, string) {
	t.Helper()

	got := gateOf(t, jobWith([]string{"DONE-x"}, lines...), files)

	return got.OK(), strings.Join(got.Lines, "\n")
}

func TestExitMarker_RefusesAMarkerLineThatPrintsALiteralExitStatus(t *testing.T) {
	t.Parallel()

	fixed := []string{
		`echo "DONE-x rc=0"`, `echo "DONE-x rc=1 gate=full"`, "printf 'DONE-x rc=2\\n'", "echo DONE-x rc=0",
		"cat <<EOT\nDONE-x rc=0\nEOT", `R=0; echo "DONE-x rc=$R"`, "R=1\necho \"DONE-x rc=${R}\"",
		`printf "DONE-x rc=%d\n" 0`, `echo "DONE-x rc=-1"`, "echo \"DONE-x rc=\" \\\n  0",
		`echo "DONE-x rc=${R:-0}"`, `echo "DONE-x rc=${R-0}"`, `echo "DONE-x rc=${R:=0}"`,
		`cat <<< "DONE-x rc=0"`, `cat <<<"DONE-x rc=1"`, `echo "DONE-x rc=0x0"`, `echo "DONE-x rc=0X1"`,
		`ssh box 'echo "DONE-x rc=0"'`, `ssh box "printf 'DONE-x rc=0\n'"`, `R=0x0; echo "DONE-x rc=$R"`,
		`printf "DONE-x rc=%d\n" 0x0`, "bash <<EOT\necho \"DONE-x rc=0\"\nEOT", `printf "DONE-x rc=%s\n" 0`,
		`printf "DONE-x rc=%i\n" 3`, `printf "DONE-x rc=%d\n" 0 extra`, "echo \"DONE-x rc=0\" > f", "X=1 <<EOT\nDONE-x rc=0\nEOT", "while read l; do :; done <<EOT\nDONE-x rc=0\nEOT", "R=0\nsource\n" + doneR,
	}

	for _, line := range fixed {
		ok, out := markerOf(t, nil, line)

		assert.False(t, ok, line)
		assert.Contains(t, out, fixedMsg, line)
	}
}

func TestExitMarker_AcceptsAMarkerLineThatPrintsTheRealStatus(t *testing.T) {
	t.Parallel()

	real := []string{
		`echo "DONE-x rc=$?"`, `echo "DONE-x rc=$rc"`, `echo "DONE-x rc=${rc}"`, `echo "DONE-x rc=$((a + b))"`,
		`R=0; R=$((R + 1)); echo "DONE-x rc=$R"`, `printf "DONE-x rc=%d\n" "$?"`, `printf "DONE-x rc=%d\n" $reds`,
		`R=$?; echo "DONE-x rc=${R:-0}"`, `echo "DONE-x rc=${?:-0}"`, `ssh box 'echo "note: rc=0 means ok"'`,
		`echo "DONE-x rc=0x$?"`, `echo "DONE-x rc=0x"`, `echo "DONE-x rc=$"`, `echo "DONE-x rc=%x"`, "printf rc=%d 0", "printf rc=%d",
		`echo "DONE-x rc=${R:-}"`, `echo "DONE-x rc=${R:+0}"`, `echo "DONE-x rc=${R:-x}"`, `echo "DONE-x rc=${9:-0}"`,
		`echo "DONE-x rc=$9"`, `echo "DONE-x rc=0 =x"`, `echo "DONE-x rc=0 -x=y"`,
		`echo "DONE-x rc=0 1x=y"`, `echo "DONE-x rc=0 x"`, `echo "arc=0"`, `echo "DONE-x rc=$R" x=1`,
	}

	for _, line := range real {
		ok, out := markerOf(t, nil, line)

		assert.True(t, ok, line+"\n"+out)
	}
}

func TestExitMarker_ReadsOnlyMarkerLines(t *testing.T) {
	t.Parallel()

	lines := []string{
		"rc=0", `echo "STEP_RESULT name=fmt rc=0"`, `echo "note: rc=0 means ok"`, "true # echo DONE-x rc=0",
		"run-tool rc=0 --flag", "ssh", "R=0", "let R=1", "echo", "cat <<EOT\nSTEP_RESULT rc=0\nEOT",
		`echo "GATE-SKIP docs: not needed rc=0"; echo "DONE-x rc=$? gate=partial:1"`,
	}

	for _, line := range lines {
		script := jobWith([]string{"docs", "DONE-x"}, line)
		got := gateOf(t, script, nil)

		assert.NotContains(t, strings.Join(got.Lines, "\n"), fixedMsg, line)
	}
}

func TestExitMarker_ALaterChangeMakesTheStatusReal(t *testing.T) {
	t.Parallel()

	changes := []string{
		"((R++))", "(( R += 1 ))", "let R=R+1", `let "R += 1"`, "R+=1", "read R < /dev/null", "read -r a R < /dev/null",
		"for R in 1 2; do :; done", "while getopts ab R; do :; done", `eval "R=\$?"`, "source ./state.sh", ". ./state.sh",
		"printf -v R %s 3", "mapfile R < /dev/null", "select R in a b; do :; done", "R=$((R-1))", "((--R))", "(( R[0]++ ))",
	}

	for _, change := range changes {
		ok, out := markerOf(t, map[string]string{"./state.sh": "R=$?\n"}, "R=0", change, doneR)

		assert.True(t, ok, change+"\n"+out)
	}
}

func TestExitMarker_ALaterLineThatChangesNothingKeepsTheStatusFixed(t *testing.T) {
	t.Parallel()

	lines := [][]string{
		{"R=0", "read other < /dev/null", "for i in 1 2; do :; done", doneR},
		{"R=0", "printf -v other %s 3", "local", "declare R", "printf", `let "("`, doneR},
		{"R=0", "(( other++ ))", "let", `let "other"`, "(( 1 + 2 ))", doneR},
		{"R=0", "echo ${#R}", doneR},
	}

	for _, set := range lines {
		ok, out := markerOf(t, nil, set...)

		assert.False(t, ok, set)
		assert.Contains(t, out, fixedMsg)
	}
}

func TestExitMarker_ASourceOrEvalThatCannotSetTheVariableKeepsTheStatusFixed(t *testing.T) {
	t.Parallel()

	files := map[string]string{"./other.sh": "Q=1\n", "/t/other.sh": "Q=1\n", "/dev/null": ""}
	bodies := []string{
		"R=0\n. /dev/null\n" + doneR, "R=0\n" + doneR + "\n. /dev/null", "R=0\nsource ./other.sh\n" + doneR,
		"R=0\n. /t/other.sh\n" + doneR, "R=0\neval \"echo hi\"\n" + doneR,
		"R=0\nif (( $# )); then echo \"R=1\"; fi\n" + doneR,
	}

	for _, body := range bodies {
		ok, out := markerOf(t, files, body)

		assert.False(t, ok, body)
		assert.Contains(t, out, fixedMsg, body)
	}
}

func TestExitMarker_ASourceThatSetsTheVariableOrCannotBeReadOrAnEvalHidesIt(t *testing.T) {
	t.Parallel()

	files := map[string]string{"./state.sh": "R=$?\n", "/t/state.sh": "R=$?\n", "./outer.sh": ". ./state.sh\n"}
	bodies := []string{
		"R=0\n. ./state.sh\n" + doneR, "R=0\nsource /t/state.sh\n" + doneR, "R=0\n. ./outer.sh\n" + doneR,
		"R=0\n. ./missing.sh\n" + doneR, "R=0\n. \"$DIR/x.sh\"\n" + doneR, "R=0\neval \"$cmd\"\n" + doneR,
		"R=0\neval \"R=\\$?\"\n" + doneR, "R=0\neval\n" + doneR, "R=0\n. ./*.sh\n" + doneR, "R=0\n. `pwd`/x.sh\n" + doneR,
		"R=0\n. ~/x.sh\n" + doneR,
	}

	for _, body := range bodies {
		ok, out := markerOf(t, files, body)

		assert.True(t, ok, body+"\n"+out)
	}
}

func TestExitMarker_AFileThatSourcesItselfIsReadOnce(t *testing.T) {
	t.Parallel()

	ok, out := markerOf(t, map[string]string{"/t/loop.sh": "Q=1\n. /t/loop.sh\n"}, "R=0", ". /t/loop.sh", doneR)

	assert.False(t, ok)
	assert.Contains(t, out, fixedMsg)
}

func TestExitMarker_ASourcedFileWithALoneCarriageReturnIsOneLine(t *testing.T) {
	t.Parallel()

	ok, out := markerOf(t, map[string]string{"/t/lib.sh": "echo hi\rR=1\n"}, "R=0", ". /t/lib.sh", doneR)

	assert.False(t, ok)
	assert.Contains(t, out, fixedMsg)
}

func TestExitMarker_ACarriageReturnAfterAStatusIsNotAKeyValueWord(t *testing.T) {
	t.Parallel()

	ok, out := markerOf(t, nil, "cat <<EOT\r\nDONE-x rc=0\r\nEOT")

	assert.True(t, ok, out)
}

func TestExitMarker_OneLeadingQuoteIsStrippedBeforeTheStatus(t *testing.T) {
	t.Parallel()

	for text, fixed := range map[string]bool{`DONE-x rc="0`: true, `DONE-x rc='0`: true, `DONE-x rc="'0`: false} {
		ok, _ := markerOf(t, nil, "cat <<EOT\n"+text+"\nEOT")

		assert.Equal(t, fixed, !ok, text)
	}
}
