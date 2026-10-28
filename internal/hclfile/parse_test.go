package hclfile_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/hclfile"
)

func TestParseReadsThroughTheFileSystem(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"a/one.hcl": {Data: []byte("name = \"x\"\nitem \"b\" {\n}\n")}}

	body, err := hclfile.Parse(fsys, "a/one.hcl")

	require.NoError(t, err)
	assert.Contains(t, body.Attributes, "name")
	require.Len(t, body.Blocks, 1)
	assert.Equal(t, "item", body.Blocks[0].Type)
}

func TestParseReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()

	_, err := hclfile.Parse(fstest.MapFS{}, "missing.hcl")

	found := diagnostics(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "missing.hcl", found[0].File)
	assert.Contains(t, found[0].Text, "cannot read the file")
}

func TestParseReportsASyntaxErrorWithItsPosition(t *testing.T) {
	t.Parallel()

	_, err := hclfile.Parse(fstest.MapFS{"bad.hcl": {Data: []byte("name = \n")}}, "bad.hcl")

	found := diagnostics(t, err)
	require.NotEmpty(t, found)
	assert.Equal(t, "bad.hcl", found[0].File)
	assert.Equal(t, 1, found[0].Line)
	assert.Contains(t, found[0].Text, "Fix the syntax of the file.")
}

func TestDecodeBodyStrictlyDecodesOneBlockBody(t *testing.T) {
	t.Parallel()

	body, err := hclfile.Parse(fstest.MapFS{"b.hcl": {Data: []byte("box {\n  name = \"n\"\n  extra = 1\n}\n")}}, "b.hcl")
	require.NoError(t, err)

	var got sample

	err = hclfile.DecodeBody(body.Blocks[0].Body, &got)

	found := diagnostics(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, 3, found[0].Line)
	assert.Contains(t, found[0].Text, "extra")
}

func TestDecodeBodyFillsTheTarget(t *testing.T) {
	t.Parallel()

	body, err := hclfile.Parse(fstest.MapFS{"b.hcl": {Data: []byte("box {\n  name = \"n\"\n}\n")}}, "b.hcl")
	require.NoError(t, err)

	var got sample

	require.NoError(t, hclfile.DecodeBody(body.Blocks[0].Body, &got))
	assert.Equal(t, "n", *got.Name)
}
