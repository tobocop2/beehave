package main

import (
	"context"
	"debug/macho"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
)

// gitLibraries returns the libraries that the program at binary loads, and the libraries that they load, with their links resolved. A library of the
// shared cache is left out. A library named relative to a program (@rpath and the like) is an error: the copy cannot know where it lies.
func gitLibraries(_ context.Context, binary string) ([]string, error) {
	seen := map[string]bool{binary: true}
	queue := []string{binary}

	for next := 0; next < len(queue); next++ {
		found, err := newLibraries(queue[next], seen)
		if err != nil {
			return nil, err
		}

		queue = append(queue, found...)
	}

	return queue[1:], nil
}

// newLibraries returns the libraries that the file at path loads and that seen does not hold yet, and adds them to seen.
func newLibraries(path string, seen map[string]bool) ([]string, error) {
	names, err := machoImports(path)
	if err != nil {
		return nil, err
	}

	var found []string

	for _, name := range names {
		library, err := libraryPath(name)
		if err != nil {
			return nil, err
		}

		if library != "" && !seen[library] {
			seen[library] = true
			found = append(found, library)
		}
	}

	return found, nil
}

// libraryPath returns the file of the library name with its links resolved, or "" for a library of the shared cache that is not on disk.
func libraryPath(name string) (string, error) {
	if strings.HasPrefix(name, "@") {
		return "", fmt.Errorf("git loads %s by a name relative to a program, which the copy does not resolve", name)
	}

	path, err := filepath.EvalSymlinks(name)
	if errors.Is(err, fs.ErrNotExist) && inSharedCache(name) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("find the library %s: %w", name, err)
	}

	return path, nil
}

// inSharedCache says whether name lies in a folder whose libraries macOS keeps only in the shared cache, which the enforcer reads with the frameworks.
func inSharedCache(name string) bool {
	return strings.HasPrefix(name, "/usr/lib/") || strings.HasPrefix(name, "/System/Library/")
}

// machoImports returns the libraries that the Mach-O file at path names, from the slice of this machine when the file holds several.
func machoImports(path string) ([]string, error) {
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()

		for _, arch := range fat.Arches {
			if arch.Cpu == machoCPU() {
				return arch.ImportedLibraries()
			}
		}

		return nil, fmt.Errorf("%s holds no code for %s", path, runtime.GOARCH)
	}

	file, err := macho.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read the libraries of %s: %w", path, err)
	}
	defer file.Close()

	return file.ImportedLibraries()
}

func machoCPU() macho.Cpu {
	if runtime.GOARCH == "arm64" {
		return macho.CpuArm64
	}

	return macho.CpuAmd64
}
