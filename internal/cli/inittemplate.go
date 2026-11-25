package cli

import (
	"embed"
	"io/fs"
)

// templateFiles holds the workflow and the prompts that init writes into a repository. A test keeps them equal to examples/local.
//
//go:embed initfiles
var templateFiles embed.FS

const templateRoot = "initfiles"

// templateFile is one file of the template, with its path below the repository.
type templateFile struct {
	path string
	data []byte
}

// readTemplate returns every file of the template. An embedded tree always walks and reads, so the errors have no recovery.
func readTemplate() []templateFile {
	var files []templateFile

	//nolint:errcheck,gosec // an embedded tree has no walk error
	fs.WalkDir(templateFiles, templateRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() {
			data, _ := templateFiles.ReadFile(path) //nolint:errcheck // an embedded file always reads
			files = append(files, templateFile{path: path[len(templateRoot)+1:], data: data})
		}

		return nil
	})

	return files
}
