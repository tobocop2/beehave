// Package srt runs a command under a sandbox plan with srt, the command of the sandbox runtime that Anthropic publishes
// (npm package @anthropic-ai/sandbox-runtime) and that Claude Code is built on. The package maps a SandboxPlan to the settings file of srt,
// so a command that is not an agent shell, such as the gate of a project, gets the same denied reads, write list and network list.
// Seatbelt (macOS) or bubblewrap (Linux) enforces the policy; obee writes no profile of its own.
package srt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/tobocop2/obee/internal/agent"
)

// Program is the name of the srt command.
const Program = "srt"

type settings struct {
	Network    network    `json:"network"`
	Filesystem filesystem `json:"filesystem"`
}

// network holds both lists because srt 0.0.81 rejects a file without deniedDomains
// (dist/sandbox/sandbox-config.js:584-593, NetworkConfigSchema: allowedDomains and deniedDomains have no .optional()).
// The same schema requires denyRead, allowWrite and denyWrite in the filesystem block (lines 807-838), and obee always writes all three.
type network struct {
	AllowedDomains []string `json:"allowedDomains"`
	DeniedDomains  []string `json:"deniedDomains"`
}

type filesystem struct {
	DenyRead   []string `json:"denyRead"`
	AllowRead  []string `json:"allowRead"`
	DenyWrite  []string `json:"denyWrite"`
	AllowWrite []string `json:"allowWrite"`
}

// Settings returns the settings file of srt for a plan. home is the home folder, which is unreadable except for the paths that the plan allows.
// temp is the temporary folder, which the command may write. srt reads everything by default and writes nothing, so the file lists the home
// folder for denied reads, and the work folder, the temporary folder and the allow-write paths of the plan for writes. The shared git directories are
// readable and never writable. A pattern of the plan is a denied read only, because srt takes literal paths for a denied write on Linux.
func Settings(plan agent.SandboxPlan, home, temp string) []byte {
	out := settings{
		Network: network{AllowedDomains: join(plan.Network), DeniedDomains: join()},
		Filesystem: filesystem{
			DenyRead:   join([]string{home}, plan.Deny, plan.DenyPatterns),
			AllowRead:  join([]string{plan.Work}, plan.GitDirs, plan.Read),
			DenyWrite:  join(plan.Deny, plan.DenyWrite),
			AllowWrite: join([]string{plan.Work, temp}, plan.AllowWrite),
		},
	}

	raw, _ := json.Marshal(out) //nolint:errcheck,errchkjson // a struct of strings always encodes

	return raw
}

// Wrap writes the settings file for the plan and returns the arguments of srt that run command (a line for sh) under it. The caller runs
// Program with the arguments and calls cleanup when the command has ended, which removes the file.
func Wrap(plan agent.SandboxPlan, home, command string) (args []string, cleanup func(), err error) {
	return wrap(plan, home, command, os.WriteFile)
}

// wrap is Wrap with the function that fills the settings file.
func wrap(plan agent.SandboxPlan, home, command string, write func(name string, data []byte, perm os.FileMode) error) ([]string, func(), error) {
	file, err := os.CreateTemp("", "obee-srt-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("write the sandbox settings: %w", err)
	}

	name := file.Name()
	remove := func() { _ = os.Remove(name) }

	// The file exists and is empty. A failed write removes it again.
	if err = errors.Join(file.Close(), write(name, Settings(plan, home, filepath.Clean(os.TempDir())), settingsMode)); err != nil {
		remove()

		return nil, nil, fmt.Errorf("write the sandbox settings: %w", err)
	}

	return []string{"--settings", name, "-c", command}, remove, nil
}

// settingsMode is the mode of the settings file: only the owner reads it.
const settingsMode = 0o600

// Missing returns the note that says srt is not installed, or "" when lookPath finds it.
func Missing(lookPath func(string) (string, error)) string {
	if _, err := lookPath(Program); err == nil {
		return ""
	}

	return "srt, the sandbox runtime, is not installed, so a command that must run in the sandbox does not start. " +
		"Run `npm install -g @anthropic-ai/sandbox-runtime`. It needs Node 22.12 or later. Linux also needs bubblewrap and socat, " +
		"and on Ubuntu 24.04 the setting `kernel.apparmor_restrict_unprivileged_userns=0`. macOS needs ripgrep. Or set sandbox = \"off\" in the project file."
}

// join returns the lists one after another, as a list that is never nil, so the JSON holds [] and never null.
func join(lists ...[]string) []string {
	if all := slices.Concat(lists...); all != nil {
		return all
	}

	return []string{}
}
