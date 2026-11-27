package main

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// linkerCache is the list of library folders that the glibc dynamic linker reads to find a library by its name.
const linkerCache = "/etc/ld.so.cache"

// gitLibraries returns the dynamic linker that the program at binary names and the libraries that it loads, with their links resolved, and the file
// that the linker reads to find a library, when the machine has one. The linker lists the libraries itself (--list, what ldd runs), by its path and
// with no environment, so no search path and no LD_LIBRARY_PATH plays a part. A static program loads none of them. A file that is no ELF program, a
// linker that is relative or gone, a library that the linker cannot find, or a linker that cannot run, is an error.
func gitLibraries(ctx context.Context, binary string) ([]string, error) {
	interpreter, linker, err := programLinker(binary)
	if err != nil || linker == "" {
		return nil, err
	}

	list := exec.CommandContext(ctx, linker, "--list", binary) //nolint:gosec // the linker that the git of the engine names
	list.Env = []string{}

	out, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("list the libraries with %s: %w", linker, err)
	}

	found, err := lddLibraries(string(out), linkerConfig(interpreter))
	if err != nil {
		return nil, err
	}

	return append([]string{linker}, found...), nil
}

// programLinker returns the dynamic linker that the ELF program at path names, as it is named and with its links resolved, or "" for a static program.
func programLinker(path string) (interpreter, linker string, err error) {
	interpreter, err = programInterpreter(path)
	if err != nil || interpreter == "" {
		return "", "", err
	}

	if !filepath.IsAbs(interpreter) {
		return "", "", fmt.Errorf("git names the dynamic linker %s by a relative path, which the copy does not resolve", interpreter)
	}

	linker, err = filepath.EvalSymlinks(interpreter)
	if err != nil {
		return "", "", fmt.Errorf("find the dynamic linker of git: %w", err)
	}

	return interpreter, linker, nil
}

// linkerConfig returns the file that the dynamic linker interpreter reads to find a library by its name: /etc/ld-musl-<arch>.path for musl, and the
// cache of glibc otherwise.
func linkerConfig(interpreter string) string {
	if name, ok := strings.CutPrefix(filepath.Base(interpreter), "ld-musl-"); ok {
		return "/etc/ld-musl-" + strings.TrimSuffix(name, ".so.1") + ".path"
	}

	return linkerCache
}

// programInterpreter returns the dynamic linker that the ELF program at path names, or "" for a static program, which loads no library.
func programInterpreter(path string) (string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("read the program %s: %w", path, err)
	}
	defer file.Close()

	for _, header := range file.Progs {
		if header.Type == elf.PT_INTERP {
			name, err := io.ReadAll(header.Open())

			return string(bytes.TrimRight(name, "\x00")), err
		}
	}

	return "", nil
}

// lddLibraries returns the file of each library in a list of the dynamic linker (the lines that ldd prints), and cache when it exists. A line with no
// path, such as the vDSO, names no file.
func lddLibraries(out, cache string) ([]string, error) {
	var found []string

	for line := range strings.SplitSeq(out, "\n") {
		path, err := lddPath(strings.TrimSpace(line))
		if err != nil {
			return nil, err
		}

		if path != "" {
			found = append(found, path)
		}
	}

	return withCache(found, cache)
}

// withCache returns found with cache added when it exists. A cache that cannot be read for another reason than that it does not exist is an error.
func withCache(found []string, cache string) ([]string, error) {
	_, err := os.Stat(cache)
	if err == nil {
		return append(found, cache), nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read the cache of the linker: %w", err)
	}

	return found, nil
}

// lddPath returns the file of one line of the list, "name => /path (address)" or "/path (address)", with its links resolved. A line of a library that
// the linker did not find, or whose file is gone, is an error.
func lddPath(line string) (string, error) {
	if _, after, ok := strings.Cut(line, "=>"); ok {
		line = strings.TrimSpace(after)
		if strings.HasPrefix(line, "not found") {
			return "", fmt.Errorf("the dynamic linker finds no file for a library of git: %s", line)
		}
	}

	path, _, _ := strings.Cut(line, " ")
	if !filepath.IsAbs(path) {
		return "", nil
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("find the library %s: %w", path, err)
	}

	return resolved, nil
}
