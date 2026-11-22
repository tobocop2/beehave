package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"text/template"

	"github.com/tobocop2/beehave/internal/model"
)

// skillFunction is the name of the template function that declares a skill of a prompt.
const skillFunction = "skill"

// recordedSkills is the name of the output of the render step that holds the skills that the prompt declares. A prompt that declares none records none.
const recordedSkills = "skills"

// skillDeclarations collects the skills that a prompt declares through the template function skill, each once, in the order of the first call.
type skillDeclarations struct {
	names []string
}

// funcs is the function map of the prompt template: skill renders the plain name of a skill and declares it.
func (d *skillDeclarations) funcs() template.FuncMap {
	return template.FuncMap{skillFunction: d.declare}
}

// declare returns the name of a skill that the prompt orders, and keeps it for the step. A name that is no folder name is an error.
func (d *skillDeclarations) declare(name string) (string, error) {
	if err := model.CheckSkill(name); err != nil {
		return "", err
	}

	if !slices.Contains(d.names, name) {
		d.names = append(d.names, name)
	}

	return name, nil
}

// recordSkills writes the skills of a step as the output of the render step, in the text a replay reads. A step with no skill writes nothing.
func recordSkills(names []string) map[string]any {
	if len(names) == 0 {
		return nil
	}

	raw, _ := json.Marshal(names) //nolint:errcheck // a list of strings always encodes

	return map[string]any{recordedSkills: string(raw)}
}

// recalledSkills reads the skills that the render step recorded. A record without them is a step with no skill.
func recalledSkills(outputs map[string]any) ([]string, error) {
	raw, ok := outputs[recordedSkills].(string)
	if !ok {
		return nil, nil
	}

	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("read the recorded skills of the step: %w", err)
	}

	return names, nil
}
