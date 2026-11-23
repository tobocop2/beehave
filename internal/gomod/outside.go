package gomod

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// replace is one replace line of a go.mod or a go.work file, as go mod edit -json prints it. A local replace has no version.
type replace struct {
	New struct {
		Path    string
		Version string
	}
}

// modFile is the part of go mod edit -json that names local paths.
type modFile struct {
	Replace []replace
}

// workspace is the part of go work edit -json that names local paths.
type workspace struct {
	Use []struct {
		DiskPath string
	}
	Replace []replace
}

// outside returns the note that names the first use path or local replace path outside the worktree dir, or why go cannot read the files, or "".
func (g goCommand) outside(ctx context.Context, dir, work string) string {
	root := resolve(dir, ".")

	mods, note := g.modFiles(ctx, dir, work, root)
	if note != "" {
		return note
	}

	for _, mod := range mods {
		if note := g.localReplace(ctx, root, mod); note != "" {
			return note
		}
	}

	return ""
}

// localReplace returns the note that names the first local replace path of the go.mod file mod outside root, or why go cannot read the file, or "".
func (g goCommand) localReplace(ctx context.Context, root, mod string) string {
	var file modFile
	if note := decode(ctx, g, &file, "mod", "edit", "-json", mod); note != "" {
		return note
	}

	return escapes(root, mod, filepath.Dir(mod), file.Replace)
}

// modFiles returns the go.mod file of the worktree, or the go.mod file of each use folder of its go.work, or the note that names a path outside the worktree.
func (g goCommand) modFiles(ctx context.Context, dir, work, root string) ([]string, string) {
	if !exists(work) {
		return []string{filepath.Join(dir, "go.mod")}, ""
	}

	var space workspace
	if note := cmp.Or(decode(ctx, g, &space, "work", "edit", "-json", work), escapes(root, work, dir, space.Replace)); note != "" {
		return nil, note
	}

	return useFiles(root, dir, work, space)
}

// useFiles returns the go.mod file of each use folder of the workspace, or the note that names the first use folder outside root.
func useFiles(root, dir, work string, space workspace) ([]string, string) {
	mods := make([]string, 0, len(space.Use))

	for _, use := range space.Use {
		folder := resolve(dir, use.DiskPath)
		if !inside(root, folder) {
			return nil, outsideNote(work, folder)
		}

		mods = append(mods, filepath.Join(folder, "go.mod"))
	}

	return mods, ""
}

// decode runs a go edit command and decodes the JSON it prints into value, or returns the note of a failure.
func decode[P *modFile | *workspace](ctx context.Context, g goCommand, value P, args ...string) string {
	out, note := g.run(ctx, args...)
	if note != "" {
		return note
	}

	if err := json.Unmarshal([]byte(out), value); err != nil {
		return failed(fmt.Sprintf("go %s printed no JSON (%v)", strings.Join(args, " "), err))
	}

	return ""
}

// escapes returns the note that names the first local replace path of file, relative to base, that is outside root, or "".
func escapes(root, file, base string, replaces []replace) string {
	for _, line := range replaces {
		if line.New.Version != "" {
			continue
		}

		if path := resolve(base, line.New.Path); !inside(root, path) {
			return outsideNote(file, path)
		}
	}

	return ""
}

// resolve returns path, taken from base when it is relative, with its links resolved. A path that does not exist is cleaned only.
func resolve(base, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}

	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}

	return filepath.Clean(path)
}

// inside reports whether path is root or a path under it.
func inside(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// outsideNote says that file names a path outside the worktree, so no download ran.
func outsideNote(file, path string) string {
	return fmt.Sprintf("the Go module download did not run: %s names %s, which is outside the worktree of the item. The step starts with the module cache as it is. Move that module into the worktree, or remove the line.", file, path)
}
