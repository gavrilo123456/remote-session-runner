package mailboxretire

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const RequestID = "req-gpu-t10-f09-merge-dev-20261126-terra01"

type incident struct {
	requestID  string
	commandID  string
	inputHash  string
	outboxHash string
	eventsHash string
}

// RetireF09 inspects or retires only the newer F09 input. The caller holds
// exclusive ingress access throughout, with the importer and writers stopped.
// The archive is a dedicated private directory on the inbox filesystem.
func RetireF09(mailboxRoot, archiveDir string, apply bool) (string, error) {
	return retire(mailboxRoot, archiveDir, apply, incident{
		requestID:  RequestID,
		commandID:  "cmd-363f44aa877e76e25b4a959b3c32f981",
		inputHash:  "c215fa6028f0c583eca5a78ba6284cfba9a8ee45df59d4eb539cbd81993769ee",
		outboxHash: "5bceb42a6650dbff812fe35605877c2105e0e967cf9eaeb13bb8df34afc25709",
		eventsHash: "7179a02c40ce22ce8fc10ffd5632cb598c05a4dec51600062a60d3dc6f097909",
	})
}

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// Open each directory relative to its already-open parent so a symlink in any
// component cannot redirect a later link or unlink.
func openDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, fmt.Errorf("expected a clean absolute directory: %q", path)
	}
	fd, err := unix.Open("/", dirFlags, 0)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(path[1:], "/") {
		next, err := unix.Openat(fd, name, dirFlags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open directory %s: %w", path, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func privateDirectory(f *os.File) (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return st, err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		return st, fmt.Errorf("expected owner-only 0700 directory: %s", f.Name())
	}
	return st, nil
}

