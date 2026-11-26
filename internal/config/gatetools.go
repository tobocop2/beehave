package config

import "slices"

// effectiveGateProgram is the sorted owner gate_program joined with the files that the gate tools read. An empty owner list stays empty.
func effectiveGateProgram(raw rawProject, read ProgramReader) []string {
	if len(raw.GateProgram) == 0 {
		return nil
	}

	names := append(secretScanFiles(), raw.GateProgram...)

	for _, command := range gateCommands(raw) {
		names = append(names, commandFiles(read, command)...)
	}

	slices.Sort(names)

	return slices.Compact(names)
}

// gateCommands are the command texts of the gate: gate, each command of gate_steps, and test_command.
func gateCommands(raw rawProject) []string {
	commands := []string{raw.Gate, raw.TestCommand.Value}

	for _, spec := range raw.GateSteps {
		commands = append(commands, spec.Command)
	}

	return commands
}

// commandFiles are the files that the programs of one command read. A command that the reader cannot read, or that no reader reads, gets the files of every gate tool.
func commandFiles(read ProgramReader, command string) []string {
	if command == "" {
		return nil
	}

	if read == nil {
		return everyToolFile()
	}

	programs, readable := read(command)
	if !readable {
		return everyToolFile()
	}

	var names []string

	for _, program := range programs {
		names = append(names, toolTable()[program]...)
	}

	return names
}

// defaultNamesAgainst refuses a name that the gate tools add when an owner entry names it as a folder or a test_paths glob can match it.
func defaultNamesAgainst(raw rawProject, program []string) error {
	for _, name := range program {
		if slices.Contains(raw.GateProgram, name) {
			continue
		}

		if err := defaultNameAgainst(raw, name); err != nil {
			return err
		}
	}

	return nil
}

// defaultNameAgainst refuses one added name that an owner entry repeats as a folder or that a test_paths glob matches.
func defaultNameAgainst(raw rawProject, name string) error {
	for _, entry := range raw.GateProgram {
		if gateProgramPair(raw.At, entry, name) != nil {
			return refuseAt(raw.At, "The gate tools read the file %q, so it joins `gate_program`, and `gate_program` entry %q names it as a directory or a path inside it. Remove the entry or fix its path.", name, entry)
		}
	}

	for _, glob := range raw.TestPaths {
		if gateProgramOverlaps(name, glob) {
			return refuseAt(raw.At, "The gate tools read %q, so it joins `gate_program`, and `test_paths` entry %q matches it. A new test file would count as a change of the gate program. Narrow the glob.", name, glob)
		}
	}

	return nil
}

// secretScanFiles are the files at the root that the secret scan of the engine reads: the ignore file of gitleaks 8.30.1, and its config.
// Once .gitleaks.toml exists, gitleaks reads the first of `.gitleaks.<ext>` in the extension order of viper v1.19.0, and only json comes before toml.
func secretScanFiles() []string {
	return []string{".gitleaks.json", ".gitleaks.toml", ".gitleaksignore"}
}

// toolTable maps a gate program to the files at the root that it reads when no option names one: the makefile names of GNU make, the config names of golangci-lint v2.15.0.
func toolTable() map[string][]string {
	makefiles := []string{"GNUmakefile", "makefile", "Makefile"}
	golangci := []string{
		".golangci.json", ".golangci.toml", ".golangci.yaml", ".golangci.yml", ".golangci.properties", ".golangci.props",
		".golangci.prop", ".golangci.hcl", ".golangci.tfvars", ".golangci.dotenv", ".golangci.env", ".golangci.ini",
	}

	return map[string][]string{"make": makefiles, "gmake": makefiles, "golangci-lint": golangci}
}

// everyToolFile are the files of every program of the tool table.
func everyToolFile() []string {
	var names []string

	for _, files := range toolTable() {
		names = append(names, files...)
	}

	return names
}
