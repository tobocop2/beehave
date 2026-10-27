package check_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check"
	"github.com/tobocop2/obee/internal/model"
)

const (
	kindOpaque   = "opaque-command"
	kindUnparsed = "unparsed"
	expectHit    = "hit"
	expectClean  = "clean"
	expectNoKind = "nokind"
)

func hasKind(findings []model.Finding, kind string) bool {
	for _, finding := range findings {
		if string(finding.Kind) == kind {
			return true
		}
	}

	return false
}

func TestScanTranscriptCases(t *testing.T) {
	t.Parallel()

	for _, tc := range transcriptCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := check.Scan(tc.command)

			switch tc.expect {
			case expectClean:
				assert.Empty(t, findings, tc.command)
			case expectHit:
				assert.NotEmpty(t, findings, tc.command)

				if tc.kind != "" {
					assert.True(t, hasKind(findings, tc.kind), "%v", findings)
				}
			case expectNoKind:
				assert.False(t, hasKind(findings, tc.kind), "%v", findings)
			}
		})
	}
}

func TestScanAndJobScriptReadEveryCutOfACaseWithoutPanicking(t *testing.T) {
	t.Parallel()

	for _, tc := range transcriptCases() {
		for end := range len(tc.command) + 1 {
			assert.NotPanics(t, func() {
				check.Scan(tc.command[:end])
				check.Scan(tc.command[end:])
				check.JobScript(tc.command[:end])
			}, "%q cut at %d", tc.command, end)
		}
	}
}

func TestScanReadsCommandSubstitutionsAndProcessSubstitutions(t *testing.T) {
	t.Parallel()

	rows := map[string]string{
		"echo $(rm -rf $X)":     "local-delete",
		"x=$(rm -rf $HOME)":     "local-delete",
		"x=$(git add -A)":       "bulk-staging",
		"cat <(git add -A)":     "bulk-staging",
		"echo \"$(git add .)\"": "bulk-staging",
	}

	for command, kind := range rows {
		assert.True(t, hasKind(check.Scan(command), kind), command)
	}
}

func TestScanRefusesALineThatDoesNotParse(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"rm -rf $X ) (", "git add -A )", "echo 'open", "if true; then", "echo a <<"} {
		assert.True(t, hasKind(check.Scan(command), kindUnparsed), command)
	}

	assert.Contains(t, check.Scan("git add -A )"), model.Finding{Kind: model.FindingUnparsed, Text: "git add -A )"})
	assert.False(t, hasKind(check.Scan("echo 'a (b)'"), kindUnparsed))
	assert.False(t, hasKind(check.Scan(`echo "bats failed (rc=$?)"`), kindUnparsed))
}

func TestScanRefusesACommandNoStaticReadCanSee(t *testing.T) {
	t.Parallel()

	rows := []string{
		"g=git; $g add -A", "$(which git) add -A", "env -S 'git add -A'", "env --split-string='rm -rf /x'",
		"find /x -exec sh -c 'rm $0' {} \\;", "find /x -execdir bash -c 'rm $0' {} +", "sudo $X rm $Y",
	}

	for _, command := range rows {
		assert.True(t, hasKind(check.Scan(command), kindOpaque), command)
	}

	for _, command := range []string{"f() { \"$@\"; }", "echo $1", "sudo -S ls", "find /x -exec ls {} \\;", "$BOX_SSH ls", "S=ssh; $S h ls", "sudo $X rm /y"} {
		assert.False(t, hasKind(check.Scan(command), kindOpaque), command)
	}
}

func TestScanSeesThroughBusybox(t *testing.T) {
	t.Parallel()

	assert.True(t, hasKind(check.Scan("busybox git add -A"), "bulk-staging"))
	assert.True(t, hasKind(check.Scan("busybox rm -rf $X"), "local-delete"))
	assert.True(t, hasKind(check.Scan(`ssh h "busybox rm -rf /x"`), "remote-delete"))
}

