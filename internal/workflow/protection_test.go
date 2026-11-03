package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

func TestProtection_OwnsTheRuleThatPairsKindsWithPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		kinds []model.FindingKind
		paths []string
		want  string
	}{
		{"kind without paths", []model.FindingKind{model.FindingProtected}, nil, "forbid_commands lists protected-path, so protect_paths must list at least one directory"},
		{"paths without a kind", []model.FindingKind{model.FindingGitWrite}, []string{"/own"}, "protect_paths is set, but forbid_commands lists no kind that uses it. Add \"protected-path\" to forbid_commands"},
		{"empty path", []model.FindingKind{model.FindingProtected}, []string{""}, "protect_paths holds an empty path, which would name the folder of the step. Give a path"},
		{"both set", []model.FindingKind{model.FindingProtected}, []string{"/own"}, ""},
		{"neither set", nil, nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := workflow.Protection(tc.kinds, tc.paths)
			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tc.want)
		})
	}
}
