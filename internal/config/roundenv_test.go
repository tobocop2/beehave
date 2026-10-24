package config_test

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
)

const envFileText = "REPO=file/repo\nISSUE_STORE=github\nBASE=development\nQUOTED=\"quoted-value\"\n"

func loadWith(t *testing.T, extra map[string]string) config.Round {
	t.Helper()

	path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), envFileText)
	env := map[string]string{"ROUND_ENV": path}

	for key, value := range extra {
		env[key] = value
	}

	round, err := config.LoadRound(env, "")
	require.NoError(t, err)

	return round
}

func TestLoadRound_Precedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra map[string]string
		want  map[string]string
	}{
		{
			name: "file_fills_every_key_when_nothing_is_exported",
			want: map[string]string{"REPO": "file/repo", "ISSUE_STORE": "github", "BASE": "development", "QUOTED": "quoted-value"},
		},
		{
			name:  "environment_wins_for_several_keys",
			extra: map[string]string{"REPO": "caller/wins", "ISSUE_STORE": "notastore", "BASE": "other"},
			want:  map[string]string{"REPO": "caller/wins", "ISSUE_STORE": "notastore", "BASE": "other", "QUOTED": "quoted-value"},
		},
		{
			name:  "exported_empty_value_is_kept_not_refilled",
			extra: map[string]string{"REPO": ""},
			want:  map[string]string{"REPO": "", "BASE": "development"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := loadWith(t, tc.extra).Env
			for key, value := range tc.want {
				require.Equal(t, value, env[key], key)
			}
		})
	}
}

func TestLoadRound_FirstLineForAKeyWins(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), "REPO=first\nREPO=second\n")
	round, err := config.LoadRound(map[string]string{"ROUND_ENV": path}, "")
	require.NoError(t, err)
	require.Equal(t, "first", round.Env["REPO"])
}

func TestLoadRound_NotARegularFileReadsAsNoRoundEnv(t *testing.T) {
	t.Parallel()

	env := map[string]string{"ROUND_ENV": "/dev/null", "REPO": "o/n"}
	round, err := config.LoadRound(env, "")
	require.NoError(t, err)
	require.Equal(t, env, round.Env)
}

func TestLoadRound_RefusesABadFile(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), "A=1\r\n")
	_, err := config.LoadRound(map[string]string{"ROUND_ENV": path}, "")
	require.ErrorIs(t, err, config.ErrRefused)
}

func TestLoadRound_DoesNotChangeTheCallersMap(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), envFileText)
	env := map[string]string{"ROUND_ENV": path}
	_, err := config.LoadRound(env, "")
	require.NoError(t, err)
	require.Len(t, env, 1)
}

