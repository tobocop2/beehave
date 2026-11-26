package config

import (
	"path"
	"slices"
	"strings"

	shell "mvdan.cc/sh/v3/syntax"
)

// effectiveGateProgram is the sorted owner gate_program joined with the files that the gate tools read. An empty owner list stays empty.
func effectiveGateProgram(raw rawProject) []string {
	if len(raw.GateProgram) == 0 {
		return nil
	}

	names := append(secretScanFiles(), raw.GateProgram...)

	for _, command := range gateCommands(raw) {
		for _, program := range programsOf(command) {
			names = append(names, toolFiles(program)...)
		}
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

// programsOf is the base name of the first word of each simple command in the text. A text that does not parse names no program.
func programsOf(command string) []string {
	file, err := shell.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	var programs []string

	shell.Walk(file, func(node shell.Node) bool {
		// Walk visits every node of the tree, and only a call expression names a program.
		if call, isCall := node.(*shell.CallExpr); isCall && len(call.Args) > 0 {
			programs = append(programs, path.Base(call.Args[0].Lit()))
		}

		return true
	})

	return programs
}

// secretScanFiles are the files at the root that the secret scan of the engine reads: the config and the ignore file of gitleaks 8.30.1.
func secretScanFiles() []string { return []string{".gitleaks.toml", ".gitleaksignore"} }

// toolFiles are the files at the root that a gate program reads when no option names one: the makefile names of GNU make, the config names of golangci-lint v2.15.0.
func toolFiles(program string) []string {
	switch program {
	case "make", "gmake":
		return []string{"GNUmakefile", "makefile", "Makefile"}
	case "golangci-lint":
		return []string{
			".golangci.json", ".golangci.toml", ".golangci.yaml", ".golangci.yml", ".golangci.properties", ".golangci.props",
			".golangci.prop", ".golangci.hcl", ".golangci.tfvars", ".golangci.dotenv", ".golangci.env", ".golangci.ini",
		}
	}

	return nil
}
