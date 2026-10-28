package hclfile

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, src string) *hclsyntax.Body {
	t.Helper()

	file, diags := hclsyntax.ParseConfig([]byte(src), "p.hcl", hcl.InitialPos)
	require.False(t, diags.HasErrors())

	return syntaxBody(file)
}

func TestDescribeStartsAtTheTopOfTheFileWhenHCLGivesNoPosition(t *testing.T) {
	t.Parallel()

	got := describe(&hcl.Diagnostic{Summary: "Unsupported argument", Detail: `An argument named "x" is not expected here.`}, parse(t, ""))

	assert.Equal(t, 1, got.Line)
	assert.Equal(t, 1, got.Col)
	assert.Equal(t, "unknown key `x`. Remove it.", got.Text)
}

func TestPlainTypeNamesTheExpectedTypeInPlainWords(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Unsuitable value: string required":            "text",
		"Unsuitable value: number required":            "a number",
		"Unsuitable value: bool required":              "true or false",
		"Unsuitable value: list of string required":    "a list",
		"Unsuitable value: set of string required":     "a list",
		"Unsuitable value: tuple required":             "a list",
		"Unsuitable value: something odd is required.": "",
	}

	for detail, want := range cases {
		assert.Equal(t, want, plainType(detail), detail)
	}
}

func TestExplainFixesAWrongTypeWithoutAKnownTypeWithTheValue(t *testing.T) {
	t.Parallel()

	got := explain(&hcl.Diagnostic{Summary: "Unsuitable value type", Detail: "Unsuitable value: odd"}, parse(t, "a = 1\n"), hcl.Pos{Line: 1, Column: 1})

	assert.Equal(t, "`a` has the wrong type. Fix the value.", got)
}

func TestAtPutsTheRangeStartInTheDiagnostic(t *testing.T) {
	t.Parallel()

	got := At(hcl.Range{Filename: "p.hcl", Start: hcl.Pos{Line: 4, Column: 2}}, "text")

	assert.Equal(t, Diagnostic{File: "p.hcl", Line: 4, Col: 2, Text: "text"}, got)
	assert.Equal(t, "p.hcl:4:2: text", got.Error())
}

func TestAttributeAtIsEmptyWhenNoAttributeHoldsThePosition(t *testing.T) {
	t.Parallel()

	body := parse(t, "a = 1\n")

	assert.Equal(t, "a", attributeAt(body, hcl.Pos{Line: 1, Column: 1, Byte: 0}))
	assert.Empty(t, attributeAt(body, hcl.Pos{Line: 9, Column: 1, Byte: 90}))
	assert.Equal(t, "b", attributeAt(parse(t, "blk {\n  b = 1\n}\n"), hcl.Pos{Line: 2, Column: 3, Byte: 8}))
}

func TestFirstQuotedIsEmptyWithoutQuotes(t *testing.T) {
	t.Parallel()

	assert.Empty(t, firstQuoted("no quotes here"))
}

func TestSyntaxBodyIsEmptyForAFileWithAnotherBodyType(t *testing.T) {
	t.Parallel()

	body := syntaxBody(&hcl.File{Body: hcl.EmptyBody()})

	assert.Empty(t, body.Attributes)
	assert.Empty(t, body.Blocks)
}
