package proc_test

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc"
)

func TestTail_KeepsTheLastBytesOnly(t *testing.T) {
	t.Parallel()

	const limit = 4 << 10

	tail := proc.NewTail(limit)
	_, err := tail.Write(bytes.Repeat([]byte("a"), limit))
	require.NoError(t, err)
	_, err = tail.Write([]byte("end"))
	require.NoError(t, err)

	assert.Len(t, tail.Text(), limit)
	assert.True(t, strings.HasSuffix(tail.Text(), "end"))
}

func TestTail_StaysBoundedForAnEndlessOutput(t *testing.T) {
	t.Parallel()

	tail := proc.NewTail(10)

	for range 1000 {
		n, err := tail.Write([]byte("0123456789abc"))
		require.NoError(t, err)
		require.Equal(t, 13, n)
	}

	assert.Equal(t, "3456789abc", tail.Text())
}

func TestTail_NeverStartsInTheMiddleOfACharacter(t *testing.T) {
	t.Parallel()

	const limit = 4 << 10

	tail := proc.NewTail(limit)
	_, err := tail.Write([]byte(strings.Repeat("€", 2000))) // 6000 bytes; the cut at 4096 lands inside a character
	require.NoError(t, err)

	assert.True(t, utf8.ValidString(tail.Text()))
	assert.LessOrEqual(t, len(tail.Text()), limit)
	assert.Greater(t, len(tail.Text()), limit-3)
	assert.True(t, strings.HasPrefix(tail.Text(), "€"))
}
