package host

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// ErrDiskText is returned for a df answer that obee does not read.
var ErrDiskText = errors.New("df gave an answer that obee does not read")

const (
	bytesPerKiB     = 1024
	diskHeaderWords = "Filesystem 1024-blocks Used Available Capacity Mounted on"
	diskDataWords   = 6
	diskAvailable   = 3
)

// ParseFreeBytes reads `df -P -k` for one file system and returns the free space in bytes.
func ParseFreeBytes(out string) (uint64, error) {
	lines := model.NonEmptyLines(out)
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != diskHeaderWords {
		return 0, fmt.Errorf("%w: %q", ErrDiskText, model.FirstLine(out))
	}

	words := strings.Fields(lines[1])
	if len(words) != diskDataWords {
		return 0, fmt.Errorf("%w: %q", ErrDiskText, lines[1])
	}

	kib, err := strconv.ParseUint(words[diskAvailable], 10, 64)
	if err != nil || kib > math.MaxUint64/bytesPerKiB {
		return 0, fmt.Errorf("%w: %q", ErrDiskText, lines[1])
	}

	return kib * bytesPerKiB, nil
}