func TestParseRoundEnv_Accepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []config.Pair
	}{
		{"skips_comment_and_blank_line", "\n# a comment\nEXTRA=yes\nREPO=a/b\n", []config.Pair{{Key: "EXTRA", Value: "yes"}, {Key: "REPO", Value: "a/b"}}},
		{
			"skips_keys_that_are_not_shell_names",
			"not a name=danger\n../../evil=danger\nBAD-KEY=nope\nREPO=a/b\n",
			[]config.Pair{{Key: "notaname", Value: "danger"}, {Key: "REPO", Value: "a/b"}},
		},
		{
			"keeps_values_and_removes_one_pair_of_quotes",
			"MIXQ='x\"\nPADDED=  padded  \n SPKEY =v\nEQ=a=b\nEMPTY=\nSINGLE='single'\nLONE=\"\nDOUBLE=\"\"quoted\"\"\nPAIR=''\n",
			[]config.Pair{
				{Key: "MIXQ", Value: "'x\""},
				{Key: "PADDED", Value: "  padded  "},
				{Key: "SPKEY", Value: "v"},
				{Key: "EQ", Value: "a=b"},
				{Key: "EMPTY", Value: ""},
				{Key: "SINGLE", Value: "single"},
				{Key: "LONE", Value: "\""},
				{Key: "DOUBLE", Value: "\"quoted\""},
				{Key: "PAIR", Value: ""},
			},
		},
		{
			"keeps_an_inline_comment_and_a_backslash_as_text",
			"NOTE=1 # not a comment\nESC=\"a\\nb\"\n",
			[]config.Pair{{Key: "NOTE", Value: "1 # not a comment"}, {Key: "ESC", Value: "a\\nb"}},
		},
		{"reads_a_final_line_without_newline", "A=1\nB=2", []config.Pair{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}}},
		{
			"keeps_shell_syntax_as_text",
			"P_DOLLAR=$HOME\nP_TICK=`id`\nP_GLOB=*\n  P_SPACED  =x\nP_A B=joined\nP_U=café\n",
			[]config.Pair{
				{Key: "P_DOLLAR", Value: "$HOME"},
				{Key: "P_TICK", Value: "`id`"},
				{Key: "P_GLOB", Value: "*"},
				{Key: "P_SPACED", Value: "x"},
				{Key: "P_AB", Value: "joined"},
				{Key: "P_U", Value: "café"},
			},
		},
		{"skips_a_non_ascii_key_on_a_line_with_no_equals_sign_or_in_a_comment", "é no equals sign\n# é=1\n  # é=1\n\t# é P=1\nGOOD=ok\n", []config.Pair{{Key: "GOOD", Value: "ok"}}},
		{"skips_a_line_with_a_vertical_tab_or_form_feed_in_the_key", "GOOD=ok\nV\x0bT=1\nF\x0cF=2\n\x0bLEAD=3\n", []config.Pair{{Key: "GOOD", Value: "ok"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), tc.text)
			got, err := config.ParseRoundEnv(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseRoundEnv_ReadsTheFileAsShellSourceDoesNotAsDotenv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []config.Pair
	}{
		{"export_prefix_joins_the_key", "export A=1\n", []config.Pair{{Key: "exportA", Value: "1"}}},
		{"blanks_around_equals_keep_the_value_blank", "A = 1\n", []config.Pair{{Key: "A", Value: " 1"}}},
		{"dollar_name_stays_text", "A=$B\n", []config.Pair{{Key: "A", Value: "$B"}}},
		{"text_after_a_closing_quote_keeps_the_quotes", "A='x'y\n", []config.Pair{{Key: "A", Value: "'x'y"}}},
		{"quoted_value_does_not_span_lines", "A=\"x\ny\"\nB=2\n", []config.Pair{{Key: "A", Value: "\"x"}, {Key: "B", Value: "2"}}},
		{"colon_is_not_a_separator", "A: 1\nB=2\n", []config.Pair{{Key: "B", Value: "2"}}},
		{"a_repeated_key_stays_in_file_order", "A=1\nA=2\n", []config.Pair{{Key: "A", Value: "1"}, {Key: "A", Value: "2"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), tc.text)
			got, err := config.ParseRoundEnv(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseRoundEnv_RefusesTheWholeFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tail  string
		words string
	}{
		{"non_ascii_key", "é=2\n", "not ASCII"},
		{"non_ascii_key_after_ascii", "ÉPQ_B=1\n", "not ASCII"},
		{"em_space_before_key", " EMSP=x\n", "not ASCII"},
		{"no_break_space_before_key", " NBSP=x\n", "not ASCII"},
		{"nul_byte", "NUL=a\x00b\n", "NUL byte"},
		{"code_point_past_the_range", "BIG=\xf4\x90\x80\x80\n", "not valid UTF-8"},
		{"invalid_bytes", "B=\xff\xfe\n", "not valid UTF-8"},
		{"surrogate", "B=\xed\xa0\x80\n", "not valid UTF-8"},
		{"overlong_two_byte", "B=\xc0\x80\n", "not valid UTF-8"},
		{"overlong_three_byte", "B=\xe0\x80\x80\n", "not valid UTF-8"},
		{"truncated_sequence", "B=\xe2\x82\n", "not valid UTF-8"},
		{"crlf", "B=v\r\nC=w\r\n", "carriage return"},
		{"lone_cr", "B=v\rC=w\n", "carriage return"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeFile(t, filepath.Join(t.TempDir(), "round.env"), "GOOD=ok\n"+tc.tail)
			_, err := config.ParseRoundEnv(path)
			require.ErrorIs(t, err, config.ErrRefused)
			require.ErrorContains(t, err, tc.words)
			require.ErrorContains(t, err, path)
		})
	}
}

func TestParseRoundEnv_AMissingFileKeepsItsCause(t *testing.T) {
	t.Parallel()

	_, err := config.ParseRoundEnv(filepath.Join(t.TempDir(), "none.env"))

	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestParseRoundEnv_AnUnreadableFileIsARefusal(t *testing.T) {
	t.Parallel()

	_, err := config.ParseRoundEnv(t.TempDir())
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "cannot read")
}

func TestFindRoundEnv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	beside := filepath.Join(root, "tools")
	pad := filepath.Join(root, "pad")
	env := map[string]string{"SCRATCHPAD": pad}

	require.Equal(t, "round.env", config.FindRoundEnv(env, beside), "the working directory is the default")

	writeFile(t, filepath.Join(pad, "round.env"), "A=1\n")
	require.Equal(t, filepath.Join(pad, "round.env"), config.FindRoundEnv(env, beside))

	writeFile(t, filepath.Join(root, "round.env"), "A=1\n")
	require.Equal(t, filepath.Join(root, "round.env"), config.FindRoundEnv(env, beside))

	writeFile(t, filepath.Join(beside, "round.env"), "A=1\n")
	require.Equal(t, filepath.Join(beside, "round.env"), config.FindRoundEnv(env, beside))
}

