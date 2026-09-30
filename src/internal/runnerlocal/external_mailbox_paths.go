package runnerlocal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const externalMailboxDirectoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// validateExternalMailboxTree checks the external parent chain and every
// existing mailbox directory without changing the filesystem. A missing root
// or child is valid here: the activation boundary must be crossed before a
// new marker-capable tree becomes visible.
func validateExternalMailboxTree(root string) error {
	return validateExternalMailboxTreeUnder(string(filepath.Separator), root)
}

func validateExternalMailboxTreeUnder(trustRoot, root string) error {
	parentFD, rootName, err := openExternalMailboxParentUnder(trustRoot, root)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)

	rootFD, err := openExistingExternalMailboxDirectory(parentFD, rootName)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errors.New("external mailbox root is unsafe")
	}
	defer unix.Close(rootFD)

	for _, name := range externalMailboxChildNames {
		childFD, err := openExistingExternalMailboxDirectory(rootFD, name)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return errors.New("external mailbox child is unsafe")
		}
		if err := unix.Close(childFD); err != nil {
			return errors.New("close external mailbox child")
		}
	}
	return nil
}

// prepareExternalMailboxTree creates only the root and standard children. It
// uses directory descriptors throughout, so a symlink substituted between
// validation and creation cannot redirect a setup operation outside the
// configured parent chain. It is called only after the candidate root set is
// durable.
func prepareExternalMailboxTree(root string) error {
	return prepareExternalMailboxTreeUnder(string(filepath.Separator), root)
}

func prepareExternalMailboxTreeUnder(trustRoot, root string) error {
	parentFD, rootName, err := openExternalMailboxParentUnder(trustRoot, root)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)

	rootFD, err := openOrCreateExternalMailboxDirectory(parentFD, rootName)
	if err != nil {
		return errors.New("create external mailbox root")
	}
	defer unix.Close(rootFD)

	for _, name := range externalMailboxChildNames {
		childFD, err := openOrCreateExternalMailboxDirectory(rootFD, name)
		if err != nil {
			return errors.New("create external mailbox child")
		}
		if err := unix.Close(childFD); err != nil {
			return errors.New("close external mailbox child")
		}
	}
	return nil
}

var externalMailboxChildNames = []string{"inbox", "outbox", "events", "acks"}

func openExternalMailboxParentUnder(trustRoot, root string) (int, string, error) {
	if !filepath.IsAbs(trustRoot) || filepath.Clean(trustRoot) != trustRoot ||
		!filepath.IsAbs(root) || filepath.Clean(root) != root {
		return -1, "", errors.New("external mailbox path is invalid")
	}
	parent := filepath.Dir(root)
	relative, err := filepath.Rel(trustRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return -1, "", errors.New("external mailbox parent is outside the trusted root")
	}
	parentFD, err := unix.Open(trustRoot, externalMailboxDirectoryOpenFlags, 0)
	if err != nil {
		return -1, "", errors.New("open external mailbox trust root")
	}
	if err := validateExternalMailboxAncestorFD(parentFD, false); err != nil {
		unix.Close(parentFD)
		return -1, "", err
	}
	if relative != "." {
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				unix.Close(parentFD)
				return -1, "", errors.New("external mailbox path is invalid")
			}
			nextFD, err := unix.Openat(parentFD, part, externalMailboxDirectoryOpenFlags, 0)
			unix.Close(parentFD)
			if err != nil {
				return -1, "", errors.New("open external mailbox ancestor")
			}
			if err := validateExternalMailboxAncestorFD(nextFD, false); err != nil {
				unix.Close(nextFD)
				return -1, "", err
			}
			parentFD = nextFD
		}
	}
	if err := validateExternalMailboxAncestorFD(parentFD, true); err != nil {
		unix.Close(parentFD)
		return -1, "", err
	}
	return parentFD, filepath.Base(root), nil
}

func validateExternalMailboxAncestorFD(fd int, requireOwner bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.New("inspect external mailbox ancestor")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("external mailbox ancestor must be a directory")
	}
	if stat.Mode&0o022 != 0 {
		return errors.New("external mailbox ancestor must not be writable by group or others")
	}
	if requireOwner && int(stat.Uid) != os.Geteuid() {
		return errors.New("external mailbox parent owner does not match the current user")
	}
	return nil
}

func openExistingExternalMailboxDirectory(parentFD int, name string) (int, error) {
	fd, err := openExternalMailboxDirectoryFD(parentFD, name)
	if err != nil {
		return -1, err
	}
	if err := validateExternalMailboxDirectoryFD(fd); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openOrCreateExternalMailboxDirectory(parentFD int, name string) (int, error) {
	fd, err := openExistingExternalMailboxDirectory(parentFD, name)
	if err == nil {
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, err
	}
	created := false
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	} else {
		created = true
	}
	if created {
		// A restrictive umask cannot make a 0700 mkdir less private, but a
		// setgid parent may add a special bit. Clear it through the opened
		// descriptor. Pre-existing directories are only validated, never
		// modified.
		fd, err = openExternalMailboxDirectoryFD(parentFD, name)
		if err != nil {
			return -1, err
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			unix.Close(fd)
			return -1, err
		}
	} else {
		fd, err = openExistingExternalMailboxDirectory(parentFD, name)
		if err != nil {
			return -1, err
		}
	}
	if err := validateExternalMailboxDirectoryFD(fd); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openExternalMailboxDirectoryFD(parentFD int, name string) (int, error) {
	return unix.Openat(parentFD, name, externalMailboxDirectoryOpenFlags, 0)
}

func validateExternalMailboxDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.New("inspect external mailbox directory")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || stat.Mode&0o7777 != 0o700 {
		return errors.New("external mailbox directory must be current-user-owned mode 0700")
	}
	return nil
}
