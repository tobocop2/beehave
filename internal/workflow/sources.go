// Package workflow reads a Starlark workflow file with the files it loads, pins them by hash and checks them.
// It runs no workflow code and imports no workflow engine.
package workflow

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/model"
)

const (
	localPrefix    = "//"
	embeddedPrefix = "@obee//"
	librarySuffix  = ".star"
	libraryFolder  = "lib"
)

//go:embed lib/*.star
var library embed.FS

// PinnedFile is one file of a run and its SHA-256, in hex. Path is relative to the folder of the workflow file, or starts with @obee// for a library in the binary.
type PinnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Builtins returns the names that the engine predeclares in every workflow file. All are steps or helpers except json, the module of the Starlark library.
func Builtins() []string {
	return []string{agentName, batchMergeName, boardSyncName, commandName, earlierCallsName, "env", factsName, gateName, gatherName, hostTeardownName, hostUpName, itemAssignName, itemCommentName, itemCreateName, itemReadName, itemStatusName, "json", mergeName, "now", prAssignName, prCommentName, prOpenName, preflightName, queueName, sandboxName, spawnName, "struct", waitName, writeName}
}

// resolveModule turns the argument of a load into a file name: a path in the folder of the workflow, or a library in the binary.
func resolveModule(module string) (string, error) {
	if name, ok := strings.CutPrefix(module, embeddedPrefix); ok {
		if _, err := library.ReadFile(path.Join(libraryFolder, name)); err != nil || path.Base(name) != name {
			return "", fmt.Errorf("the binary has no library %q. Load a library such as %sreview_fix.star", module, embeddedPrefix)
		}

		return module, nil
	}

	rel, local := strings.CutPrefix(module, localPrefix)
	if !local || !fs.ValidPath(rel) || !strings.HasSuffix(rel, librarySuffix) {
		return "", fmt.Errorf("cannot load %q. Load a file of the workflow folder such as \"%slib.star\", or a library such as \"%sreview_fix.star\"", module, localPrefix, embeddedPrefix)
	}

	return rel, nil
}

// readFile reads a pinned file by its name: from the folder, or from the binary.
func readFile(dir, name string) ([]byte, error) {
	if script, ok := strings.CutPrefix(name, scriptPrefix); ok {
		return fs.ReadFile(os.DirFS(repoRoot(dir)), script)
	}

	if lib, ok := strings.CutPrefix(name, embeddedPrefix); ok {
		return library.ReadFile(path.Join(libraryFolder, lib))
	}

	return fs.ReadFile(os.DirFS(dir), name)
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// Pin reads the workflow file and every file it loads, and returns their hashes sorted by path. It runs no code.
func Pin(file string) ([]PinnedFile, error) {
	g := scan(file)
	if err := g.failure(); err != nil {
		return nil, err
	}

	pinned := make([]PinnedFile, 0, len(g.units))
	for name, u := range g.units {
		pinned = append(pinned, PinnedFile{Path: name, SHA256: hashOf(u.data)})
	}

	scripts, err := g.pinScripts()
	if err != nil {
		return nil, err
	}

	pinned = append(pinned, scripts...)

	slices.SortFunc(pinned, func(a, b PinnedFile) int { return strings.Compare(a.Path, b.Path) })

	return pinned, nil
}

// failure joins the problems that the scan found, and returns nil when it found none.
func (g *graph) failure() error {
	errs := make([]error, len(g.found))
	for i, d := range g.found {
		errs[i] = d
	}

	return errors.Join(errs...)
}

// pinScripts returns the hashes of the scripts of the repository that the command calls of the workflow run.
func (g *graph) pinScripts() ([]PinnedFile, error) {
	inside, _ := g.scripts()
	pinned := make([]PinnedFile, 0, len(inside))

	for _, name := range inside {
		data, err := readFile(g.dir, scriptPrefix+name)
		if err != nil {
			return nil, fmt.Errorf("read the script %s: %w", name, err)
		}

		pinned = append(pinned, PinnedFile{Path: scriptPrefix + name, SHA256: hashOf(data)})
	}

	return pinned, nil
}

// Sources holds the bytes of the files that a run pinned. A run executes these bytes and no others.
type Sources struct {
	main    string
	dir     string
	data    map[string][]byte
	modules map[string]starlark.StringDict
}

// readPinned reads a pinned file and compares its hash with the pin.
func readPinned(dir string, p PinnedFile) ([]byte, error) {
	data, err := readFile(dir, p.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w. Restore the file, or cancel the run and start again", p.Path, err)
	}

	if got := hashOf(data); got != p.SHA256 {
		return nil, fmt.Errorf("%s changed since the run started: it had hash %s and has hash %s. Restore the file, or cancel the run and start again", p.Path, p.SHA256, got)
	}

	return data, nil
}

// Open reads each pinned file once and compares its hash with the pin. A file that changed is an error, and nothing runs.
func Open(file string, pinned []PinnedFile) (*Sources, error) {
	dir := filepath.Dir(file)
	s := &Sources{main: filepath.Base(file), dir: dir, data: map[string][]byte{}, modules: map[string]starlark.StringDict{}}

	for _, p := range pinned {
		data, err := readPinned(dir, p)
		if err != nil {
			return nil, err
		}

		s.data[p.Path] = data
	}

	if _, ok := s.data[s.main]; !ok {
		return nil, fmt.Errorf("the run did not pin %s. Cancel the run and start again", s.main)
	}

	return s, nil
}

// Entry runs the top level of the workflow file on thread and returns its function name, which is main for an item that a user started
// and the entry of a spawn call for an item that spawn started. Loads are served from the pinned bytes.
func (s *Sources) Entry(thread *starlark.Thread, predeclared starlark.StringDict, name string) (starlark.Value, error) { //nolint:ireturn // a global of a file can be any value
	thread.Load = func(t *starlark.Thread, module string) (starlark.StringDict, error) {
		return s.load(t, module, predeclared)
	}

	globals, err := s.exec(thread, s.main, predeclared)
	if err != nil {
		return nil, err
	}

	entry, ok := globals[name]
	if !ok {
		return nil, fmt.Errorf("%s defines no function %s; add def %s(item)", s.main, name, name)
	}

	return entry, nil
}

// Defines reports whether the workflow file has a function of that name at the top level. It reads the globals that Entry made, so it is false before Entry ran.
func (s *Sources) Defines(name string) bool {
	_, ok := s.modules[s.main][name].(starlark.Callable)

	return ok
}

func (s *Sources) load(thread *starlark.Thread, module string, predeclared starlark.StringDict) (starlark.StringDict, error) {
	name, err := resolveModule(module)
	if err != nil {
		return nil, err
	}

	if done, ok := s.modules[name]; ok {
		return done, nil
	}

	if _, ok := s.data[name]; !ok {
		return nil, fmt.Errorf("the run did not pin %q. Cancel the run and start again", module)
	}

	return s.exec(thread, name, predeclared)
}

func (s *Sources) exec(thread *starlark.Thread, name string, predeclared starlark.StringDict) (starlark.StringDict, error) {
	globals, err := starlark.ExecFileOptions(model.StarlarkFileOptions(), thread, name, s.data[name], predeclared)
	if err != nil {
		return nil, err
	}

	globals.Freeze()
	s.modules[name] = globals

	return globals, nil
}
