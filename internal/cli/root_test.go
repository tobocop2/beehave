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

			code := cli.Execute(context.Background(), tc.args, info, map[string]string{}, &stdout, &stderr)

			require.Equal(t, tc.wantCode, code)
			require.Equal(t, tc.wantStdout, stdout.String())
			require.Equal(t, tc.wantStderr, stderr.String())
		})
	}
}

func TestExecute_RefusesToStartWhenADBOSVariableIsSet(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	env := map[string]string{"DBOS__CONDUCTOR_KEY": "secret-value"}
	code := cli.Execute(context.Background(), []string{"--version"}, cli.BuildInfo{Version: "1"}, env, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, "obee: DBOS__CONDUCTOR_KEY is set. DBOS reads it and would connect to a service outside this machine. Unset it and start obee again.\n", stderr.String())
	require.NotContains(t, stderr.String(), "secret-value")
}
