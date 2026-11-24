package gate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// Key names the gate that a request would run: the project (name, repository and the whole host definition), the tree it judges, the command, the test report,
// the build host, the pin file, the base commit that the engine read and, for a local gate, the version that each pinned tool reports on this machine.
// Two requests with the same ID run the same gate on the same tree, so a pass of one is a pass of the other.
// Head and Tree are the commit of the tree and its tree id. An empty ID means that no earlier pass may stand in for this gate.
type Key struct {
	ID   string
	Head string
	Tree string
}

// Key reads the key of the gate that Run would start for the request. It starts nothing.
// A tree with uncommitted changes, a pin file that cannot be read at the base commit, or a local tool that is not at its pin, gives an empty ID: Run then reports the problem.
func (g Gate) Key(ctx context.Context, request Request) (Key, error) {
	state, err := g.read(ctx, request.Dir)
	if err != nil {
		return Key{}, err
	}

	key := Key{Head: state.Head, Tree: state.Tree}
	if !state.Clean {
		return key, nil
	}

	pinned, ok := g.pinned(ctx, request)
	if !ok {
		return key, nil
	}

	project := request.Project
	key.ID = digest(project.Name, project.Repo, host(project), state.Tree, project.Gate, stepsKey(project.GateSteps), project.TestReport, hostName(project), project.PinFile, fmt.Sprintf("%q", project.GateProgram), pinned, request.anchor())

	return key, nil
}

// pinned is the pin file at the base commit and the installed tools, as one text. False means that the gate has no key.
func (g Gate) pinned(ctx context.Context, request Request) (string, bool) {
	pins, ok := g.pins(ctx, request)
	if !ok {
		return "", false
	}

	tools, ok := g.tools(ctx, request)

	return digest(pins, tools), ok
}

// host is the whole host definition of the project, so that two projects with the same host name but another machine never share a key.
func host(project model.Project) string {
	return fmt.Sprintf("%+v", project.Host)
}

// hostName is the build host that the key holds: the name of the host, or for a project that declares more than one host the whole list.
// The engine places a gate on any declared host, so a pass on one host stands for the same tree on another.
func hostName(project model.Project) string {
	if len(project.BuildHosts) > 1 {
		return fmt.Sprintf("%q", project.BuildHosts)
	}

	return project.BuildHost
}

// tools reads the version that each pinned tool reports on this machine, which is what the pin check of a local gate reads.
// False means that the pin check would void the gate (the file cannot be parsed, a tool is missing or not at its pin): the project then has no key, so the gate runs and voids.
// A gate on a build host does not run the pin check here, so it holds no installed versions.
func (g Gate) tools(ctx context.Context, request Request) (string, bool) {
	project := request.Project
	if project.PinFile == "" || project.BuildHost != config.LocalHost {
		return "", true
	}

	check := g.pinsOf(ctx, request)
	if check.Void != "" {
		return "", false
	}

	return g.versions(ctx, request.Dir, check.Tools)
}

func (g Gate) versions(ctx context.Context, dir string, pins []Pin) (string, bool) {
	var versions []string

	for _, p := range pins {
		output, code := g.installed(ctx, dir, p)
		if !atPin(p, output, code) {
			return "", false
		}

		versions = append(versions, p.Tool, output)
	}

	return digest(versions...), true
}

// digest hashes the parts with their lengths, so that two lists of parts never give the same text.
// stepsKey is the text of the gate list in the key: each step as its quoted name and command, and its timeout only when it has one, so a list
// with no timeouts keeps the text, and so the key, that it had before steps had timeouts.
func stepsKey(steps []model.GateSpec) string {
	parts := make([]string, 0, len(steps))

	for _, spec := range steps {
		if spec.Timeout == 0 {
			parts = append(parts, fmt.Sprintf("%q", struct{ Name, Command string }{spec.Name, spec.Command}))

			continue
		}

		parts = append(parts, fmt.Sprintf("%q", struct{ Name, Command, Timeout string }{spec.Name, spec.Command, spec.Timeout.String()}))
	}

	return "[" + strings.Join(parts, " ") + "]"
}

func digest(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(sum, "%d:%s;", len(part), part)
	}

	return hex.EncodeToString(sum.Sum(nil))
}

// pins reads the pin file of the project at the base commit. A project with no pin file has empty pins. False means that the file cannot be read.
func (g Gate) pins(ctx context.Context, request Request) (string, bool) {
	file := request.Project.PinFile
	if file == "" {
		return "", true
	}

	data, code := g.pinText(ctx, request, file)

	return data, code == 0
}
