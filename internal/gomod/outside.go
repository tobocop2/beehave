package gomod

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// newSumMode is the mode of a go.sum file that the download adds to the worktree.
	newSumMode = 0o600
	// copyMode is the mode of the folders and files of the copy: only the engine reads them.
	copyMode = 0o700
	// writeMode is the most a checksum file that the engine writes back may allow: the mode of the old file with no execute bit.
	writeMode = 0o666
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

// sum is a checksum file that the download can change: its text and mode at copy time, and whether it was there.
type sum struct {
	data  []byte
	mode  fs.FileMode
	found bool
}

// tree copies the module files of the worktree dir into the folder copy that the engine owns, so go never runs in the files the agent wrote.
// root opens the worktree and out opens the copy, so no read or write leaves either. sums holds each checksum file by its path relative to dir.
type tree struct {
	dir  string
	root *os.Root
	copy string
	out  *os.Root
	sums map[string]sum
}

// openTree makes the folder copy and opens it and the worktree dir, or returns the note of a failure.
func openTree(dir, copy string) (*tree, string) {
	made := os.Mkdir(copy, copyMode)
	out, outErr := os.OpenRoot(copy)
	root, rootErr := os.OpenRoot(dir)

	t := &tree{dir: dir, root: root, copy: copy, out: out, sums: map[string]sum{}}
	if err := cmp.Or(made, outErr, rootErr); err != nil {
		t.close()

		return nil, failed(fmt.Sprintf("the worktree cannot be opened (%v)", err))
	}

	return t, ""
}

// close closes the worktree and the copy.
func (t *tree) close() {
	for _, root := range []*os.Root{t.root, t.out} {
		if root != nil {
			_ = root.Close()
		}
	}
}

// download copies the module files, runs go mod download all in the copy with the go command that command gives for a GOWORK value, and writes
// the checksum files that it changed back into the worktree. It returns the note of a failure.
func (t *tree) download(ctx context.Context, command func(work string) goCommand) string {
	if note := t.top(); note != "" {
		return note
	}

	g := command(workFile(filepath.Join(t.copy, "go.work")))
	if note := t.locals(ctx, g); note != "" {
		return note
	}

	if _, note := g.run(ctx, "mod", "download", "all"); note != "" {
		return note
	}

	return t.back()
}

// top copies go.work, go.work.sum, go.mod and go.sum of the worktree, or returns the note that names a link.
func (t *tree) top() string {
	return cmp.Or(t.take("go.work"), t.take("go.work.sum"), t.folder("."))
}

// locals copies the go.mod file of each use folder and local replace folder, or returns the note that names a path outside the worktree, a link, or
// why go cannot read a file.
func (t *tree) locals(ctx context.Context, g goCommand) string {
	work := filepath.Join(t.copy, "go.work")
	if !exists(work) {
		return t.replaces(ctx, g, ".")
	}

	var space workspace
	if note := cmp.Or(decode(ctx, g, &space, "work", "edit", "-json", work), t.targets("go.work", ".", space.Replace)); note != "" {
		return note
	}

	for _, use := range space.Use {
		folder, note := t.local("go.work", ".", use.DiskPath)
		if note = cmp.Or(note, t.folder(folder), t.replaces(ctx, g, folder)); note != "" {
			return note
		}
	}

	return ""
}

// replaces copies the go.mod file of each local replace folder of the go.mod file in folder, or returns the note of a failure.
func (t *tree) replaces(ctx context.Context, g goCommand, folder string) string {
	var file modFile
	if note := decode(ctx, g, &file, "mod", "edit", "-json", filepath.Join(t.copy, folder, "go.mod")); note != "" {
		return note
	}

	return t.targets(filepath.Join(folder, "go.mod"), folder, file.Replace)
}

// targets copies the go.mod file of each local replace path that file names relative to base, or returns the note of a failure.
func (t *tree) targets(file, base string, replaces []replace) string {
	for _, line := range replaces {
		if line.New.Version != "" {
			continue
		}

		folder, note := t.local(file, base, line.New.Path)
		if note = cmp.Or(note, t.take(filepath.Join(folder, "go.mod"))); note != "" {
			return note
		}
	}

	return ""
}

// local returns the folder that path names relative to base, as a path inside the worktree, or the note that names a path that is absolute or outside.
func (t *tree) local(file, base, path string) (string, string) {
	folder := filepath.Join(base, path)
	if filepath.IsAbs(path) || !filepath.IsLocal(folder) {
		return "", outsideNote(filepath.Join(t.dir, file), cmp.Or(absolute(path), filepath.Join(t.dir, folder)))
	}

	return folder, ""
}

