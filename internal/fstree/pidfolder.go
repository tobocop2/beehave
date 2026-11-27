package fstree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// Folders makes the folders of one process and knows which ones it made. A folder with the pid of this process that it did not make is the leftover of
// a killed process that had the same pid, and the next Make removes it. One Folders serves all the callers of a process that share a root. The zero value is ready.
// Two sets on one root remove each other's live folders, so a process builds one set and shares it.
// A nil Folders knows no folder, so it keeps every folder whose pid runs, this process included.
type Folders struct {
	mu   sync.Mutex
	made map[string]bool
}

// lock takes the lock of the set, and returns the function that gives it back. A nil set has no lock.
func (f *Folders) lock() func() {
	if f == nil {
		return func() {}
	}

	f.mu.Lock()

	return f.mu.Unlock
}

// keeps says whether the folder at path belongs to this process: the set made it, or the set is nil and so cannot tell.
func (f *Folders) keeps(path string) bool { return f == nil || f.made[path] }

// record notes that the set made the folder at path.
func (f *Folders) record(path string) {
	if f == nil {
		return
	}

	if f.made == nil {
		f.made = map[string]bool{}
	}

	f.made[path] = true
}

// openRoot makes root when it is missing and refuses a root that TrustedRoot refuses.
func openRoot(root string) error {
	if err := os.Mkdir(root, ownerOnlyFolder); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	return TrustedRoot(root, os.Getuid())
}

// Make makes a new folder named by Name in root, which holds the folders of the processes of one user. It makes root when it is missing, refuses
// a root that TrustedRoot refuses, and first removes the folders of the processes that are gone, so a process that was killed leaves a folder for the next
// caller to reclaim, not for the owner to find.
func (f *Folders) Make(root string) (string, error) {
	defer f.lock()()

	if err := openRoot(root); err != nil {
		return "", err
	}

	if err := f.reclaim(root); err != nil {
		return "", err
	}

	path, err := makeNumbered(root)
	if err != nil {
		return "", err
	}

	f.record(path)

	return path, nil
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
		return fmt.Errorf("%s is not a folder that beehest made", root)
	case !owned || int(owner.Uid) != uid:
		return fmt.Errorf("%s belongs to another user", root)
	case info.Mode().Perm()&groupOrOtherWrite != 0:
		return fmt.Errorf("%s can be written by another user", root)
	}

	return nil
}

// reclaim removes the folders in root that belong to a process which is gone. A folder that stays is tried again by the next caller. Each path is root
// joined with the name of one entry that root lists, so the removal never leaves root.
func (f *Folders) reclaim(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if ownerAlive(entry.Name()) && (!ownPid(entry.Name()) || f.keeps(path)) {
			continue
		}

		_ = Remove(path) //nolint:errcheck // best effort; the next caller tries again
	}

	return nil
}

// ownPid says whether the name of a folder (<pid>-<random>) holds the pid of this process.
func ownPid(name string) bool {
	text, _, _ := strings.Cut(name, "-")

	return text == strconv.Itoa(os.Getpid())
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
