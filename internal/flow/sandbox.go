package flow

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// sandboxMark is the constructor of the value that the sandbox builtin returns. The sandbox keyword of an agent call takes only such a value, or None.
const sandboxMark = starlark.String("sandbox")

// recordedSandbox is the name of the output of the render step that holds the sandbox the step runs with.
const recordedSandbox = "sandbox"

// sandboxChoice is the sandbox keyword of an agent call as written: absent, None, or a sandbox value.
type sandboxChoice struct {
	// set is true when the call gives the keyword. box is nil for None.
	set bool
	box *model.Sandbox
}

// sandbox is the builtin sandbox(network = [...], allow_write = [...]). It checks the lists and returns a value for the sandbox keyword of an agent call.
func (s *script) sandbox(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var network, allow starlark.Value

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"network": &network, "allow_write": &allow}); err != nil {
		return nil, err
	}

	box, err := boxOf(network, allow)
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromKeywords(sandboxMark, []starlark.Tuple{
		{starlark.String("network"), stringList(box.Network)},
		{starlark.String("allow_write"), stringList(box.AllowWrite)},
	}), nil
}

func stringList(items []string) *starlark.List {
	values := make([]starlark.Value, len(items))
	for i, item := range items {
		values[i] = starlark.String(item)
	}

	return starlark.NewList(values)
}

// boxOf reads the two lists of a sandbox call.
func boxOf(network, allow starlark.Value) (*model.Sandbox, error) {
	domains, err := words("network", network)
	if err != nil {
		return nil, err
	}

	paths, err := words("allow_write", allow)
	if err != nil {
		return nil, err
	}

	if err = errors.Join(model.CheckNetwork(domains), model.CheckAllowWrite(paths)); err != nil {
		return nil, err
	}

	return &model.Sandbox{Network: domains, AllowWrite: paths}, nil
}

// readSandbox reads the sandbox keyword of an agent call. A nil value is a call that leaves the keyword out.
func readSandbox(v starlark.Value) (sandboxChoice, error) {
	switch value := v.(type) {
	case nil:
		return sandboxChoice{}, nil
	case starlark.NoneType:
		return sandboxChoice{set: true}, nil
	case *starlarkstruct.Struct:
		if value.Constructor() == sandboxMark {
			return sandboxFromStruct(value)
		}
	}

	return sandboxChoice{}, errors.New("sandbox must be sandbox(...) or None. Write sandbox = sandbox(network = [...]) to turn the sandbox on, or leave the keyword out to use the default of the project")
}

func sandboxFromStruct(value *starlarkstruct.Struct) (sandboxChoice, error) {
	fields := starlark.StringDict{}
	value.ToStringDict(fields)

	box, err := boxOf(fields["network"], fields["allow_write"])

	return sandboxChoice{set: true, box: box}, err
}

// effective returns the sandbox the step runs with: the value of the call, or for a call without the keyword the default of the project.
// The default is a sandbox with no domain and no extra path. nil means no sandbox.
func (c sandboxChoice) effective(projectDefault bool) *model.Sandbox {
	switch {
	case c.set:
		return c.box
	case projectDefault:
		return &model.Sandbox{}
	}

	return nil
}

// sandboxDefault reports whether the project of the run sets the sandbox on by default. A build that sets no default gives false.
func (s *script) sandboxDefault() bool {
	return s.e.deps.SandboxDefault != nil && s.e.deps.SandboxDefault(s.in.Project)
}

// recordedOutputFile is the name of the output of the render step that holds the output file that the recorded prompt names.
const recordedOutputFile = "output_file"

// recalledOutputFile reads the output file that the render step recorded. A record made before the path was pinned has none, and the caller keeps the computed path.
func recalledOutputFile(outputs map[string]any, computed string) string {
	if file, ok := outputs[recordedOutputFile].(string); ok && file != "" {
		return file
	}

	return computed
}

// recordSandbox writes the sandbox of a step as the output of the render step, in the text a replay reads. No sandbox writes nothing.
func recordSandbox(box *model.Sandbox) map[string]any {
	if box == nil {
		return nil
	}

	raw, _ := json.Marshal(box) //nolint:errcheck // two lists of strings always encode

	return map[string]any{recordedSandbox: string(raw)}
}

// recalledSandbox reads the sandbox that the render step recorded. A record without it is a step with no sandbox.
func recalledSandbox(outputs map[string]any) (*model.Sandbox, error) {
	raw, ok := outputs[recordedSandbox].(string)
	if !ok {
		return nil, nil //nolint:nilnil // a record without the value replays with no sandbox
	}

	var box model.Sandbox
	if err := json.Unmarshal([]byte(raw), &box); err != nil {
		return nil, fmt.Errorf("read the recorded sandbox of the step: %w", err)
	}

	return &box, nil
}