func TestScanReadsAbbreviatedGitOptionsAndUnknownPathspecs(t *testing.T) {
	t.Parallel()

	hits := []string{
		"git add --upd", "git add --upda", "git add --al", "git add --all", "git add ${X:-.}", "git add ${X:=.}",
		"git commit -m x ${X:-.}", "git add --dry -A",
	}
	for _, command := range hits {
		assert.True(t, hasKind(check.Scan(command), "bulk-staging"), command)
	}

	for _, command := range []string{"git add --dry-run -A", "git add --intent-to-add .", "git add $X", "git add -- file", "git add --", "git commit --al -m x"} {
		assert.False(t, hasKind(check.Scan(command), "bulk-staging"), command)
	}
}

func TestAnEmptyGitIndexFileDoesNotMakeStagingPrivate(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"GIT_INDEX_FILE= git add -A", `GIT_INDEX_FILE="" git add -A`, "env GIT_INDEX_FILE= git add -A", `ssh h "GIT_INDEX_FILE= git add -A"`} {
		assert.True(t, hasKind(check.Scan(command), "bulk-staging"), command)
	}

	for _, command := range []string{"GIT_INDEX_FILE=/tmp/i git add -A", "env GIT_INDEX_FILE=/tmp/i git add -A", "GIT_INDEX_FILE=$X git add -A"} {
		assert.False(t, hasKind(check.Scan(command), "bulk-staging"), command)
	}
}

func TestAGcloudValueFlagDoesNotCountAsTheSshWord(t *testing.T) {
	t.Parallel()

	for flag := range map[string]bool{"--project": true, "--account": true, "--zone": true, "--configuration": true, "--format": true, "--impersonate-service-account": true, "--billing-project": true, "--verbosity": true} {
		assert.Empty(t, check.Scan("gcloud "+flag+" ssh compute instances list -- rm -rf /x"), flag)
	}

	assert.True(t, hasKind(check.Scan("gcloud compute ssh i -- rm -rf /x"), "remote-delete"))
}

func seedFuzz(f *testing.F) {
	f.Helper()

	for _, tc := range transcriptCases() {
		f.Add(tc.command)
	}

	f.Add("()0")
	f.Add("R='ssh h; $R '; $R rm x")
}

func FuzzScan(f *testing.F) {
	seedFuzz(f)

	f.Fuzz(func(_ *testing.T, text string) { check.Scan(text) })
}

func FuzzJobScript(f *testing.F) {
	seedFuzz(f)

	f.Fuzz(func(_ *testing.T, text string) { check.JobScript(text) })
}

func TestScanKeepsAVariableCommandWordThatHidesNoRule(t *testing.T) {
	t.Parallel()

	for _, command := range []string{`"$ROOT/scripts/build.sh" --release`, "$HOME/bin/run.sh", "$CC -o x x.c", "${TOOL} run", `L=echo; $L "bats failed (rc=$?)"`} {
		assert.Empty(t, check.Scan(command), command)
	}
}

func TestScanSeesAWrapperCopiedByADeclaration(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{"export", "local", "declare", "readonly", "typeset"} {
		findings := check.Scan(keyword + " W=ssh\n$W h \"rm -rf /x\"")

		assert.True(t, hasKind(findings, "remote-delete"), keyword)
		assert.False(t, hasKind(findings, kindOpaque), keyword)
	}
}

func TestScanSeesAWrapperFunctionDefinedAfterTheFunctionThatCallsIt(t *testing.T) {
	t.Parallel()

	script := "outer() { inner \"$@\"; }\ninner() { ssh box \"$@\"; }\nouter \"rm -rf /x\""

	assert.True(t, hasKind(check.Scan(script), "remote-delete"))
}

func TestAnUnreadableLineDoesNotHideTheHitOnAnotherLine(t *testing.T) {
	t.Parallel()

	for _, script := range []string{"git add -A &&", "echo 'open\ngit add -A", "git add -A ||", "git add -A |"} {
		findings := check.Scan(script)

		assert.True(t, hasKind(findings, "bulk-staging"), script)
		assert.True(t, hasKind(findings, kindUnparsed), script)
	}
}
