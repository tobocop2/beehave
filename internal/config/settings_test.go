package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
)

func TestSettings_Defaults(t *testing.T) {
	t.Parallel()

	s := config.NewSettings(map[string]string{})
	hours, err := s.IdleHours()
	require.NoError(t, err)
	grace, err := s.GraceHours()
	require.NoError(t, err)
	factor, err := s.GateLoadFactor()
	require.NoError(t, err)
	floor, err := s.DiskFloorGB()
	require.NoError(t, err)
	_, capSet, err := s.LaneCap()
	require.NoError(t, err)
	_, prSet, err := s.IntegrationPR()
	require.NoError(t, err)
	prs, err := s.IntegrationPRs()
	require.NoError(t, err)

	require.Equal(t, "github", s.IssueStore())
	require.InDelta(t, 1.0, hours, 0)
	require.InDelta(t, 1.0, grace, 0)
	require.Equal(t, "2", factor)
	require.Equal(t, 60, floor)
	require.Equal(t, "/home/jobs", s.JobsDir())
	require.Empty(t, s.Repo())
	require.False(t, capSet)
	require.False(t, prSet)
	require.Empty(t, prs)
	require.Empty(t, s.RoundItems())
}

func TestSettings_EmptyValueTakesTheDefault(t *testing.T) {
	t.Parallel()

	s := config.NewSettings(map[string]string{
		"ISSUE_STORE": "", "IDLE_HOURS": "", "DISK_FLOOR_GB": "", "JOBS_DIR": "", "REPO": "", "GATE_LOAD_FACTOR": "",
	})
	hours, err := s.IdleHours()
	require.NoError(t, err)
	floor, err := s.DiskFloorGB()
	require.NoError(t, err)
	factor, err := s.GateLoadFactor()
	require.NoError(t, err)

	require.Equal(t, "github", s.IssueStore())
	require.InDelta(t, 1.0, hours, 0)
	require.Equal(t, 60, floor)
	require.Equal(t, "/home/jobs", s.JobsDir())
	require.Empty(t, s.Repo())
	require.Equal(t, "2", factor)
}

func TestSettings_EveryKeyIsRead(t *testing.T) {
	t.Parallel()

	s := config.NewSettings(map[string]string{
		"REPO": "o/n", "DEFAULT_BRANCH": "main", "ROUND_PREFIX": "round/", "BASE": "stage",
		"INTEGRATION_BRANCH": "stage-b", "ROUND": "r3", "ISSUE_STORE": "beads", "ME_LOGIN": "me",
		"ME_EMAIL": "me@example.com", "IDLE_HOURS": "0.5", "GRACE_HOURS": "2", "INTEGRATION_PR": "7",
		"INTEGRATION_PRS": "7,5,3", "FLOOR_PRS_STAGING": "1", "FLOOR_PRS_DEV": "2", "FLOOR_PRS_OPEN": "3",
		"FLOOR_PRS_BASE": "4", "LANE_CAP": "6", "ROUND_EPIC": "e-1", "ROUND_ITEMS": "#a-1, b-2 c-3",
		"BOX_SSH": "ssh box", "GATE_LOAD_FACTOR": "1.5", "DISK_FLOOR_GB": "0", "JOBS_DIR": "/srv/jobs",
		"MUTATION_STEP_RE": "mut.*",
	})

	require.Equal(t, []string{"o/n", "main", "round/", "stage", "stage-b", "r3", "beads"},
		[]string{s.Repo(), s.DefaultBranch(), s.RoundPrefix(), s.Base(), s.IntegrationBranch(), s.RoundName(), s.IssueStore()})
	require.Equal(t, []string{"me", "me@example.com", "e-1", "ssh box", "/srv/jobs", "mut.*"},
		[]string{s.MeLogin(), s.MeEmail(), s.RoundEpic(), s.BoxSSH(), s.JobsDir(), s.MutationStepRE()})
	require.Equal(t, []string{"a-1", "b-2", "c-3"}, s.RoundItems())

	idle, err := s.IdleHours()
	require.NoError(t, err)
	grace, err := s.GraceHours()
	require.NoError(t, err)
	require.InDelta(t, 0.5, idle, 0)
	require.InDelta(t, 2.0, grace, 0)

	requireWhole(t, 7, s.IntegrationPR)
	requireWhole(t, 1, s.FloorPRsStaging)
	requireWhole(t, 2, s.FloorPRsDev)
	requireWhole(t, 3, s.FloorPRsOpen)
	requireWhole(t, 4, s.FloorPRsBase)
	requireWhole(t, 6, s.LaneCap)

	prs, err := s.IntegrationPRs()
	require.NoError(t, err)
	require.Equal(t, []string{"7", "5", "3"}, prs)

	factor, err := s.GateLoadFactor()
	require.NoError(t, err)
	require.Equal(t, "1.5", factor)

	floor, err := s.DiskFloorGB()
	require.NoError(t, err)
	require.Equal(t, 0, floor)
}

