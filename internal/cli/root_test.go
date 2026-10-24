package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
)

func TestExecute(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Version: "1.2.3"}

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "prints_version",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "obee 1.2.3\n",
		},
		{
			name:       "rejects_unknown_flag",
			args:       []string{"--nope"},
			wantCode:   1,
			wantStderr: "obee: unknown flag: --nope\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			code := cli.Execute(context.Background(), tc.args, info, &stdout, &stderr)

			require.Equal(t, tc.wantCode, code)
			require.Equal(t, tc.wantStdout, stdout.String())
			require.Equal(t, tc.wantStderr, stderr.String())
		})
	}
}
