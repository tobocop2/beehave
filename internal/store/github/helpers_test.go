package github_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// strict makes any runner refuse a field or a flag that the pinned gh refuses. A scripted runner refuses in place and keeps its record of calls; another runner is wrapped.
// Every test builds the store over strict(run): TestStore_IsBuiltOverAStrictRunnerInEveryTest fails for a call of github.New that does not.
func strict(run proc.Runner) proc.Runner {
	if scripted, ok := run.(*proctest.Scripted); ok {
		return scripted.Strict(ghcontract.Check)
	}

	return proctest.Checked(run, ghcontract.Check)
}

func toJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}

func fill(count int, row map[string]any) []map[string]any {
	rows := make([]map[string]any, count)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("p-%d", i), "number": i + 1, "title": fmt.Sprintf("t%d", i)}
		for key, value := range row {
			rows[i][key] = value
		}
	}

	return rows
}