func requireWhole(t *testing.T, want int, read func() (int, bool, error)) {
	t.Helper()

	got, set, err := read()
	require.NoError(t, err)
	require.True(t, set)
	require.Equal(t, want, got)
}

func TestSettings_WholeNumberKeysRefuseOtherText(t *testing.T) {
	t.Parallel()

	keys := map[string]func(config.Settings) error{
		"LANE_CAP":       func(s config.Settings) error { _, _, err := s.LaneCap(); return err },
		"INTEGRATION_PR": func(s config.Settings) error { _, _, err := s.IntegrationPR(); return err },
		"FLOOR_PRS_OPEN": func(s config.Settings) error { _, _, err := s.FloorPRsOpen(); return err },
		"DISK_FLOOR_GB":  func(s config.Settings) error { _, err := s.DiskFloorGB(); return err },
	}

	for name, read := range keys {
		for _, bad := range []string{"x", "1.5", "-1", "1 2", "٣"} {
			err := read(config.NewSettings(map[string]string{name: bad}))
			require.ErrorIs(t, err, config.ErrRefused, name+"="+bad)
			require.ErrorContains(t, err, name+"="+bad+" is not a whole number.")
		}
	}
}

func TestSettings_AWholeNumberTooLargeForAnIntIsTooLarge(t *testing.T) {
	t.Parallel()

	_, _, err := config.NewSettings(map[string]string{"FLOOR_PRS_DEV": "99999999999999999999"}).FloorPRsDev()

	require.ErrorIs(t, err, config.ErrRefused)
	require.EqualError(t, err, "FLOOR_PRS_DEV=99999999999999999999 is too large. Write a smaller whole number.")
}

func TestSettings_LaneCap(t *testing.T) {
	t.Parallel()

	for _, zeros := range []string{"0", "00"} {
		_, _, err := config.NewSettings(map[string]string{"LANE_CAP": zeros}).LaneCap()
		require.EqualError(t, err, "LANE_CAP="+zeros+" allows no lane: set 1 or more, or leave it unset.")
	}

	for _, value := range []int{1, 10} {
		requireWhole(t, value, config.NewSettings(map[string]string{"LANE_CAP": map[int]string{1: "1", 10: "10"}[value]}).LaneCap)
	}
}

func TestSettings_HoursRefuseText(t *testing.T) {
	t.Parallel()

	_, err := config.NewSettings(map[string]string{"IDLE_HOURS": "soon"}).IdleHours()
	require.ErrorContains(t, err, "IDLE_HOURS=soon is not a number")

	_, err = config.NewSettings(map[string]string{"GRACE_HOURS": "soon"}).GraceHours()
	require.ErrorContains(t, err, "GRACE_HOURS=soon is not a number")
}

func TestSettings_HoursRefuseHexFloats(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"0x1p-2", "0X1P+1", "-0x1p0", " 0x1p0"} {
		_, err := config.NewSettings(map[string]string{"IDLE_HOURS": bad}).IdleHours()
		require.ErrorContains(t, err, "IDLE_HOURS="+bad+" is not a number", bad)

		_, err = config.NewSettings(map[string]string{"GRACE_HOURS": bad}).GraceHours()
		require.ErrorContains(t, err, "GRACE_HOURS="+bad+" is not a number", bad)
	}

	for _, good := range []struct {
		text string
		want float64
	}{{"1e1", 10}, {" 2.5 ", 2.5}, {".5", 0.5}} {
		got, err := config.NewSettings(map[string]string{"IDLE_HOURS": good.text}).IdleHours()
		require.NoError(t, err, good.text)
		require.InDelta(t, good.want, got, 0, good.text)
	}
}

func TestSettings_ABadKeyBlocksOnlyItsReaders(t *testing.T) {
	t.Parallel()

	s := config.NewSettings(map[string]string{"LANE_CAP": "x", "IDLE_HOURS": "soon", "GATE_LOAD_FACTOR": "fast", "INTEGRATION_PR": "q"})
	floor, err := s.DiskFloorGB()
	require.NoError(t, err)
	require.Equal(t, 60, floor)
	require.Equal(t, "github", s.IssueStore())

	_, _, err = s.LaneCap()
	require.Error(t, err)
	_, err = s.IdleHours()
	require.Error(t, err)
	_, err = s.GateLoadFactor()
	require.Error(t, err)
	_, _, err = s.IntegrationPR()
	require.Error(t, err)
}

