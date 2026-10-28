package check

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"mvdan.cc/sh/v3/expand"

	"github.com/tobocop2/obee/internal/model"
)

func TestSSHOptionWidth(t *testing.T) {
	t.Parallel()

	for word, want := range map[string]int{"-p": 2, "-tp": 2, "-p22": 1, "-tt": 1, "-4o": 2} {
		assert.Equal(t, want, sshOptionWidth(word), word)
	}
}

func TestIsWrapperWord(t *testing.T) {
	t.Parallel()

	for word, want := range map[string]bool{
		"$BOX_SSH": true, "${BOX_SSH}": true, "$box": true, "$SSH_OPTS": false, "$BOX_SSH_OPTS": false, "$(x)": false,
		"box.sh": true, "/x/host_ssh.sh": true, "sandbox.sh": false, "inbox.sh": false, "box": false,
	} {
		assert.Equal(t, want, isWrapperWord(word), word)
	}
}

func TestLiteralAbs(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"/home/x/literal": true, "/": false, "relative": false, "/x/../y": false, "/x/$HOME": false, "/x/{a,b}": false, "/x/*": false, "~/x": false,
	} {
		assert.Equal(t, want, literalAbs(name), name)
	}
}

func TestDeleteHit(t *testing.T) {
	t.Parallel()

	rows := []struct {
		head []string
		want bool
	}{
		{[]string{"ls"}, false},
		{nil, false},
		{[]string{"rm", "x"}, true},
		{[]string{"unlink", "x"}, true},
		{[]string{"find", "/x", "-delete"}, true},
		{[]string{"find", "/x", "-exec", "rm", "{}"}, true},
		{[]string{"find", "/x", "-exec", "echo"}, false},
		{[]string{"find", "/x", "-exec"}, false},
		{[]string{"find", "/x", "-name", "y"}, false},
	}

	for _, row := range rows {
		assert.Equal(t, row.want, deleteHit(row.head), row.head)
	}
}

func TestDeletePathsOfFindStopAtTheFirstTest(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"/a", "/b"}, deletePaths([]string{"find", "/a", "/b", "-name", "x", "-delete"}))
	assert.Equal(t, []string{"/a", "-"}, deletePaths([]string{"rm", "-rf", "/a", "-"}))
}

func TestHeadOfKeepsAShellThatHasNoScript(t *testing.T) {
	t.Parallel()

	rows := []struct{ words, head []string }{
		{[]string{"bash"}, []string{"bash"}},
		{[]string{"bash", "-s"}, []string{"bash", "-s"}},
		{[]string{"bash", "--", "./box.sh", "x"}, []string{"./box.sh", "x"}},
		{[]string{"bash", "-o", "pipefail", "./box.sh"}, []string{"./box.sh"}},
		{[]string{"bash", "-lc", "git add -A"}, []string{"bash", "-lc", "git add -A"}},
		{[]string{"bash", "-x"}, []string{"bash", "-x"}},
		{[]string{"sudo"}, []string{}},
		{[]string{"timeout"}, []string{}},
		{[]string{"timeout", "-k", "5", "100", "git", "add"}, []string{"git", "add"}},
		{[]string{"env", "A=1", "-u", "B", "ls"}, []string{"-u", "B", "ls"}},
	}

	for _, row := range rows {
		head, _ := headOf(row.words)
		assert.Equal(t, row.head, head, row.words)
	}
}

func TestEachCommandIsReportedOnce(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]model.Finding{{Kind: model.FindingBulkStaging, Text: "git add -A"}},
		Scan("git add -A; git add -A"))
}

func TestCommitClusterWithoutAValueOrAllIsNotAHit(t *testing.T) {
	t.Parallel()

	assert.Empty(t, Scan("git commit -v"))
	assert.Empty(t, Scan("git commit -sv x"))
}

func TestACommandThatDropsToNothingIsNotAHit(t *testing.T) {
	t.Parallel()

	assert.Empty(t, Scan("sudo"))
	assert.Empty(t, Scan("bash -c"))
	assert.Empty(t, Scan("X=1"))
}

func TestACommandSubstitutionIsRead(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, Scan("echo $(git add -A)"))
	assert.NotEmpty(t, Scan("cat <(git add -A)"))
	assert.NotEmpty(t, Scan("rm /x/$(date)"))
}

func TestAPathWithAVariableOrABraceListIsNotLiteral(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, Scan("rm /x/$Y"))
	assert.NotEmpty(t, Scan("rm /x/{a,b}"))
	assert.Empty(t, Scan("rm /x/a"))
}

func TestAPipeIntoARemoteShellReadsTheEchoBeforeIt(t *testing.T) {
	t.Parallel()

	findings := Scan(`echo "rm -rf /x" | ssh h bash`)

	assert.Contains(t, findings, model.Finding{Kind: model.FindingRemoteDelete, Text: "rm -rf /x"})
}

func TestResolverListsNoVariable(t *testing.T) {
	t.Parallel()

	resolver{}.Each(func(string, expand.Variable) bool { return false })
	assert.False(t, resolver{}.Get("IFS").IsSet())
}

