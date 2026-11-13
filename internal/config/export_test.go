package config

// ArgumentOwners maps each constructor argument of the runners file and the project file to its constructor. Two blocks that share a key name give it to the first, as the loader does.
func ArgumentOwners(registry Adapters) map[string]string {
	owners := map[string]string{}

	projectBlocks, _ := registry.projectBlocks()
	hostBlocks, _ := registry.hostBlocks()

	for _, specs := range [][]spec{runnersSpecs(), projectSpecs(projectBlocks, hostBlocks)} {
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
