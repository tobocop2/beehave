package github

import (
	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/proc"
)

// RoleRunner returns a runner for gh that runs as the holder of token. Its environment is the network names of base and three variables: GH_TOKEN, an own
// GH_CONFIG_DIR (configDir, an empty folder that the caller made) and GH_PROMPT_DISABLED=1. No other variable of the engine reaches gh, so it never reads the login of the owner.
func RoleRunner(token, configDir string, base map[string]string) proc.ExecRunner {
	runner := proc.NewExecRunner(Tool, "")
	runner.Only = credential.RoleEnv(base, "GH_TOKEN="+token, "GH_CONFIG_DIR="+configDir, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")

	return runner
}