func openChild(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, dirFlags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	if _, err := privateDirectory(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type artifact struct {
	name string
	hash string
}

func sameFile(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// inspect hashes an ordinary bounded file opened without following links.
// The only permitted second link is checked by the caller against the other
// exact name in the retirement archive. Published results must be single-link.
func (a artifact) inspect(dir *os.File, ingress bool) (*unix.Stat_t, error) {
	if dir == nil {
		return nil, nil
	}
	var before unix.Stat_t
	fd := int(dir.Fd())
	err := unix.Fstatat(fd, a.name, &before, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mode := before.Mode & 07777
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uint32(os.Geteuid()) ||
		(mode != 0600 && !(ingress && mode == 0644)) || before.Nlink < 1 || before.Nlink > 2 ||
		(!ingress && before.Nlink != 1) || before.Size < 0 || before.Size > 16<<20 {
		return nil, fmt.Errorf("unsafe file %s/%s", dir.Name(), a.name)
	}
	n, err := unix.Openat(fd, a.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(n), a.name)
	defer f.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(n, &opened); err != nil {
		return nil, err
	}
	if !sameFile(&before, &opened) || opened.Mode != before.Mode || opened.Uid != before.Uid ||
		opened.Nlink != before.Nlink || opened.Size != before.Size {
		return nil, fmt.Errorf("file changed before inspection: %s", a.name)
	}
	sum := sha256.New()
	length, err := io.Copy(sum, io.LimitReader(f, opened.Size+1))
	if err != nil {
		return nil, err
	}
	if length != opened.Size || fmt.Sprintf("%x", sum.Sum(nil)) != a.hash {
		return nil, fmt.Errorf("unexpected contents: %s", a.name)
	}
	return &opened, nil
}

type location int

const (
	inboxOnly location = iota
	linked
	archiveOnly
)

func (a artifact) location(inbox, archive *os.File) (location, error) {
	src, err := a.inspect(inbox, true)
	if err != nil {
		return 0, err
	}
	dst, err := a.inspect(archive, true)
	if err != nil {
		return 0, err
	}
	switch {
	case src != nil && dst == nil && src.Nlink == 1:
		return inboxOnly, nil
	case src != nil && dst != nil && sameFile(src, dst) && src.Nlink == 2 && dst.Nlink == 2:
		return linked, nil
	case src == nil && dst != nil && dst.Nlink == 1:
		return archiveOnly, nil
	}
	return 0, fmt.Errorf("missing, mismatched, or multiply linked artifact: %s", a.name)
}

func state(json, marker location) (string, error) {
	switch {
	case json == inboxOnly && marker == inboxOnly:
		return "ready", nil
	case json == linked:
		return "partial", nil
	case json == archiveOnly && marker == archiveOnly:
		return "retired", nil
	default:
		return "", errors.New("unexpected retirement sequence")
	}
}

// Keep both exact files linked in the archive before disarming the pair.
// linkat fails on collision and retains the original inode for crash recovery.
func preserve(a artifact, inbox, archive *os.File) error {
	where, err := a.location(inbox, archive)
	if err != nil || where != inboxOnly {
		return err
	}
	return unix.Linkat(int(inbox.Fd()), a.name, int(archive.Fd()), a.name, 0)
}

func syncArchiveFile(archive *os.File, a artifact) error {
	fd, err := unix.Openat(int(archive.Fd()), a.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

func withdraw(a artifact, inbox, archive *os.File) error {
	where, err := a.location(inbox, archive)
	if err != nil || where == archiveOnly {
		return err
	}
	if where != linked {
		return fmt.Errorf("missing durable archive link: %s", a.name)
	}
	if err := unix.Unlinkat(int(inbox.Fd()), a.name, 0); err != nil {
		return err
	}
	return inbox.Sync()
}

func retire(root, archiveDir string, apply bool, spec incident) (string, error) {
	if !filepath.IsAbs(archiveDir) || filepath.Clean(archiveDir) != archiveDir || archiveDir == "/" ||
		archiveDir == root || strings.HasPrefix(archiveDir+"/", root+"/") || strings.HasPrefix(root+"/", archiveDir+"/") {
		return "", errors.New("expected a dedicated archive outside the mailbox")
	}
	mailbox, err := openDirectory(root)
	if err != nil {
		return "", err
	}
	defer mailbox.Close()
	if _, err := privateDirectory(mailbox); err != nil {
		return "", err
	}
	inbox, err := openChild(mailbox, "inbox")
	if err != nil {
		return "", err
	}
	defer inbox.Close()
	outbox, err := openChild(mailbox, "outbox")
	if err != nil {
		return "", err
	}
	defer outbox.Close()
	events, err := openChild(mailbox, "events")
	if err != nil {
		return "", err
	}
	defer events.Close()
	verifyOriginal := func() error {
		for _, item := range []struct {
			dir *os.File
			artifact
		}{
			{outbox, artifact{spec.requestID + ".json", spec.outboxHash}},
			{events, artifact{spec.commandID + ".ndjson", spec.eventsHash}},
		} {
			st, err := item.inspect(item.dir, false)
			if err != nil {
				return err
			}
			if st == nil {
				return fmt.Errorf("missing original evidence: %s", item.name)
			}
		}
		return nil
	}
	if err := verifyOriginal(); err != nil {
		return "", err
	}
	parent, err := openDirectory(filepath.Dir(archiveDir))
	if err != nil {
		return "", err
	}
	defer parent.Close()
	parentStat, err := privateDirectory(parent)
	if err != nil {
		return "", err
	}
	inboxStat, err := privateDirectory(inbox)
	if err != nil {
		return "", err
	}
	if parentStat.Dev != inboxStat.Dev {
		return "", errors.New("archive and inbox must share a filesystem")
	}
	archiveName := filepath.Base(archiveDir)
	archive, err := openChild(parent, archiveName)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return "", err
	}
	json := artifact{spec.requestID + ".json", spec.inputHash}
	marker := artifact{spec.requestID + ".ready", fmt.Sprintf("%x", sha256.Sum256(nil))}
	if archive != nil {
		defer archive.Close()
		archiveStat, err := privateDirectory(archive)
		if err != nil {
			return "", err
		}
		if archiveStat.Dev != inboxStat.Dev || sameFile(&archiveStat, &inboxStat) {
			return "", errors.New("archive must be a separate directory on the inbox filesystem")
		}
		names, err := archive.Readdirnames(3)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		for _, name := range names {
			if name != json.name && name != marker.name {
				return "", fmt.Errorf("unexpected evidence in archive: %s", name)
			}
		}
	}
	inspect := func() (string, error) {
		j, err := json.location(inbox, archive)
		if err != nil {
			return "", err
		}
		m, err := marker.location(inbox, archive)
		if err != nil {
			return "", err
		}
		return state(j, m)
	}
	result, err := inspect()
	if err != nil || !apply {
		return result, err
	}
	if archive == nil {
		if err := unix.Mkdirat(int(parent.Fd()), archiveName, 0700); err != nil {
			return "", err
		}
		archive, err = openChild(parent, archiveName)
		if err != nil {
			return "", err
		}
		defer archive.Close()
	}
	if err := parent.Sync(); err != nil {
		return "", err
	}
	for _, item := range []artifact{json, marker} {
		if err := preserve(item, inbox, archive); err != nil {
			return "", err
		}
		// A durable marker link always implies a durable JSON link, even if
		// the machine stopped before either inbox name was removed.
		if err := archive.Sync(); err != nil {
			return "", err
		}
	}
	for _, item := range []artifact{json, marker} {
		where, err := item.location(inbox, archive)
		if err != nil {
			return "", err
		}
		if where == inboxOnly {
			return "", fmt.Errorf("missing archive link: %s", item.name)
		}
		if err := syncArchiveFile(archive, item); err != nil {
			return "", err
		}
	}
	for _, item := range []artifact{marker, json} {
		if err := withdraw(item, inbox, archive); err != nil {
			return "", err
		}
	}
	// A retry can observe a completed unlink whose directory sync had failed.
	if err := inbox.Sync(); err != nil {
		return "", err
	}
	if err := verifyOriginal(); err != nil {
		return "", err
	}
	return inspect()
}
