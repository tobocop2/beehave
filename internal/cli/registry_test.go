package cli_test

import (
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// registry declares the adapters that the project files of these tests name. The cli package reads the config with whatever registry the binary gives it,
// so the tests give it names and tools of their own and import no adapter.
func registry() config.Adapters {
	ledger := config.Block{Name: "beads", Args: []config.Arg{{Key: "prefix"}}}

	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{
			"local":  {},
			"beads":  {Block: &ledger, Tools: []string{"bd"}},
			"github": {Keys: []string{"repo"}, Tools: []string{"gh"}},
		},
		Claims: map[model.ClaimsName]*config.Entry{
			"local": {},
			"beads": {Block: &ledger, Tools: []string{"bd"}},
		},
		Forges: map[model.ForgeName]*config.Entry{
			"github": {Keys: []string{"repo"}, Tools: []string{"gh"}},
		},
		Hosts: map[model.HostProvider]*config.Entry{
			"skypilot": {Idle: true},
		},
		Agents: map[model.Agent]*config.Entry{
			"claude": {},
		},
	}
}