func TestAFragmentThatDoesNotParseIsReturnedAsUnreadable(t *testing.T) {
	t.Parallel()

	units, bad := parseUnits("echo ok\nif true; then\n  echo 'open")

	assert.Len(t, units, 1)
	assert.Equal(t, []string{"if true; then", "echo 'open"}, bad)
}

func TestAWrapperFunctionWithNoCommandAndAHeredocWithNoFileAreNotRefused(t *testing.T) {
	t.Parallel()

	assert.Empty(t, JobScript("f() { timeout 5; }\nf() { sudo -E \"$@\"; }\nf() { tee -a <<EOF\nx\nEOF\n}"))
}

func TestAWrapperVariableInsideALongerWordIsExpanded(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, Scan("S=ssh\nsh -c \"$S h rm /x\""))
	assert.Empty(t, Scan("S=ssh\necho a$S"))
}

func TestOnlyThePipeSegmentBeforeASshIsItsInput(t *testing.T) {
	t.Parallel()

	remote := model.Finding{Kind: model.FindingRemoteDelete, Text: "rm -rf /x"}

	assert.NotContains(t, Scan(`echo "rm -rf /x" | cat | ssh h bash`), remote)
	assert.Contains(t, Scan(`echo "rm -rf /x" |& ssh h bash`), remote)
}

func TestFindPathsWithoutATestAreAllPaths(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"/x"}, deletePaths([]string{"find", "/x"}))
}

func TestAnyNumberOfWrappersIsDropped(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, JobScript(strings.Repeat("exec ", 21)+"pytest"))
	assert.NotEmpty(t, JobScript(strings.Repeat("nice ", 200)+"pytest"))
}

func TestHeadOfDropsEachWrapperAndItsValuedOptions(t *testing.T) {
	t.Parallel()

	rows := [][]string{
		{"sudo", "-u", "me", "x"},
		{"env", "-u", "A", "x"},
		{"nice", "-n", "5", "x"},
		{"timeout", "-k", "5", "100", "x"},
		{"gtimeout", "-s", "KILL", "100", "x"},
		{"xargs", "-n", "1", "x"},
		{"sshpass", "-p", "pw", "x"},
		{"stdbuf", "-o", "L", "x"},
		{"ionice", "-c", "3", "x"},
		{"busybox", "x"},
		{"nohup", "x"},
		{"time", "x"},
		{"exec", "x"},
		{"command", "x"},
		{"setsid", "x"},
	}

	for _, words := range rows {
		head, prefix := headOf(words)
		assert.Equal(t, []string{"x"}, head, words)
		assert.Len(t, prefix, len(words)-1, words)
	}
}

func TestUvRunsSkipsOptionsAndTheirValues(t *testing.T) {
	t.Parallel()

	rows := map[string]bool{
		"run pytest": true, "tool run pytest": true, "-q run pytest": true, "--directory x run pytest": true, "-q tool run pytest": true,
		"pip install pytest": false, "sync --extra pytest": false, "tool install pytest": false, "": false, "-q": false, "--directory x sync": false,
	}

	for text, want := range rows {
		assert.Equal(t, want, uvRuns(strings.Fields(text)), text)
	}
}

func TestUvRunsStaysLinearInTheNumberOfOptions(t *testing.T) {
	t.Parallel()

	words := strings.Fields(strings.Repeat("-a x ", 5000) + "sync")

	assert.False(t, uvRuns(words))
	assert.True(t, uvRuns(append(words[:len(words)-1:len(words)-1], "run")))
}

// chainScript sets n variables, each naming the one before it twice, and runs the last one with a delete.
func chainScript(n int) string {
	var script strings.Builder

	script.WriteString("V0='ssh h x'\n")

	for i := 1; i < n; i++ {
		fmt.Fprintf(&script, "V%d='ssh h $V%d $V%d'\n", i, i-1, i-1)
	}

	fmt.Fprintf(&script, "$V%d rm -rf \"$D\"\n", n-1)

	return script.String()
}

func TestAChainOfAssignmentsWorksOutEachValueOnce(t *testing.T) {
	t.Parallel()

	for _, n := range []int{20, 40} {
		script := chainScript(n)
		s := newScanner()
		s.prepass(script)
		s.scan(script, false)

		assert.Equal(t, n, s.resolutions, "assignments %d", n)
		assert.NotEmpty(t, dedupe(s.findings), "assignments %d", n)
	}
}

func TestAValueLongerThanTheCapStaysAsNameText(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["BIG"] = "ssh h " + strings.Repeat("x", maxResolvedText)
	s.vars["USER"] = "ssh h $BIG"

	_, held := s.value("BIG")
	assert.False(t, held)

	text, held := s.value("USER")
	assert.True(t, held)
	assert.Equal(t, "ssh h $BIG", text)
}

