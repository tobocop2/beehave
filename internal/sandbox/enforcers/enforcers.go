// Package enforcers is the table that chooses a sandbox enforcer by name, the same way the agent adapters are chosen.
package enforcers

import (
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/sandbox"
	"github.com/tobocop2/obee/internal/sandbox/landlock"
	"github.com/tobocop2/obee/internal/sandbox/seatbelt"
)

// Order lists the enforcers from the best to the weakest. Auto picks the first that can run here.
func Order() []string { return []string{landlock.Name, seatbelt.Name} }

// Table maps the name of each enforcer to the enforcer. Pass it with Order to sandbox.Choose.
func Table() map[string]sandbox.Enforcer {
	return map[string]sandbox.Enforcer{landlock.Name: landlock.New(), seatbelt.Name: seatbelt.New()}
}

// Refusal returns the reason why the gate of the project does not run on this machine, or "" when it runs. A gate is refused when the project has its
// sandbox on, the gate would run here, and no enforcer can run here. program is true for a gate that names its own program.
func Refusal(project config.Project, program bool) string {
	return refusal(project, program, Table(), Order())
}

func refusal(project config.Project, program bool, table map[string]sandbox.Enforcer, order []string) string {
	if !config.GateRefused(project.Project, program) {
		return ""
	}

	if _, err := sandbox.Choose(table, order, project.SandboxEnforcer); err != nil {
		return config.GateSandboxRefusal + " " + err.Error() + "."
	}

	return ""
}
