package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
)

const (
	variableWords = " is a variable, and a config file takes none. "
	helperAdvice  = "Put a config value inside a constructor call, or start the name with an underscore for a helper."
	stopped       = "Starlark computation cancel" + "led: " // the Starlark text for a stopped thread
	hostLine      = "host(provider = \"skypilot\", disk_floor_gb = 50)\n"
)

func TestLoadFolder_AStrayTopLevelNameIsRefused(t *testing.T) {
	t.Parallel()

	project := `project(source = "/s", base_branch = "main", gate = "g")` + "\n"

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"flat_ported_hcl_file", "tracker = \"github\"\nrepo = \"o/n\"\n" + project,
			"p.star:1:1: `tracker`" + variableWords + "Put a config value inside a constructor call: pass it to project(...).",
		},
		{"typo", "gatee = \"make check\"\n" + project, "p.star:1:1: `gatee`" + variableWords + helperAdvice},
		{
			"value_that_matches_a_host_argument", project + "disk_floor_gb = 50\n",
			"p.star:2:1: `disk_floor_gb`" + variableWords + "Put a config value inside a constructor call: pass it to host(...).",
		},
		{
			"value_that_matches_a_skypilot_argument", project + "region = \"x\"\n",
			"p.star:2:1: `region`" + variableWords + "Put a config value inside a constructor call: pass it to skypilot(...).",
		},
		{"name_set_from_a_helper", "_n = 1\nn = _n\n" + project, "p.star:2:1: `n`" + variableWords + helperAdvice},
		{"tuple_target", "_a, b = 1, 2\n" + project, "p.star:1:5: `b`" + variableWords + helperAdvice},
		{"list_target", "[_a, c] = [1, 2]\n" + project, "p.star:1:6: `c`" + variableWords + helperAdvice},
		{"parenthesis_target", "(_a, d) = (1, 2)\n" + project, "p.star:1:6: `d`" + variableWords + helperAdvice},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.want)
		})
	}
}

func TestLoadFolder_AStrayTopLevelNameInTheRunnersFileIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "agent = \"claude\"\n"+goodRunner)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "runners.star:1:1: `agent`"+variableWords+"Put a config value inside a constructor call: pass it to runner(...).")
}

func TestLoadFolder_AFunctionAndAnUnderscoreNameAreAllowed(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t,
		"_base = \"main\"\n_a, _b = 1, 2\n_list = [1]\n_list[0] = 2\ndef gate_for(name):\n  return \"make \" + name\n\nproject(source = \"/s\", base_branch = _base, gate = gate_for(\"check\"))\n")

	require.Empty(t, problems)
	require.Equal(t, "make check", project.Gate)
}

func TestLoadFolder_AFailCallReportsThePositionOfTheCall(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "fail(\"boom\")\n"+call(goodProject))

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], "p.star:1:5: fail: boom. Fix the file.")
	require.NotContains(t, problems[0].Error(), "<builtin>")
}

func TestLoadFolder_ABlockOfTheWrongKindIsRefusedWithTheRightName(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, skypilot, want string }{
		{"host_where_skypilot_goes", hostCall(""), `host: for parameter "skypilot": got host(...), want skypilot(...). Fix the call.`},
		{"string_where_skypilot_goes", `"gcp"`, `host: for parameter "skypilot": got string, want skypilot(...). Fix the call.`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", skypilot = "+tc.skypilot)))

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], "p.star:1:")
			require.ErrorContains(t, problems[0], tc.want)
			require.NotContains(t, problems[0].Error(), "appears twice")
		})
	}
}

func TestLoadFolder_EveryArgumentNamesItsConstructor(t *testing.T) {
	t.Parallel()

	// The table comes from the constructor definitions, so no second copy exists.
	owners := config.ArgumentOwners()
	require.Len(t, owners, 31)

	for key, owner := range owners {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			// The runners file and the project file each hold some of the constructors; the stray name is checked in both.
			_, runnerProblems := loadRunnersText(t, key+" = 1\n")
			_, projectProblems := loadProjectText(t, key+" = 1\n")

			require.Len(t, runnerProblems, 1)
			require.Len(t, projectProblems, 1)
			require.ErrorContains(t, runnerProblems[0], "`"+key+"`"+variableWords)
			require.ErrorContains(t, projectProblems[0], "`"+key+"`"+variableWords)

			inRunners := owner == "agents" || owner == "pool" || owner == "runner"
			if inRunners {
				require.ErrorContains(t, runnerProblems[0], "pass it to "+owner+"(...).")

				_, problems := loadRunnersText(t, owner+"("+key+" = None)\n")
				for _, found := range problems {
					require.NotContains(t, found.Error(), "unexpected keyword argument", "the %s call must take %s", owner, key)
				}

				return
			}

			require.ErrorContains(t, projectProblems[0], "pass it to "+owner+"(...).")

			_, problems := loadProjectText(t, owner+"("+key+" = None)\n")
			for _, found := range problems {
				require.NotContains(t, found.Error(), "unexpected keyword argument", "the %s call must take %s", owner, key)
			}
		})
	}
}

func TestLoadFolder_ABlockThatNoArgumentHoldsIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"host_call_with_a_local_build", hostLine + call(goodProject),
			"p.star:1:5: `host(...)` builds a block that no `project` argument holds. Write project(host = host(...)).",
		},
		{
			"host_call_with_a_cluster_build", hostLine + call(clusterProject),
			"p.star:1:5: `host(...)` builds a block that no `project` argument holds. Write project(host = host(...)).",
		},
		{
			"skypilot_call_inside_a_held_host", "skypilot(cloud = \"gcp\")\n" + call(clusterProject+", host = "+hostCall("")),
			"p.star:1:9: `skypilot(...)` builds a block that no `host` argument holds. Write host(skypilot = skypilot(...)).",
		},
		{
			"helper_variable_that_nothing_reads", "_h = " + hostLine + call(goodProject),
			"p.star:1:10: `host(...)` builds a block that no `project` argument holds.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.want)
			require.NotContains(t, problems[0].Error(), "is required")
		})
	}
}

func TestLoadFolder_ABlockHeldThroughAVariableIsAccepted(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "_h = "+hostLine+call(clusterProject+", host = _h"))

	require.Empty(t, problems)
	require.NotNil(t, project.Host)
}

func TestLoadFolder_PrintIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "print(\"hi\")\n"+call(goodProject))

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], "p.star:1:6: "+stopped+"`print` writes output, and a config file does no output. Remove the print call. Fix the file.")
}

func TestLoadFolder_AComprehensionThatReachesTheStepLimitIsStopped(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "_x = [i for i in range(1000000000)]\n")

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], "runners.star:1:7: "+stopped+"too many steps. Fix the file.")
}

func TestLoadFolder_AFloatForAnIntegerArgumentIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, host, want string }{
		{"disk_floor_gb", `host(provider = "skypilot", disk_floor_gb = 50.0)`, `host: for parameter "disk_floor_gb": got float, want int. Fix the call.`},
		{"idle_minutes", `host(provider = "skypilot", disk_floor_gb = 50, idle_minutes = 60.0)`, `host: for parameter "idle_minutes": got float, want int. Fix the call.`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(clusterProject+", host = "+tc.host))

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], "p.star:1:")
			require.ErrorContains(t, problems[0], tc.want)
		})
	}
}
