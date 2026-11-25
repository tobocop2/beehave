package config

import "github.com/tobocop2/beehave/internal/model"

// argumentFixture is the adapter registry of the argument census: the pool windows, the project blocks and the host blocks that the loader reads from the adapters.
func argumentFixture() ([]model.WindowSpec, []Block, []Block) {
	machine := []Arg{{Key: "address", Optional: true}, {Key: "user", Optional: true}, {Key: "port", Kind: WholeArg, Optional: true}, {Key: "identity", Optional: true}, {Key: "work_root", Optional: true}, {Key: "known_hosts", Optional: true}}
	hosts := []Block{
		{Name: "skypilot", Args: []Arg{{Key: "cloud", Optional: true}, {Key: "region", Optional: true}, {Key: "instance_type", Optional: true}}},
		{Name: "slurm", Args: machine},
		{Name: "ssh", Args: machine},
	}

	return []model.WindowSpec{{Name: "five_hour"}, {Name: "seven_day"}}, []Block{{Name: "beads", Args: []Arg{{Key: "prefix"}}}}, hosts
}

// ArgumentOwners maps each constructor argument of the runners file and the project file to its constructor. An argument that two constructors take belongs to the first, as the loader reads it.
func ArgumentOwners() map[string]string {
	owners := map[string]string{}
	windows, declared, hosts := argumentFixture()

	for _, specs := range [][]spec{runnersSpecs(windows), projectSpecs(declared, hosts)} {
		for _, s := range specs {
			for _, argument := range s.arguments() {
				if _, owned := owners[argument]; !owned {
					owners[argument] = s.name
				}
			}
		}
	}

	return owners
}

// FixtureArguments lists the arguments that the adapter fixture adds to the fixed constructors: a window name for the pool, a block name for the project or the host, and the keys of each block.
func FixtureArguments() []string {
	windows, declared, hosts := argumentFixture()

	var names []string
	for _, window := range windows {
		names = append(names, window.Name)
	}

	for _, block := range append(declared, hosts...) {
		names = append(names, block.Name)
		for _, arg := range block.Args {
			names = append(names, arg.Key)
		}
	}

	return names
}
