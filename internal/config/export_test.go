package config

// ArgumentOwners maps each constructor argument of the runners file and the project file to its constructor. An argument that two constructors take belongs to the first, as the loader reads it.
func ArgumentOwners() map[string]string {
	owners := map[string]string{}

	machine := []Arg{{Key: "address", Optional: true}, {Key: "user", Optional: true}, {Key: "port", Kind: WholeArg, Optional: true}, {Key: "identity", Optional: true}, {Key: "work_root", Optional: true}, {Key: "known_hosts", Optional: true}}
	hosts := []Block{
		{Name: "skypilot", Args: []Arg{{Key: "cloud", Optional: true}, {Key: "region", Optional: true}, {Key: "instance_type", Optional: true}}},
		{Name: "slurm", Args: machine},
		{Name: "ssh", Args: machine},
	}

	for _, specs := range [][]spec{runnersSpecs(), projectSpecs([]Block{{Name: "beads", Args: []Arg{{Key: "prefix"}}}}, hosts)} {
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
