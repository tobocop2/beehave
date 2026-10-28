package beads_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
)

func bodyFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(path, []byte("finding\n"), 0o600))

	return path
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

func codeOf(t *testing.T, err error) int {
	t.Helper()

	var failure *store.Error
	require.ErrorAs(t, err, &failure)

	return failure.Code
}