func TestRedefiningAVariableFromItselfKeepsTheOldValueUntilTheCap(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["W"] = "sudo"

	assert.Equal(t, "sudo sudo $Z", s.redefine("W", "$W ${W} $Z"))
	assert.Equal(t, "$Z", s.redefine("Z", "$Z"))

	s.vars["W"] = strings.Repeat("x", maxResolvedText)
	assert.Equal(t, "$W $W", s.redefine("W", "$W $W"))
}

func TestRedefiningAVariableStopsAtTheCapExactly(t *testing.T) {
	t.Parallel()

	atCap := strings.Repeat("x", maxResolvedText)
	pastCap := strings.Repeat("x", maxResolvedText+1)

	s := newScanner()
	s.vars["W"] = atCap
	assert.Equal(t, atCap, s.redefine("W", "$W"))

	s.vars["W"] = pastCap
	assert.Equal(t, "$W", s.redefine("W", "$W"))
}

func TestAValueBuiltFromAnOpenEntryIsKeptForOneLookupOnly(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["A"] = "ssh h; $B"
	s.vars["B"] = "ssh h; $A"
	s.vars["C"] = "$B"
	s.vars["D"] = "$B $C"

	_, held := s.value("D")
	assert.True(t, held)
	assert.Empty(t, s.memo)
}

func TestALookupThatRunsOutOfBudgetLeavesTheNameAsText(t *testing.T) {
	t.Parallel()

	s := newScanner()

	for i := range maxResolutions + 5 {
		s.vars[fmt.Sprintf("V%d", i)] = fmt.Sprintf("ssh h $V%d $W", i+1)
	}

	s.vars["W"] = "$V0"

	_, held := s.value("V0")
	assert.False(t, held)
	assert.LessOrEqual(t, s.resolutions, maxResolutions)
}

// cycleScript sets a variable that names itself, a tower of variables that each name the one below twice, and n lines that use the top.
func cycleScript(levels, lines int) string {
	var script strings.Builder

	script.WriteString("C='ssh h $C'\nV0='ssh h $C'\n")

	for i := 1; i < levels; i++ {
		fmt.Fprintf(&script, "V%d='ssh h $V%d $V%d'\n", i, i-1, i-1)
	}

	for range lines {
		fmt.Fprintf(&script, "ssh h '$V%d $V%d rm /x'\n", levels-1, levels-1)
	}

	return script.String()
}

func TestAVariableThatReachesACycleCostsTheSameWorkOnEveryUse(t *testing.T) {
	t.Parallel()

	const levels = 8

	// One lookup of the top variable works out each of the tower's values once: the cycle variable and the levels.
	const perUse = levels + 1

	script := cycleScript(levels, 5000)
	assert.Greater(t, len(script), 110_000)

	s := newScanner()
	s.prepass(script)

	for range 10_000 {
		_, held := s.value("V7")
		assert.True(t, held)
	}

	assert.Equal(t, 10_000*perUse, s.resolutions, "work per use must not grow with the number of uses")

	// A scan reads the text a use expands to, so its work per line is a constant too.
	perLine := func(lines int) int {
		scanned := newScanner()
		text := cycleScript(levels, lines)
		scanned.prepass(text)
		scanned.scan(text, false)

		return scanned.resolutions
	}

	assert.Equal(t, 2*perLine(10), perLine(20))
}

func TestAnOpenValueIsKeptForOneLookupOnly(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["A"] = "ssh h; $B"
	s.vars["B"] = "ssh h; $A"

	text, held := s.value("A")
	assert.True(t, held)
	assert.Equal(t, "ssh h; ssh h; $A", text)

	text, held = s.value("B")
	assert.True(t, held)
	assert.Equal(t, "ssh h; ssh h; $B", text)
	assert.Empty(t, s.memo)
}

func TestAVariableStaysReadableOnTheSecondUseOfAnOpenValue(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["A"] = "ssh h; $B"
	s.vars["B"] = "ssh h; $A"

	for range 2 {
		_, held := s.value("A")
		assert.True(t, held)
		assert.Empty(t, s.resolving)
	}

	assert.NotEmpty(t, Scan(`A='ssh h; $B'; B='ssh h rm -rf "$D"; $A'; $A echo; $A echo`))
}

func TestTheSizeCapAndTheResolutionCapAreExactBounds(t *testing.T) {
	t.Parallel()

	s := newScanner()
	s.vars["AT"] = strings.Repeat("x", maxResolvedText)
	s.vars["PAST"] = strings.Repeat("x", maxResolvedText+1)

	_, held := s.value("AT")
	assert.True(t, held)

	_, held = s.value("PAST")
	assert.False(t, held)

	for _, n := range []int{maxResolutions, maxResolutions + 1} {
		s := newScanner()

		for i := range n {
			s.vars[fmt.Sprintf("V%d", i)] = fmt.Sprintf("$V%d", i+1)
		}

		s.vars[fmt.Sprintf("V%d", n-1)] = "ssh h"

		_, held := s.value("V0")
		assert.Equal(t, n == maxResolutions, held, "values %d", n)
	}
}
