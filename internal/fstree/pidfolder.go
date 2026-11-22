package fstree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ownerOnlyFolder is the mode of the folder that holds the folders of the processes.
const ownerOnlyFolder = 0o700

// groupOrOtherWrite is the part of a mode that lets another user change a folder.
const groupOrOtherWrite = 0o022

// Attempts is the number of folders that one process can hold in one root at the same time. It bounds the number in a folder name, and so the length of the path.
const Attempts = 100

// Name is the name of the folder that the process pid makes as its number n, from 0 up to Attempts - 1. The pid in the name is the mark of ownership.
func Name(pid, n int) string { return strconv.Itoa(pid) + "-" + strconv.Itoa(n) }

// PidFolder makes a new folder named by Name in root, which holds the folders of the processes of one user. It makes root when it is missing, refuses
// a root that TrustedRoot refuses, and first removes the folders of the processes that are gone, so a process that was killed leaves a folder for the next
// caller to reclaim, not for the owner to find.
func PidFolder(root string) (string, error) {
	if err := os.Mkdir(root, ownerOnlyFolder); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}

	if err := TrustedRoot(root, os.Getuid()); err != nil {
		return "", err
	}

	if err := reclaim(root); err != nil {
		return "", err
	}

	return makeNumbered(root)
}

// makeNumbered makes the first folder of the process in root whose name is free.
func makeNumbered(root string) (string, error) {
	for n := range Attempts {
		path := filepath.Join(root, Name(os.Getpid(), n))

		err := os.Mkdir(path, ownerOnlyFolder)
		if err == nil {
			return path, nil
		}

		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}

	return "", fmt.Errorf("%s holds %d folders of this process already", root, Attempts)
}

// TrustedRoot looks at the folder root itself, not at what a link there points to, and applies trusted.
func TrustedRoot(root string, uid int) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}

	return trusted(root, info, uid)
}

// trusted refuses a folder that another user could have made or can change. On a shared temporary folder another user can make the fixed name first, and then
// swap a folder in it for a link. The folder must be a real folder (not a link) that the user owns, and no group or other user may write it.
func trusted(root string, info fs.FileInfo, uid int) error {
	owner, owned := info.Sys().(*syscall.Stat_t)

	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a folder that beehave made", root)
	case !owned || int(owner.Uid) != uid:
		return fmt.Errorf("%s belongs to another user", root)
	case info.Mode().Perm()&groupOrOtherWrite != 0:
		return fmt.Errorf("%s can be written by another user", root)
	}

	return nil
}

// reclaim removes the folders in root that belong to a process which is gone. A folder that stays is tried again by the next caller. Each path is root
// joined with the name of one entry that root lists, so the removal never leaves root.
func reclaim(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if ownerAlive(entry.Name()) {
			continue
		}

		_ = Remove(filepath.Join(root, entry.Name())) //nolint:errcheck // best effort; the next caller tries again
	}

	return nil
}

// ownerAlive says whether the process that the name of a folder (<pid>-<random>) holds is still running. A name that holds no process id is not alive.
func ownerAlive(name string) bool {
	text, _, _ := strings.Cut(name, "-")

	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return false
	}

	err = syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}
