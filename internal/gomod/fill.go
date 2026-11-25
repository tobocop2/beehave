// Package gomod fills the Go module cache of a project with the network of the engine, so a sandboxed step builds with no network.
package gomod

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/gotelemetry"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	// proxy is the only module host the download reaches. It has no direct fallback, so go never runs git against a host that a go.mod names.
	proxy = "https://proxy.golang.org"
	// fillTimeout is the longest one download may run.
	fillTimeout = 10 * time.Minute
	// vendorList is the file that turns on vendor mode, in which go reads no module cache.
	vendorList = "vendor/modules.txt"
	// cacheMode is the mode of the cache folder of a project: only the owner reads it.
	cacheMode = 0o700
)

// Filler fills one module cache per project under Root, with the go command that Path finds.
type Filler struct {
	Root string
	Path string
}

// Fill runs go mod download all on a copy of the module files of the item worktree dir and returns the module cache of the project, or "" for a folder with no go.mod and no go.work,
// or with a vendor folder. The note says why the cache may be incomplete; the step starts with the cache as it is.
func (f Filler) Fill(ctx context.Context, project, dir string) (string, string) {
	cache := f.Cache(project, dir)
	if cache == "" {
		return "", ""
	}

	if err := os.MkdirAll(cache, cacheMode); err != nil {
		return "", fmt.Sprintf("the Go module cache %s cannot be made (%v), so the step starts with no module cache.", cache, err)
	}

	return cache, f.download(ctx, cache, dir)
}

// Cache returns the module cache of the project for the item worktree dir, or "" for a folder with no go.mod and no go.work, or with a vendor folder.
// It reads no file and makes no folder.
func (f Filler) Cache(project, dir string) string {
	if !exists(filepath.Join(dir, "go.work")) && !exists(filepath.Join(dir, "go.mod")) || exists(filepath.Join(dir, vendorList)) {
		return ""
	}

	return filepath.Join(f.Root, url.PathEscape(project))
}

// download copies the module files of the worktree into a folder of an empty home that it removes after, runs the download there, and writes the
// go.sum lines it added back into the worktree. go never runs in the files the agent wrote. It returns the note of a failure.
func (f Filler) download(ctx context.Context, cache, dir string) string {
	home, remove, err := emptyHome(f.Root)
	defer remove()

	if err != nil {
		return failed(fmt.Sprintf("its empty home folder cannot be made (%v)", err))
	}

	binary, err := proc.LookPath("go", f.Path)
	if err != nil {
		return failed("the engine finds no go command on its PATH")
	}

	t, note := openTree(dir, filepath.Join(home, "tree"))
	if note != "" {
		return note
	}
	defer t.close()

	return t.download(ctx, f.command(binary, t.copy, cache, home))
}

// command returns the function that gives the go command in the folder dir for a GOWORK value.
func (f Filler) command(binary, dir, cache, home string) func(work string) goCommand {
	return func(work string) goCommand { return goCommand{binary: binary, dir: dir, env: f.env(cache, home, work)} }
}

// emptyHome makes the empty home folder of one download in root, with the telemetry of the go command off. It returns the function that removes
// the folder, which the caller runs whatever the error.
func emptyHome(root string) (string, func(), error) {
	home, err := os.MkdirTemp(root, ".home-")
	if err != nil {
		return "", func() {}, err
	}

	return home, func() { _ = os.RemoveAll(home) }, gotelemetry.Off(home)
}

// env is the whole environment of the go command: no variable of the owner reaches it.
func (f Filler) env(cache, home, work string) []string {
	return []string{
		"PATH=" + f.Path, "HOME=" + home, "GOENV=off", "GOFLAGS=-mod=readonly", "GOPROXY=" + proxy, "GOTOOLCHAIN=local",
		"GOMODCACHE=" + cache, "GOCACHE=" + filepath.Join(home, "build"), "GOWORK=" + work, "XDG_CONFIG_HOME=" + filepath.Join(home, gotelemetry.ConfigFolder),
	}
}

// workFile returns the go.work file of the worktree for GOWORK, or off, so a go.work in a parent folder is not used.
func workFile(work string) string {
	if exists(work) {
		return work
	}

	return "off"
}

// goCommand runs the go command in the folder dir with the fixed environment.
type goCommand struct {
	binary string
	dir    string
	env    []string
}

// run starts go with the arguments and returns what it printed, or the note of a failure.
func (g goCommand) run(ctx context.Context, args ...string) (string, string) {
	result := proc.ExecRunner{Binary: g.binary, Dir: g.dir, Timeout: fillTimeout, Only: g.env}.Run(ctx, args)
	if result.ExitCode != 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(result.Stderr), "\n")
		return "", failed(cmp.Or(first, fmt.Sprintf("go exited with exit code %d", result.ExitCode)))
	}

	return result.Stdout, ""
}

// failed is the note of a download that did not run or did not finish.
func failed(reason string) string {
	return "the Go module download failed, so the step starts with the module cache as it is: " + reason
}

// exists reports whether a file or folder is at path.
func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
