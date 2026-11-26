package host_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

var errNoDf = errors.New("df failed")

// freeOf returns a free-space function that answers free bytes for every folder and records the folders it was asked about.
func freeOf(free uint64, asked *[]string) host.FreeSpace {
	return func(_ context.Context, folder string) (uint64, error) {
		*asked = append(*asked, folder)

		return free, nil
	}
}

func TestLocalFloor_AFolderUnderTheFloorIsRefusedWithItsFreeAndNeededSpace(t *testing.T) {
	t.Parallel()

	var asked []string

	folder := t.TempDir()
	floor := host.LocalFloor{Free: freeOf(3*gb, &asked), FloorGB: 50}

	err := floor.Check(t.Context(), folder)

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Contains(t, err.Error(), "3 GB free, floor 50 GB")
	assert.Contains(t, err.Error(), folder)
	assert.Contains(t, err.Error(), "free-space floor")
}

func TestLocalFloor_AFolderAtTheFloorPasses(t *testing.T) {
	t.Parallel()

	var asked []string

	folder := t.TempDir()

	require.NoError(t, host.LocalFloor{Free: freeOf(50*gb, &asked), FloorGB: 50}.Check(t.Context(), folder))
	assert.Equal(t, []string{folder}, asked, "the folder that was measured")
}

func TestLocalFloor_AFolderWhoseSpaceCannotBeReadIsRefusedAtOnce(t *testing.T) {
	t.Parallel()

	calls := 0
	floor := host.LocalFloor{
		Free: func(context.Context, string) (uint64, error) {
			calls++

			return 0, errNoDf
		},
		FloorGB: 50, Wait: time.Hour, Poll: time.Millisecond,
	}
	folder := t.TempDir()

	err := floor.Check(t.Context(), folder)

	require.ErrorIs(t, err, host.ErrDiskRead)
	require.ErrorIs(t, err, errNoDf)
	assert.Contains(t, err.Error(), folder)
	assert.Equal(t, 1, calls, "a read that fails is not read again")
}

func TestLocalFloor_WaitsForSpaceForTheWaitOfTheProject(t *testing.T) {
	t.Parallel()

	reads := 0
	free := func(context.Context, string) (uint64, error) {
		reads++
		if reads < 3 {
			return 1 * gb, nil
		}

		return 60 * gb, nil
	}

	require.NoError(t, host.LocalFloor{Free: free, FloorGB: 50, Wait: time.Minute, Poll: time.Millisecond}.Check(t.Context(), t.TempDir()))
	assert.Equal(t, 3, reads, "two reads under the floor, then one above it")
}

func TestLocalFloor_AFolderStillUnderTheFloorWhenTheWaitEndsIsRefused(t *testing.T) {
	t.Parallel()

	var asked []string

	err := host.LocalFloor{Free: freeOf(1*gb, &asked), FloorGB: 50, Wait: 20 * time.Millisecond, Poll: time.Millisecond}.Check(t.Context(), t.TempDir())

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Greater(t, len(asked), 1, "the folder is read again while the wait runs")
}

func TestLocalFloor_AWaitOfZeroRefusesAtOnce(t *testing.T) {
	t.Parallel()

	var asked []string

	err := host.LocalFloor{Free: freeOf(1*gb, &asked), FloorGB: 50, Poll: time.Millisecond}.Check(t.Context(), t.TempDir())

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Len(t, asked, 1, "no wait means one read")
}

func TestLocalFloor_ACanceledWaitReturnsTheCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var asked []string

	err := host.LocalFloor{Free: freeOf(1*gb, &asked), FloorGB: 50, Wait: time.Hour, Poll: time.Hour}.Check(ctx, t.TempDir())

	require.ErrorIs(t, err, context.Canceled)
}

func TestLocalFloor_AFolderThatDoesNotExistYetIsMeasuredAtItsNearestParent(t *testing.T) {
	t.Parallel()

	var asked []string

	parent := t.TempDir()
	missing := filepath.Join(parent, "trees", "p", "item")

	require.NoError(t, host.LocalFloor{Free: freeOf(60*gb, &asked), FloorGB: 50}.Check(t.Context(), missing))
	assert.Equal(t, []string{parent}, asked, "the nearest folder that exists")

	asked = nil
	err := host.LocalFloor{Free: freeOf(1*gb, &asked), FloorGB: 50}.Check(t.Context(), missing)

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Contains(t, err.Error(), missing, "the refusal names the folder that was asked for")
}

func TestLocalFloor_AFolderBelowAFileIsMeasuredAtTheFile(t *testing.T) {
	t.Parallel()

	var asked []string

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	require.NoError(t, host.LocalFloor{Free: freeOf(60*gb, &asked), FloorGB: 50}.Check(t.Context(), filepath.Join(file, "inside")))
	assert.Equal(t, []string{file}, asked)
}

func TestLocalFloor_EveryFolderMustHoldTheFloor(t *testing.T) {
	t.Parallel()

	roomy, tight := t.TempDir(), t.TempDir()
	free := func(_ context.Context, folder string) (uint64, error) {
		if folder == tight {
			return 1 * gb, nil
		}

		return 60 * gb, nil
	}
	floor := host.LocalFloor{Free: free, FloorGB: 50}

	require.NoError(t, floor.Check(t.Context(), roomy))
	require.NoError(t, floor.Check(t.Context()), "no folder is no refusal")

	for name, folders := range map[string][]string{"tight_last": {roomy, tight}, "tight_first": {tight, roomy}} {
		err := floor.Check(t.Context(), folders...)

		require.ErrorIs(t, err, host.ErrDiskLow, name)
		assert.Contains(t, err.Error(), tight, name)
	}
}

func TestDfFree_ReadsTheFreeBytesOfDfForTheFolder(t *testing.T) {
	t.Parallel()

	df := proctest.Always(model.CommandResult{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk1 100 40 60 40% /\n"})

	free, err := host.DfFree(df)(t.Context(), "/srv/trees")

	require.NoError(t, err)
	assert.Equal(t, uint64(60*1024), free)
	assert.Equal(t, [][]string{{"-P", "-k", "/srv/trees"}}, df.Calls)
}

func TestDfFree_RefusesADfThatFailsOrPrintsText(t *testing.T) {
	t.Parallel()

	_, err := host.DfFree(proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "no such file"}))(t.Context(), "/srv")
	require.ErrorContains(t, err, "no such file")

	_, err = host.DfFree(proctest.Always(model.CommandResult{Stdout: "not a table"}))(t.Context(), "/srv")
	require.ErrorIs(t, err, host.ErrDiskText)
}
