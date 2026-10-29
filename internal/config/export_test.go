package config

// ArgumentOwners maps each constructor argument of the runners file and the project file to its constructor.
func ArgumentOwners() map[string]string {
	owners := map[string]string{}

	for _, specs := range [][]spec{runnersSpecs(), projectSpecs()} {
		for _, s := range specs {
			for _, argument := range s.arguments() {
				owners[argument] = s.name
			}
		}
	}

	return owners
}
