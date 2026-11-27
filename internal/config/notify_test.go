package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotify_AListLoadsIntoTheProject(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, notify = ["ntfy", "publish", "beehest"]`))

	require.Empty(t, problems)
	require.Equal(t, []string{"ntfy", "publish", "beehest"}, project.Notify)
}

func TestNotify_AnUnsetNotifyLoadsAsNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.Notify)
}

func TestNotify_ARefusedValueNamesTheKeyAndTheFix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"text", `"ntfy publish beehest"`, "list of strings"},
		{"a list with a number", `["ntfy", 3]`, "list of strings"},
		{"an empty list", `[]`, "`notify` is an empty list"},
		{"an empty program", `["", "x"]`, "`notify` names an empty program"},
		{"a blank program", `["  ", "x"]`, "`notify` names an empty program"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(localProject+`, notify = `+tt.value))

			require.NotEmpty(t, problems)
			require.NotContains(t, problems.Error(), "unexpected keyword")
			require.Contains(t, problems.Error(), "notify")
			require.Contains(t, problems.Error(), tt.want)
		})
	}
}