// folder copies the go.mod and go.sum files of a module folder of the worktree.
func (t *tree) folder(folder string) string {
	return cmp.Or(t.take(filepath.Join(folder, "go.mod")), t.take(filepath.Join(folder, "go.sum")))
}

// take copies the file at path, relative to the worktree, into the copy. A missing file is no copy, and a link in the path or a file that is not
// regular is the note that names it. A checksum file is kept in sums, so the lines the download adds reach the worktree.
func (t *tree) take(path string) string {
	info, link := t.lstat(path)
	if link != "" {
		return linkNote(link)
	}

	checksum := filepath.Base(path) == "go.sum" || filepath.Base(path) == "go.work.sum"
	if checksum {
		t.sums[path] = sum{mode: newSumMode}
	}

	if info == nil {
		return ""
	}

	data, note := t.copyIn(path)
	if checksum {
		t.sums[path] = sum{data: data, mode: info.Mode().Perm(), found: true}
	}

	return note
}

// copyIn copies the file at path from the worktree into the copy and returns its text, or the note of a failure.
func (t *tree) copyIn(path string) ([]byte, string) {
	data, err := t.root.ReadFile(path)
	if err = cmp.Or(err, t.out.MkdirAll(filepath.Dir(path), copyMode), t.out.WriteFile(path, data, copyMode)); err != nil {
		return nil, failed(fmt.Sprintf("%s cannot be copied (%v)", filepath.Join(t.dir, path), err))
	}

	return data, ""
}

// lstat returns the file at path with no link followed, or nil when a part of the path does not exist. The second result is the worktree path of
// the first part that is a link, or of the file when it is not regular.
func (t *tree) lstat(path string) (fs.FileInfo, string) {
	part, info := "", fs.FileInfo(nil)
	for name := range strings.SplitSeq(filepath.Clean(path), string(filepath.Separator)) {
		var err error

		part = filepath.Join(part, name)
		if info, err = t.root.Lstat(part); err != nil {
			return nil, ""
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, filepath.Join(t.dir, part)
		}
	}

	if !info.Mode().IsRegular() {
		return nil, filepath.Join(t.dir, part)
	}

	return info, ""
}

// back writes each checksum file that the download changed or added into the worktree, as a new file that replaces the old one, and returns the note
// of a failure.
func (t *tree) back() string {
	for _, path := range slices.Sorted(maps.Keys(t.sums)) {
		old := t.sums[path]

		data, err := t.out.ReadFile(path)
		if err != nil || old.found && bytes.Equal(data, old.data) {
			continue
		}

		if note := t.replace(path, data, old.mode); note != "" {
			return note
		}
	}

	return ""
}

// replace writes data to a new file beside path and renames it over path, so the write never follows a link that path became. The new file is made
// with an exclusive create, which never follows or reuses an entry at its name, and with no execute bit.
func (t *tree) replace(path string, data []byte, mode fs.FileMode) string {
	temp := path + ".beehave"

	err := t.create(temp, data, mode)
	if errors.Is(err, fs.ErrExist) {
		return inTheWayNote(filepath.Join(t.dir, path), filepath.Join(t.dir, temp))
	}

	if err == nil {
		err = t.root.Rename(temp, path)
	}

	if err != nil {
		return fmt.Sprintf("the go.sum lines that the Go module download added cannot be written to %s (%v), so the step starts with the file as it was.", filepath.Join(t.dir, path), err)
	}

	return ""
}

// create makes the new file temp in the worktree with data, or fails with fs.ErrExist when any entry, a link included, is at that name.
func (t *tree) create(temp string, data []byte, mode fs.FileMode) error {
	file, err := t.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode&writeMode)
	if err != nil {
		return err
	}

	_, err = file.Write(data)

	return cmp.Or(err, file.Close())
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

// absolute returns path when it is absolute, or "".
func absolute(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return ""
}

// outsideNote says that file names a path that is not a relative path inside the worktree, so no download ran.
func outsideNote(file, path string) string {
	return fmt.Sprintf("the Go module download did not run: %s names %s, which is outside the worktree of the item or not a relative path. The step starts with the module cache as it is. Move that module into the worktree and name it with a relative path, or remove the line.", file, path)
}

// inTheWayNote says that an entry at the temporary name temp stops the write-back of the checksum file path.
func inTheWayNote(path, temp string) string {
	return fmt.Sprintf("the go.sum lines that the Go module download added are not written to %s, because %s is in the way, so the step starts with the file as it was. Remove %s.", path, temp, temp)
}

// linkNote says that a module file of the worktree is a link or not a regular file, so no download ran.
func linkNote(path string) string {
	return fmt.Sprintf("the Go module download did not run: %s is a link or not a regular file, and the engine copies only regular module files. The step starts with the module cache as it is. Replace the link with the file.", path)
}
