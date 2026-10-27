package check

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type chainShape struct{ name, term, head, join, tail string }

// readChain builds a chain of the given number of terms and reads it. It returns the bytes kept, the length of the job and the time to read.
func readChain(t *testing.T, shape chainShape, terms int) (kept, size int, took time.Duration) {
	t.Helper()

	list := repeat(shape.term, terms)
	if shape.head != "" {
		list[0] = shape.head
	}

	if shape.tail != "" {
		list[terms-1] = shape.tail
	}

	job := strings.Join(list, shape.join) + "\n"
	units, bad := parseUnits(job)
	begin := time.Now()

	for _, call := range readGateScript(units).calls {
		kept += len(call)
	}

	assert.Empty(t, bad)

	return kept, len(job), time.Since(begin)
}

// TestReadGateScript_AChainKeepsNoMoreBytesThanTheJobAndIsReadInLinearTime bounds two costs of the step rules. A chain of n terms must keep about one copy of its text, not n copies. Reading a chain of 4n terms must take less than 8 times as long as reading n terms (linear is 4, quadratic is 16), where a read of n terms counts as at least the floor. Without the race detector the n-term read is under the floor, so the check is then an absolute bound of 8 times the floor. The shapes run one after the other, so that they share no CPU.
func TestReadGateScript_AChainKeepsNoMoreBytesThanTheJobAndIsReadInLinearTime(t *testing.T) {
	t.Parallel()

	const (
		terms = 5000
		grow  = 4
		// The floor keeps a fast read from failing the ratio on timer noise.
		floor = 50 * time.Millisecond
	)

	const skip = `echo "GATE-SKIP s: x"`

	tests := []chainShape{
		{"and_list", "true", "", " && ", ""},
		{"semicolons", "true", "", "; ", ""},
		{"pipeline", "true", "", " | ", ""},
		{"skip_mark_in_the_first_term", "true", skip, " && ", ""},
		{"skip_mark_in_the_last_term", "true", "", " && ", skip},
		{"subshell_terms", "(true)", "", " && ", ""},
		{"time_terms", "time true", "", "; ", ""},
		{"timed_pipeline", "time true", "", " | ", ""},
		{"coproc_pipeline", "coproc true", "", " | ", ""},
		{"function_terms", "f() time true", "", "; ", ""},
	}

	for _, test := range tests {
		_, _, small := readChain(t, test, terms)
		kept, size, large := readChain(t, test, terms*grow)

		assert.LessOrEqual(t, kept, size, test.name)
		assert.Less(t, large, 2*grow*max(small, floor), "%s: the cost of reading a chain is linear in its length", test.name)
	}
}

func repeat(term string, count int) []string {
	terms := make([]string, count)
	for i := range terms {
		terms[i] = term
	}

	return terms
}
