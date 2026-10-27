package cli

import (
	"fmt"

	"github.com/spf13/pflag"

	"github.com/tobocop2/obee/internal/config"
)

// projectFlags holds the flags that name a project of the config, as run does.
type projectFlags struct {
	project string
	config  string
}

func (p *projectFlags) bind(f *pflag.FlagSet) {
	f.StringVar(&p.project, "project", "", "the project of the config (default: the only project)")
	f.StringVar(&p.config, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")
}

// given reports whether the command line names a project or a config folder.
func (p *projectFlags) given() bool { return p.project != "" || p.config != "" }

// load reads the config folder and returns the project the flags choose. The project may be left out when the config holds one.
func (p *projectFlags) load(env map[string]string) (config.Project, error) {
	dir, err := config.FolderPath(env, p.config)
	if err != nil {
		return config.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, env)
	if len(problems) > 0 {
		return config.Project{}, fmt.Errorf("the config has problems: run obee check config: %w", problems[0])
	}

	name, err := chooseProject(cfg, p.named())
	if err != nil {
		return config.Project{}, err
	}

	return cfg.Projects[name], nil
}

// named returns the project the flag names, or none.
func (p *projectFlags) named() []string {
	if p.project == "" {
		return nil
	}

	return []string{p.project}
}
