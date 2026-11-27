package config

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehest/internal/model"
)

const credentialSpecName = "credential"

// credentialSpec is the schema of the credential constructor.
func credentialSpec() spec {
	return spec{credentialSpecName, []string{"file?", "command?", "ci_locked?"}}
}

// credentials is the credentials argument of the project call: a dict from a role name to a credential(...) block.
type credentials map[model.Role]model.Credential

// Unpack stores the dict. It refuses any other value, a key that is not a role that the engine knows, and a value that is not a credential(...) block.
func (c *credentials) Unpack(v starlark.Value) error {
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("got %s, want a dict such as {\"writer\": credential(file = ...)}", describe(v))
	}

	found := credentials{}

	for _, item := range dict.Items() {
		role, block, err := entry(item[0], item[1])
		if err != nil {
			return err
		}

		found[role] = block
	}

	*c = found

	return nil
}

// entry checks one key and value of the dict and marks the block as held.
func entry(key, val starlark.Value) (model.Role, model.Credential, error) {
	role, isText := starlark.AsString(key)
	if !isText || model.Role(role) != model.RoleWriter {
		return "", model.Credential{}, fmt.Errorf("the key %s is not a role. The only role is %q", key, model.RoleWriter)
	}

	held, isBlock := val.(*value[model.Credential])
	if !isBlock {
		return "", model.Credential{}, fmt.Errorf("the role %q got %s, want credential(...)", role, describe(val))
	}

	held.made.held = true

	return model.Role(role), *held.block, nil
}

// credentialBuiltin makes the credential constructor. It builds a block that the credentials argument of the project call holds.
func credentialBuiltin(file *loader) *starlark.Builtin {
	return file.builtin(credentialSpecName, false, func(at syntax.Position, kwargs []starlark.Tuple) (starlark.Value, error) {
		block, err := newCredential(at, kwargs)
		record := file.record(at, credentialSpecName)
		record.owner = "project"
		record.usage = `credentials = {"writer": credential(...)}`
		file.made = append(file.made, record)

		return &value[model.Credential]{block: block, made: record}, err
	})
}

// newCredential reads the arguments of a credential call. It takes exactly one of file and command.
func newCredential(at syntax.Position, kwargs []starlark.Tuple) (*model.Credential, error) {
	var path text

	var words command

	var locked truth

	if err := credentialSpec().unpack(at, kwargs, &path, &words, &locked); err != nil {
		return nil, err
	}

	switch {
	case path.Set == words.Set:
		return nil, refuseAt(at, "`credential` takes exactly one of `file` and `command`. Set `file` to a token file or `command` to a program that prints the token.")
	case path.Set && strings.TrimSpace(path.Value) == "":
		return nil, refuseAt(at, "`file` of `credential` is empty. Name the token file.")
	case words.Set && (len(words.Words) == 0 || slices.Contains(words.Words, "")):
		return nil, refuseAt(at, "`command` of `credential` holds no program or an empty word. Write the program and its arguments.")
	}

	return &model.Credential{File: path.Value, Command: slices.Clone(words.Words), CILocked: locked.Value}, nil
}
