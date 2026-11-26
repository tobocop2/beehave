package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// FreeSpace returns the free bytes of the file system that holds folder.
type FreeSpace func(ctx context.Context, folder string) (uint64, error)

// DfFree returns the FreeSpace that runs `df -P -k <folder>` with the runner of df and reads the answer with ParseFreeBytes.
func DfFree(df proc.Runner) FreeSpace {
	return func(ctx context.Context, folder string) (uint64, error) {
		result := df.Run(ctx, []string{"-P", "-k", folder})
		if result.ExitCode != 0 {
			return 0, fmt.Errorf("df %s: exit %d: %s", folder, result.ExitCode, model.FirstLine(result.Stderr))
		}

		return ParseFreeBytes(result.Stdout)
	}
}

// LocalFloor is the disk floor of the local machine: the check that a lane runs before it starts, with the floor and the wait of the host block.
// Free reads the free space of a folder. A wait of zero or less does not wait. Poll is the time between two reads while the wait runs.
type LocalFloor struct {
	Free    FreeSpace
	FloorGB int
	Wait    time.Duration
	Poll    time.Duration
}

// Check refuses unless every folder holds at least FloorGB of free space. A folder that does not exist yet is measured at its nearest parent that exists.
// A folder under the floor is read again after each poll until the wait ends, and is then refused with ErrDiskLow. A read that fails is refused at once with ErrDiskRead.
// Each refusal names the folder.
func (l LocalFloor) Check(ctx context.Context, folders ...string) error {
	return waitUntil(ctx, l.Wait, l.Poll, func(ctx context.Context) error {
		for _, folder := range folders {
			if err := l.measure(ctx, folder); err != nil {
				return fmt.Errorf("disk of %s: %w", folder, err)
			}
		}

		return nil
	})
}

// measure reads the free space at the nearest parent of folder that exists, and compares it with the floor.
func (l LocalFloor) measure(ctx context.Context, folder string) error {
	free, err := l.Free(ctx, nearestParent(folder))
	if err != nil {
		return fmt.Errorf("%w: %w", err, ErrDiskRead)
	}

	return belowFloor(model.DiskReading{FreeBytes: free, Readable: true}, l.FloorGB)
}

// nearestParent returns folder when it exists, else the nearest folder above it that does. The root of the file system always exists.
func nearestParent(folder string) string {
	for {
		if _, err := os.Stat(folder); err == nil {
			return folder
		}

		folder = filepath.Dir(folder)
	}
}
