package github_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

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
