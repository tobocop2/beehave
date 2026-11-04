package gate

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/check/shell"
)

type chainShape struct{ name, term, head, join, tail string }

// chainRead is what one read of a chain cost: the bytes the read kept, the bytes it allocated, and the allocations it made.
type chainRead struct {
	kept, size        int
	allocated, allocs uint64
}

// readChain builds a chain of the given number of terms and reads it. The count covers the read only, not the build of the job or its parse.
func readChain(t *testing.T, shape chainShape, terms int) chainRead {
	t.Helper()

	list := repeat(shape.term, terms)
	if shape.head != "" {
		list[0] = shape.head
	}

	if shape.tail != "" {
		list[terms-1] = shape.tail
	}

	job := strings.Join(list, shape.join) + "\n"
	units, bad := shell.ParseUnits(job)
	assert.Empty(t, bad)

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	script := readGateScript(units)

	runtime.ReadMemStats(&after)

	read := chainRead{
		size:      len(job),
		allocated: after.TotalAlloc - before.TotalAlloc,
		allocs:    after.Mallocs - before.Mallocs,
	}

	for _, call := range script.calls {
		read.kept += len(call)
	}

	return read
}

// TestReadGateScript_AChainKeepsNoMoreBytesThanTheJobAndIsReadInLinearTime bounds two costs of the step rules by counting, so that the load of the machine cannot change the result.
// A chain of n terms must keep about one copy of its text, not n copies. The read of a chain of 4n terms must allocate less than 8 times the bytes, and make less than 8 times the allocations, of the read of n terms (linear is 4, quadratic is 16).
// A read that walks the chain once for each term allocates for each walk, so a quadratic read shows as quadratic counts.
// The test runs alone: the counts are those of the whole process, and another test that allocates at the same time would add to them.
func TestReadGateScript_AChainKeepsNoMoreBytesThanTheJobAndIsReadInLinearTime(t *testing.T) { //nolint:paralleltest // the allocation counts are those of the process
	const (
		terms = 5000
		grow  = 4
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
		small := readChain(t, test, terms)
		large := readChain(t, test, terms*grow)

		assert.LessOrEqual(t, large.kept, large.size, "%s: the read keeps no more bytes than the job", test.name)
		assert.Less(t, large.allocated, 2*grow*max(small.allocated, 1), "%s: the bytes allocated by a read are linear in the length of the chain", test.name)
		assert.Less(t, large.allocs, 2*grow*max(small.allocs, 1), "%s: the allocations of a read are linear in the length of the chain", test.name)
	}
}

func repeat(term string, count int) []string {
	terms := make([]string, count)
	for i := range terms {
		terms[i] = term
	}

	return terms
}