func TestFindRoundEnv_ExplicitVariable(t *testing.T) {
	t.Parallel()

	beside := t.TempDir()
	writeFile(t, filepath.Join(beside, "round.env"), "A=1\n")

	require.Equal(t, "/nonexistent/x.env", config.FindRoundEnv(map[string]string{"ROUND_ENV": "/nonexistent/x.env"}, beside))
	require.Equal(t, filepath.Join(beside, "round.env"), config.FindRoundEnv(map[string]string{"ROUND_ENV": ""}, beside), "empty means unset")
}

func TestRound_Need(t *testing.T) {
	t.Parallel()

	round := loadWith(t, nil)

	value, err := round.Need("REPO", "")
	require.NoError(t, err)
	require.Equal(t, "file/repo", value)

	_, err = round.Need("ME_LOGIN", "It names whose items are ours.")
	require.ErrorIs(t, err, config.ErrRefused)
	require.EqualError(t, err, "ME_LOGIN is not set. Export it, or put it in "+round.EnvFile+". It names whose items are ours.")

	_, err = loadWith(t, map[string]string{"REPO": ""}).Need("REPO", "")
	require.ErrorContains(t, err, "REPO is not set")
}

func TestRound_Root(t *testing.T) {
	t.Parallel()

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, symlink(real, link))
	writeFile(t, filepath.Join(real, "round.env"), "REPO=o/n\n")

	round, err := config.LoadRound(map[string]string{"ROUND_ENV": filepath.Join(link, "sub", "..", "round.env")}, "")
	require.NoError(t, err)

	root, err := round.Root()
	require.NoError(t, err)
	require.Equal(t, link, root, "a symlink stays and dot-dot goes")
}

func TestRound_RootNeedsARoundEnv(t *testing.T) {
	t.Parallel()

	round, err := config.LoadRound(map[string]string{"ROUND_ENV": "/dev/null"}, "")
	require.NoError(t, err)

	_, err = round.Root()
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "no round.env at /dev/null")
	require.ErrorContains(t, err, "Set ROUND_ENV.")
}

func TestRound_SettingsComeFromTheMergedEnvironment(t *testing.T) {
	t.Parallel()

	require.Equal(t, "other", loadWith(t, map[string]string{"BASE": "other"}).Settings().Base())
	require.Equal(t, "file/repo", loadWith(t, nil).Settings().Repo())
}
