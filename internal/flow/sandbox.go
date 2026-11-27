package flow

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow"
)

// sandboxMark is the constructor of the value that the sandbox builtin returns. The sandbox keyword of an agent call takes only such a value, or None.
const sandboxMark = starlark.String("sandbox")

// sandboxKeyword is the name of the sandbox keyword of an agent call and of a command call.
const sandboxKeyword = starlark.String("sandbox")

// recordedSandbox is the name of the output of the render step that holds the sandbox the step runs with.
const recordedSandbox = "sandbox"

// sandboxChoice is the sandbox keyword of an agent call as written: absent, None, or a sandbox value.
type sandboxChoice struct {
	// set is true when the call gives the keyword. box is nil for None.
	set bool
	box *model.Sandbox
}

// sandbox is the builtin sandbox(network = [...]). It checks the list and returns a value for the sandbox keyword of an agent call.
func (s *script) sandbox(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var network starlark.Value

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"network": &network}); err != nil {
		return nil, err
	}

	box, err := boxOf(network)
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromKeywords(sandboxMark, []starlark.Tuple{{starlark.String("network"), stringList(box.Network)}}), nil
}

func stringList(items []string) *starlark.List {
	values := make([]starlark.Value, len(items))
	for i, item := range items {
		values[i] = starlark.String(item)
	}

	return starlark.NewList(values)
}

// boxOf reads the network list of a sandbox call.
func boxOf(network starlark.Value) (*model.Sandbox, error) {
	domains, err := workflow.NetworkDomains(network)
	if err != nil {
		return nil, err
	}

	return &model.Sandbox{Network: domains}, nil
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

	box, err := boxOf(fields["network"])

	return sandboxChoice{set: true, box: box}, err
}

// errUnsandboxed is returned for a step that sets sandbox = None in a run whose project has the sandbox on.
var errUnsandboxed = errors.New(`the step sets sandbox = None, and the project runs its agent steps in the sandbox. The step did not start, and the run failed. Set sandbox = "off" in the project, or give the step sandbox(network = [...]) in the workflow file. Then run beehest cancel <item>, and run beehest run <item>`)

// UsedSandbox returns the sandbox mode that the run uses: the stored mode, or off for a run recorded before the mode was stored.
// The engine and the lines that name the off mode all call it, so the line and the behavior of a run cannot differ.
func (in ItemInput) UsedSandbox() model.SandboxMode {
	if in.Sandbox != "" {
		return in.Sandbox
	}

	return model.SandboxOff
}

// runMode returns the sandbox mode that the run uses.
func (s *script) runMode() model.SandboxMode { return s.in.UsedSandbox() }

// refusedAtCall returns the refusal of an agent call before the engine writes the start line of the step. Any other call, and a call that the agent step itself rejects, gives nil.
func (s *script) refusedAtCall(b *starlark.Builtin, kwargs []starlark.Tuple) error {
	if b.Name() != builtinAgent {
		return nil
	}

	for _, pair := range kwargs {
		if key, ok := pair[0].(starlark.String); ok && key == sandboxKeyword {
			choice, err := readSandbox(pair[1])
			if err != nil {
				return nil
			}

			return choice.refused(s.runMode())
		}
	}

	return nil
}

// refused returns errUnsandboxed for a None in a run with the sandbox on, and nil for any other choice.
func (c sandboxChoice) refused(mode model.SandboxMode) error {
	if c.set && c.box == nil && mode == model.SandboxOn {
		return errUnsandboxed
	}

	return nil
}

// effective returns the sandbox the step runs with: the value of the call, or for a call without the keyword the default of the run.
// The default is a sandbox with no domain and no extra path. nil means no sandbox.
func (c sandboxChoice) effective(mode model.SandboxMode) *model.Sandbox {
	switch {
	case c.set:
		return c.box
	case mode == model.SandboxOn:
		return &model.Sandbox{}
	}

	return nil
}

// recordedOutputFile is the name of the output of the render step that holds the output file that the recorded prompt names.
const recordedOutputFile = "output_file"

// recordedStartHead is the name of the output of the render step that holds the commit of the item folder before the agent starts.
const recordedStartHead = "start_head"

// recordedLogin is the name of the output of the render step that holds the runner that the call runs on: the pool pick, or the runner that the call names.
// recordedModel holds the model that the call sets, and is left out for a call that sets none. recordedLabel holds the label of the call, and is left out for a call that sets none.
// The view of a run reads all three from the render step, so a running or a failed step shows them.
const (
	recordedLogin = "login"
	recordedModel = "model"
	recordedLabel = "label"
)

// recalledOutputFile reads the output file that the render step recorded, which a replay keeps. A record made before the path was pinned has none, and the caller keeps the computed path.
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

	raw, _ := json.Marshal(box) //nolint:errcheck // a list of strings always encodes

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

// recordedPromptData is the name of the output of the render step that holds the data of a call. A call with no data records none.
// The name sorts after the login and the model, so the clipped line of a dry run still shows both.
const recordedPromptData = "prompt_data"

// promptData is the recorded data of a call: the file that the recorded prompt names, and the JSON text that the phase writes to it.
type promptData struct {
	File string `json:"file"`
	Text string `json:"text"`
}

// recordData writes the data of a call and its file as the output of the render step, in the text a replay reads. A call with no data writes nothing.
func recordData(data promptData) map[string]any {
	if data.File == "" {
		return nil
	}

	raw, _ := json.Marshal(data) //nolint:errcheck // two strings always encode

	return map[string]any{recordedPromptData: string(raw)}
}

// recalledData reads the data that the render step recorded. A record without it is a call with no data.
func recalledData(outputs map[string]any) (promptData, error) {
	raw, ok := outputs[recordedPromptData].(string)
	if !ok {
		return promptData{}, nil
	}

	var data promptData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return promptData{}, fmt.Errorf("read the recorded data of the step: %w", err)
	}

	return data, nil
}
