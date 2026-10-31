package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

const kindZsh = "zsh-expansion"

func TestScan_ARefusesEachZshOnlyExpansionOutsideSingleQuotes(t *testing.T) {
	t.Parallel()

	commands := map[string]string{
		"flags in a loop":      "names=$(cut -f1 t); for n in ${(f)names}; do echo \"$n\"; done",
		"word split":           "for n in ${=list}; do :; done",
		"glob":                 "ls ${~pat}",
		"array":                "ls ${^arr}",
		"flags in double":      "echo \"${(s:,:)csv}\"",
		"word split in double": "echo \"${=x}\"",
		"after a closed quote": "echo 'a' ${=x}",
		"after an escape":      "echo \\' ${=x}",
		"second line":          "echo ok\nls ${~pat}",
		"after a here-string":  "cat <<< x; ls ${^arr}",
		"after a heredoc":      "cat <<'EOF'\nbody\nEOF\nls ${=x}",
		"quote across lines":   "echo \"a\nb\" ${=x}",
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := shell.Scan(command)

			assert.True(t, hasKind(findings, kindZsh), "%v", findings)
		})
	}
}

func TestScan_AZshExpansionFindingSaysWhatToWriteInPosix(t *testing.T) {
	t.Parallel()

	for form, want := range map[string]string{
		"echo ${(f)x}": "${(f)x} is a zsh-only flag; split with `tr`, `cut` or `read` in a pipe instead",
		"echo ${=x}":   "${=x} is zsh-only word splitting; in POSIX an unquoted $name splits on its own",
		"echo ${~x}":   "${~x} is zsh-only globbing of a value; in POSIX an unquoted $name globs on its own",
		"echo ${^x}":   "${^x} is a zsh-only array expansion; loop over the words in POSIX instead",
	} {
		findings := shell.ScanFor(form, []model.FindingKind{model.FindingZshExpansion})

		assert.Equal(t, []model.Finding{{Kind: model.FindingZshExpansion, Text: want}}, findings, form)
	}
}

func TestScan_AZshExpansionWithNoClosingBraceIsCut(t *testing.T) {
	t.Parallel()

	findings := shell.ScanFor("echo ${=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []model.FindingKind{model.FindingZshExpansion})

	assert.Len(t, findings, 1)
	assert.Contains(t, findings[0].Text, "${=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa is zsh-only")
}

func TestScan_AZshExpansionInSingleQuotesOrAHeredocBodyPasses(t *testing.T) {
	t.Parallel()

	commands := map[string]string{
		"single quotes":       "grep -n '${(f)' notes.md",
		"single across lines": "echo '${=x}\n${~y}'",
		"quoted heredoc":      "cat >> /tmp/l.md <<'EOF'\nno ${(f)x} here\nEOF",
		"indented heredoc":    "cat <<-EOF\n\tno ${=x} here\n\tEOF\nls",
		"bash forms":          "echo \"${#list} ${x:-y} ${x/a/b}\"",
		"plain variable":      "echo ${x} ${x#a} ${x%b}",
		"dollar at the end":   "echo ${",
		"escaped dollar":      "echo \\${=x}",
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanFor(command, []model.FindingKind{model.FindingZshExpansion})

			assert.Empty(t, findings, command)
		})
	}
}

func TestScan_AZshFormAfterAHeredocLookalikeIsRefused(t *testing.T) {
	t.Parallel()

	commands := map[string]string{
		"shift in arithmetic": "echo $((1 << 2))\nls ${=x}",
		"in double quotes":    "echo \"a <<EOF\"\nls ${=x}",
		"in single quotes":    "echo 'a <<EOF'\nls ${=x}",
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanFor(command, []model.FindingKind{model.FindingZshExpansion})

			assert.Len(t, findings, 1, command)
		})
	}
}

func TestScan_AnApostropheInACommentDoesNotOpenAQuote(t *testing.T) {
	t.Parallel()

	findings := shell.ScanFor("echo a # it's\nls ${=x}", []model.FindingKind{model.FindingZshExpansion})

	assert.Len(t, findings, 1)
}

func TestScan_ATextThatLooksLikeAStandInNameIsNotAZshForm(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"echo ${OBEEZSH7_x}", "echo ${=x} ${OBEEZSH7_x}", "echo ${OBEEZSH99999999999999999999_x}"} {
		findings := shell.ScanFor(command, []model.FindingKind{model.FindingZshExpansion})

		assert.LessOrEqual(t, len(findings), 1, command)
	}
}

func TestScan_AFlagGroupThatSwallowsASecondFormStillReports(t *testing.T) {
	t.Parallel()

	findings := shell.Scan("echo ${(${=x)")

	assert.NotEmpty(t, findings)
}

func TestScan_AZshFormInACommentIsRefused(t *testing.T) {
	t.Parallel()

	findings := shell.ScanFor("echo a # ${=x}", []model.FindingKind{model.FindingZshExpansion})

	assert.Len(t, findings, 1)
}
