package shell_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
)

// doublingScript sets a chain of variables that each double the one before, so the last one is near the size cap, and uses the last one n times.
func doublingScript(n int) string {
	var script strings.Builder

	script.WriteString("C='ssh h $C'\nV0=\"$C $C\"\n")

	for i := 1; i < 8; i++ {
		fmt.Fprintf(&script, "V%d=\"$V%d $V%d\"\n", i, i-1, i-1)
	}

	script.WriteString(strings.Repeat("$V7 a b\n", n))

	return script.String()
}

// timeOf returns how long a scan of the script takes and its findings.
func timeOf(script string) (time.Duration, int) {
	start := time.Now()
	findings := shell.Scan(script)

	return time.Since(start), len(findings)
}

// The scan reads the value of the last variable once, however many times the script uses it.
// The bound is a multiple of the time of one use, so a slow host or a race build moves both sides.
// Before the fix, 3000 uses cost 3000 times one use; now they cost under 100 times.
func TestScanUsesOfADoublingChainCostOneRead(t *testing.T) {
	t.Parallel()

	const (
		uses       = 3000
		multiple   = 500
		oneUseTime = time.Millisecond
	)

	one, _ := timeOf(doublingScript(1))
	many, findings := timeOf(doublingScript(uses))

	assert.Zero(t, findings)
	assert.Less(t, many, multiple*max(one, oneUseTime))
}
