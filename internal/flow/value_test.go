package flow

import (
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

// shared builds a list of the given depth that holds the list of the level below twice, so it has 2^depth leaves and only depth lists in memory.
func shared(depth int) starlark.Value {
	var level starlark.Value = starlark.MakeInt(1)

	for range depth {
		level = starlark.NewList([]starlark.Value{level, level})
	}

	return level
}

func TestReturnedValue_ASharedListIsRefusedAtTheLimitWithBoundedMemory(t *testing.T) { //nolint:paralleltest // the test reads the allocation count of the whole process, so no other test may run beside it
	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	_, err := returnedValue("worker", shared(24))

	runtime.ReadMemStats(&after)

	require.ErrorContains(t, err, "returned a value that is larger than 65536 bytes")
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(8<<20), "the conversion stops at the limit and does not build the whole value")
}

func TestReturnedValue_AnEmptyListOfEmptyListsRunsOutToo(t *testing.T) {
	t.Parallel()

	items := make([]starlark.Value, 40000)
	for i := range items {
		items[i] = starlark.NewList(nil)
	}

	_, err := returnedValue("worker", starlark.NewList(items))

	require.ErrorContains(t, err, "larger than")
}

func TestReturnedValue_ADictWithManyKeysRunsOutToo(t *testing.T) {
	t.Parallel()

	d := starlark.NewDict(40000)
	for i := range 40000 {
		require.NoError(t, d.SetKey(starlark.String("k"+strconv.Itoa(i)), starlark.None))
	}

	_, err := returnedValue("worker", d)

	require.ErrorContains(t, err, "larger than 65536 bytes")
}

func TestReturnedValue_ANotFiniteFloatIsRefusedWithAClearMessage(t *testing.T) {
	t.Parallel()

	for name, f := range map[string]starlark.Float{"nan": nanValue(), "infinity": starlark.Float(math.Inf(1))} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := returnedValue("worker", starlark.NewList([]starlark.Value{f}))

			require.ErrorContains(t, err, "worker returned a value that is not JSON-safe")
			require.ErrorContains(t, err, "unsupported value")
			assert.Contains(t, err.Error(), "or remove the return", "the message tells the author that dropping the return is a fix")
		})
	}
}

// nanValue returns a float that is not a number.
func nanValue() starlark.Float { return starlark.Float(math.NaN()) }

func TestReturnedValue_TheDepthAndSizeLimitsAreExact(t *testing.T) {
	t.Parallel()

	nested := func(levels int) starlark.Value {
		var v starlark.Value = starlark.MakeInt(1)
		for range levels {
			v = starlark.NewList([]starlark.Value{v})
		}

		return v
	}

	_, err := returnedValue("worker", nested(valueDepth))
	require.NoError(t, err, "a value at the depth limit is kept")

	_, err = returnedValue("worker", nested(valueDepth+1))
	require.ErrorContains(t, err, "nests deeper than 32 levels")

	exact := starlark.String(strings.Repeat("x", valueLimit-len(`""`)))
	data, err := returnedValue("worker", exact)
	require.NoError(t, err, "a value of exactly the limit is kept")
	assert.Len(t, data, valueLimit)

	_, err = returnedValue("worker", starlark.String(strings.Repeat("x", valueLimit-1)))
	require.ErrorContains(t, err, "larger than 65536 bytes")
}

func TestReturnedValue_AnEscapedStringThatGrowsPastTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	_, err := returnedValue("worker", starlark.String(strings.Repeat("\x01", valueLimit/2)))

	require.ErrorContains(t, err, "larger than 65536 bytes", "the JSON text of the value is checked as well as the count")
}
