package hclfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/hclfile"
)

type inner struct {
	Size *int `hcl:"size,optional"`
}

type item struct {
	Label string `hcl:"label,label"`
	Port  *int   `hcl:"port,optional"`
}

type sample struct {
	Name  *string   `hcl:"name,optional"`
	Tags  *[]string `hcl:"tags,optional"`
	Limit *int      `hcl:"limit,optional"`
	Inner *inner    `hcl:"inner,block"`
	Items []item    `hcl:"item,block"`
}

type needy struct {
	Name string `hcl:"name"`
}

func write(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sample.hcl")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func diagnostics(t *testing.T, err error) []hclfile.Diagnostic {
	t.Helper()

	var found []hclfile.Diagnostic

	for _, e := range flatten(err) {
		var d hclfile.Diagnostic

		require.ErrorAs(t, e, &d)

		found = append(found, d)
	}

	return found
}

func flatten(err error) []error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return joined.Unwrap()
	}

	return []error{err}
}

func TestDecodeRoundTripsEveryKind(t *testing.T) {
	t.Parallel()

	path := write(t, `name = "a"
tags  = ["x", "y"]
limit = 3
inner {
  size = 4
}
item "one" {
  port = 8080
}
item "two" {}
`)

	var got sample

	require.NoError(t, hclfile.Decode(path, &got))
	assert.Equal(t, "a", *got.Name)
	assert.Equal(t, []string{"x", "y"}, *got.Tags)
	assert.Equal(t, 3, *got.Limit)
	assert.Equal(t, 4, *got.Inner.Size)
	require.Len(t, got.Items, 2)
	assert.Equal(t, "one", got.Items[0].Label)
	assert.Equal(t, 8080, *got.Items[0].Port)
	assert.Nil(t, got.Items[1].Port)
}

func TestDecodeKeepsAbsentKeysNil(t *testing.T) {
	t.Parallel()

	var got sample

	require.NoError(t, hclfile.Decode(write(t, ""), &got))
	assert.Nil(t, got.Name)
	assert.Nil(t, got.Tags)
	assert.Nil(t, got.Inner)
	assert.Empty(t, got.Items)
}

func TestDecodeUnknownKeyGivesPositionAndValidNames(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		line int
		col  int
		want string
	}{
		"top level key":    {"name = \"a\"\nlimt = 1\n", 2, 1, "unknown key `limt`. Remove it, or use `limit`."},
		"key in a block":   {"inner {\n  sise = 1\n}\n", 2, 3, "unknown key `sise`. Remove it, or use `size`."},
		"key in a labeled": {"item \"a\" {\n  host = 1\n}\n", 2, 3, "unknown key `host`. Remove it, or use `port`."},
		"key with no near": {"name = \"a\"\nzzzzzzzz = 1\n", 2, 1, "unknown key `zzzzzzzz`. Remove it."},
		"unknown block":    {"outer {\n}\n", 1, 1, "unknown block `outer`. Remove it."},
		"block in a block": {"inner {\n  deep {\n  }\n}\n", 2, 3, "unknown block `deep`. Remove it."},
		"key as a block":   {"name {\n}\n", 1, 1, "`name` is a key, not a block. Write `name = value`."},
		"block as a key":   {"inner = 1\n", 1, 1, "`inner` is a block, not a key. Write `inner { ... }`."},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := write(t, tc.text)

			var got sample

			found := diagnostics(t, hclfile.Decode(path, &got))
			require.Len(t, found, 1)
			assert.Equal(t, path, found[0].File)
			assert.Equal(t, tc.line, found[0].Line)
			assert.Equal(t, tc.col, found[0].Col)
			assert.Equal(t, tc.want, found[0].Text)
		})
	}
}

func TestDecodeWrongTypeGivesPositionAndKey(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		line int
		col  int
		key  string
		kind string
	}{
		"string for number": {"limit = \"x\"\n", 1, 10, "limit", "a number"},
		"number for list":   {"name = \"a\"\ntags = 1\n", 2, 8, "tags", "a list"},
		"in a block":        {"inner {\n  size = \"big\"\n}\n", 2, 11, "size", "a number"},
		"list for text":     {"name = [\"a\"]\n", 1, 8, "name", "text"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := write(t, tc.text)

			var got sample

			found := diagnostics(t, hclfile.Decode(path, &got))
			require.Len(t, found, 1)
			assert.Equal(t, tc.line, found[0].Line)
			assert.Equal(t, tc.col, found[0].Col)
			assert.Contains(t, found[0].Text, "`"+tc.key+"` has the wrong type")
			assert.Contains(t, found[0].Text, ". Use "+tc.kind+".")
			assert.NotContains(t, found[0].Text, "required")
		})
	}
}

func TestDecodeRepeatedKeyGivesPosition(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		line int
		want string
	}{
		"repeated key":   {"name = \"a\"\nname = \"b\"\n", 2, "`name` is set twice. Keep one."},
		"repeated block": {"inner {}\ninner {}\n", 2, "block `inner` appears twice. Keep one."},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got sample

			found := diagnostics(t, hclfile.Decode(write(t, tc.text), &got))
			require.Len(t, found, 1)
			assert.Equal(t, tc.line, found[0].Line)
			assert.Equal(t, tc.want, found[0].Text)
		})
	}
}

func TestDecodeSyntaxErrorGivesPosition(t *testing.T) {
	t.Parallel()

	var got sample

	found := diagnostics(t, hclfile.Decode(write(t, "name = \n"), &got))
	require.NotEmpty(t, found)
	assert.Equal(t, 1, found[0].Line)
	assert.Contains(t, found[0].Text, "Fix the syntax")
}

func TestDecodeMissingRequiredKeyGivesPosition(t *testing.T) {
	t.Parallel()

	var got needy

	found := diagnostics(t, hclfile.Decode(write(t, ""), &got))
	require.Len(t, found, 1)
	assert.Equal(t, 1, found[0].Line)
	assert.Contains(t, found[0].Text, "`name` is required. Add it.")
}

func TestDecodeReportsEveryProblem(t *testing.T) {
	t.Parallel()

	var got sample

	found := diagnostics(t, hclfile.Decode(write(t, "limit = \"x\"\nbogus = 1\n"), &got))
	assert.Len(t, found, 2)
}

func TestDecodeUnreadableFileNamesPathAndFix(t *testing.T) {
	t.Parallel()

	var got sample

	path := filepath.Join(t.TempDir(), "missing.hcl")
	err := hclfile.Decode(path, &got)

	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), path+": cannot read the file"))
	assert.Contains(t, err.Error(), "Check that the file exists")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, 0, diagnostics(t, err)[0].Line)
}

func TestDiagnosticRendersFileLineColumnText(t *testing.T) {
	t.Parallel()

	d := hclfile.Diagnostic{File: "a.hcl", Line: 3, Col: 4, Text: "bad."}

	assert.Equal(t, "a.hcl:3:4: bad.", d.Error())
}
