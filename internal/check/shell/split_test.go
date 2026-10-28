package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"mvdan.cc/sh/v3/syntax"
)

func TestSplitValueKeepsAQuotedStringAsOneWord(t *testing.T) {
	t.Parallel()

	rows := map[string][]string{
		`ssh h $X $X`:         {"ssh", "h", "$X", "$X"},
		`ssh h "rm -rf /x"`:   {"ssh", "h", "rm -rf /x"},
		`ssh h rm -rf "$D"`:   {"ssh", "h", "rm", "-rf", "$D"},
		`ssh h; rm -rf "$D"`:  {"ssh", "h;", "rm", "-rf", `"$D"`},
		`X=1 ssh h`:           {"X=1", "ssh", "h"},
		`ssh h > out`:         {"ssh", "h", ">", "out"},
		`ssh h "$(date)"`:     {"ssh", "h", "$(...)"},
		`ssh h ${A:-b}`:       {"ssh", "h", "${A:-b}"},
		`ssh h "unterminated`: {"ssh", "h", `"unterminated`},
	}

	for value, want := range rows {
		assert.Equal(t, want, splitValue(value), value)
	}
}

func TestValueTextTakesTheInsideOfOneQuotedString(t *testing.T) {
	t.Parallel()

	rows := map[string]string{
		`W='ssh h "x"'`: `ssh h "x"`,
		`W="ssh h 'x'"`: `ssh h 'x'`,
		`W=""`:          "",
		`W=ssh`:         "ssh",
	}

	for text, want := range rows {
		file, ok := parseText(text)
		assert.True(t, ok, text)

		call, isCall := file.Stmts[0].Cmd.(*syntax.CallExpr)
		assert.True(t, isCall, text)
		assert.Equal(t, want, valueText(text, call.Assigns[0].Value), text)
	}
}
