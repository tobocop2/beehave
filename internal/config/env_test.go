package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

func TestRefuseDBOSVariables_RefusesEachVariableTheReportNames(t *testing.T) {
	t.Parallel()

	names := []string{
		"DBOS__CLOUD", "DBOS__CONDUCTOR_APP_NAME", "DBOS__CONDUCTOR_KEY", "DBOS__CONDUCTOR_URL",
		"DBOS__APPVERSION", "DBOS__VMID", "DBOS__APPID", "DBOS__ANYTHING", "DBOS_DOMAIN", "DBOS_SYSTEM_DATABASE_URL",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := config.RefuseDBOSVariables(map[string]string{name: "", "PATH": "/bin"})

			require.ErrorIs(t, err, config.ErrRefused)
			require.ErrorContains(t, err, name+" is set")
			require.ErrorContains(t, err, "Unset it")
		})
	}
}

func TestRefuseDBOSVariables_NamesEveryVariableInOrder(t *testing.T) {
	t.Parallel()

	err := config.RefuseDBOSVariables(map[string]string{"DBOS__VMID": "1", "DBOS__APPID": "2"})

	require.ErrorContains(t, err, "DBOS__APPID, DBOS__VMID is set")
}

func TestRefuseDBOSVariables_AllowsOtherVariables(t *testing.T) {
	t.Parallel()

	env := map[string]string{"DBOS": "1", "DBOS_": "1", "XDBOS__A": "1", "DBOS_OTHER": "1", "BEEHAVE_STATE_DIR": "/x"}

	require.NoError(t, config.RefuseDBOSVariables(env))
}

func TestProcessEnv_ReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("BEEHAVE_PROBE_VALUE", "a=b")

	require.Equal(t, "a=b", config.ProcessEnv()["BEEHAVE_PROBE_VALUE"])
}

func TestFolderPath(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	tests := []struct {
		name string
		env  map[string]string
		flag string
		want string
	}{
		{"flag_wins", map[string]string{"BEEHAVE_CONFIG": "/from/env"}, "/from/flag", "/from/flag"},
		{"flag_expands_home", nil, "~/cfg", filepath.Join(home, "cfg")},
		{"variable_when_no_flag", map[string]string{"BEEHAVE_CONFIG": "/from/env"}, "", "/from/env"},
		{"variable_expands_home", map[string]string{"BEEHAVE_CONFIG": "~/e"}, "", filepath.Join(home, "e")},
		{"default_is_under_the_home_folder", nil, "", filepath.Join(home, ".config", "beehave")},
	}

	for _, tc := range tests { //nolint:paralleltest // sets HOME
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.FolderPath(tc.env, tc.flag)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestFolderPath_RefusesWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := config.FolderPath(nil, "")
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "Set HOME")

	_, err = config.FolderPath(nil, "~/x")
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "Set HOME, or write the full path")
}