func TestSettings_GateLoadFactor(t *testing.T) {
	t.Parallel()

	for _, good := range []string{"2", "0.5", "10", ".", "1.2.3", "..", "007", "5."} {
		got, err := config.NewSettings(map[string]string{"GATE_LOAD_FACTOR": good}).GateLoadFactor()
		require.NoError(t, err)
		require.Equal(t, good, got)
	}

	for _, bad := range []string{"1e3", "-1", "+1", "abc", " 1", "1 ", "1,5", "٣", "0x1", "1\n"} {
		_, err := config.NewSettings(map[string]string{"GATE_LOAD_FACTOR": bad}).GateLoadFactor()
		require.ErrorContains(t, err, "GATE_LOAD_FACTOR="+bad+" is not a number")
	}
}

func TestSettings_IntegrationPR(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"none", ""} {
		_, set, err := config.NewSettings(map[string]string{"INTEGRATION_PR": text}).IntegrationPR()
		require.NoError(t, err)
		require.False(t, set, text)
	}

	for _, text := range []string{"None", "NONE", "no", "none "} {
		_, _, err := config.NewSettings(map[string]string{"INTEGRATION_PR": text}).IntegrationPR()
		require.ErrorContains(t, err, "is not a whole number", text)
	}
}

func TestSettings_IntegrationPRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"one_number", map[string]string{"INTEGRATION_PRS": "7"}, []string{"7"}},
		{"three_numbers", map[string]string{"INTEGRATION_PRS": "7,5,3"}, []string{"7", "5", "3"}},
		{"blank_after_comma", map[string]string{"INTEGRATION_PRS": "7, 5"}, []string{"7", "5"}},
		{"blanks_around", map[string]string{"INTEGRATION_PRS": " 7 ,5 "}, []string{"7", "5"}},
		{"empty_entries", map[string]string{"INTEGRATION_PRS": "7,,5,"}, []string{"7", "5"}},
		{"blank_inside_a_number_joins_it", map[string]string{"INTEGRATION_PRS": "12 13"}, []string{"1213"}},
		{"only_a_comma", map[string]string{"INTEGRATION_PRS": ","}, nil},
		{"falls_back_to_the_single_pr", map[string]string{"INTEGRATION_PR": "9"}, []string{"9"}},
		{"empty_list_falls_back", map[string]string{"INTEGRATION_PR": "9", "INTEGRATION_PRS": ""}, []string{"9"}},
		{"list_wins", map[string]string{"INTEGRATION_PR": "9", "INTEGRATION_PRS": "7,5"}, []string{"7", "5"}},
		{"none_means_none", map[string]string{"INTEGRATION_PR": "none"}, nil},
		{"empty_means_none", map[string]string{"INTEGRATION_PR": ""}, nil},
		{"unset_means_none", map[string]string{}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := config.NewSettings(tc.env).IntegrationPRs()
			require.NoError(t, err)
			require.Equal(t, tc.want, append([]string(nil), got...))
		})
	}
}

func TestSettings_IntegrationPRsRefusesAnEntryThatIsNotANumber(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"7;5", "a,7", "7\n5", "#7"} {
		_, err := config.NewSettings(map[string]string{"INTEGRATION_PRS": text}).IntegrationPRs()
		require.ErrorContains(t, err, "INTEGRATION_PRS has", text)
	}
}

func TestSettings_RoundItems(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"a b": {"a", "b"}, "a\tb": {"a", "b"}, "a \t b": {"a", "b"}, "a,b": {"a", "b"}, "#a #b": {"a", "b"},
		"a#b": {"a", "b"}, "a\nb": {"a", "b"}, " a ": {"a"}, "a b": {"a b"}, "a;b": {"a;b"}, "": nil,
	}

	for text, want := range tests {
		require.Equal(t, want, config.NewSettings(map[string]string{"ROUND_ITEMS": text}).RoundItems(), text)
	}
}

func TestIsHeld(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"hold": true, "P1,hold,rust": true, "holder": false, "on-hold-ish": false, "P2,needs-hold": false, "": false,
	}

	for labels, want := range tests {
		require.Equal(t, want, config.IsHeld(labels), labels)
	}
}

func TestBoxSSHProblem(t *testing.T) {
	t.Parallel()

	require.Empty(t, config.BoxSSHProblem(""))
	require.Empty(t, config.BoxSSHProblem("sh -c true"))
	require.Equal(t,
		"BOX_SSH NOT RUNNABLE: round.env sets BOX_SSH to 'no-such-command-xyz --flag', and 'no-such-command-xyz' does not exist. "+
			"Point BOX_SSH at the live build host's helper, or remove it if no host is up.",
		config.BoxSSHProblem("no-such-command-xyz --flag"))
	require.Contains(t, config.BoxSSHProblem("  "), "and '' does not exist")
	require.Contains(t, config.BoxSSHProblem("no-such-command xyz"), "and 'no-such-command xyz' does not exist")
}
