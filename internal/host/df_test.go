package host_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
)

func TestParseFreeBytes_ReadsTheAvailableColumnInBytes(t *testing.T) {
	t.Parallel()

	free, err := host.ParseFreeBytes(hosttest.DF)

	require.NoError(t, err)
	assert.Equal(t, uint64(94371840)*1024, free)
}

func TestParseFreeBytes_RefusesAnAnswerItDoesNotRead(t *testing.T) {
	t.Parallel()

	header := "Filesystem 1024-blocks Used Available Capacity Mounted on\n"

	for name, out := range map[string]string{
		"empty":          "",
		"no header":      "/dev/sda1 1 1 1 1% /\n",
		"a short row":    header + "/dev/sda1 1 1\n",
		"a bad number":   header + "/dev/sda1 1 1 x 1% /\n",
		"a huge number":  header + "/dev/sda1 1 1 18446744073709551615 1% /\n",
		"a second row":   header + "/dev/sda1 1 1 1 1% /\n/dev/sda2 1 1 1 1% /b\n",
		"an error text":  "df: cannot read\n",
		"a changed head": "Filesystem 512-blocks Used Available Capacity Mounted on\n/dev/sda1 1 1 1 1% /\n",
	} {
		_, err := host.ParseFreeBytes(out)

		require.ErrorIs(t, err, host.ErrDiskText, name)
	}
}
