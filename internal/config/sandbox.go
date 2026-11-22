package config

// SandboxDefault says whether the named project runs its steps in the sandbox by default. A project the config lacks does not.
func (c Config) SandboxDefault(project string) bool { return c.Projects[project].SandboxByDefault() }
