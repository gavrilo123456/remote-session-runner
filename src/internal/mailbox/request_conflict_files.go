package mailbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/domain"
)

const (
	requestConflictCode    = "request_id_conflict"
	requestConflictMessage = "request ID is already bound to different instructions; use a fresh request ID and idempotency key"
)

var errRequestConflictFiles = errors.New("mailbox request conflict preservation refused")

// requestConflictRecord describes the refused publication, not the original
// exchange. In particular, it has no response revision, cursor or ACK fields.
type requestConflictRecord struct {
	FormatVersion         int    `json:"format_version"`
	MailboxID             string `json:"mailbox_id"`
	RequestID             string `json:"request_id"`
	InputSHA256           string `json:"input_sha256"`
	OriginalBindingSHA256 string `json:"original_binding_sha256"`
	Code                  string `json:"code"`
	Message               string `json:"message"`
	Accepted              bool   `json:"accepted"`
	Executed              bool   `json:"executed"`
}

type requestConflictFiles struct {
	mailboxRoot string
	root        string
}

// Conflict history is never scanned during recovery. Only the small unfinished
// index is read; temporary files are kept in a different directory so a crash
// cannot leave temporary names ahead of genuine pending entries.
type requestConflictDirectories struct {
	root, pending, staging, archive *os.File
}

func (d *requestConflictDirectories) close() {
	for _, f := range []*os.File{d.archive, d.staging, d.pending, d.root} {
		if f != nil {
			_ = f.Close()
		}
	}
}

func newRequestConflictFiles(mailboxRoot string) (*requestConflictFiles, error) {
	if !filepath.IsAbs(mailboxRoot) || filepath.Clean(mailboxRoot) != mailboxRoot || strings.IndexByte(mailboxRoot, 0) >= 0 {
		return nil, errRequestConflictFiles
	}
	if err := ensureOwnerDirectory(mailboxRoot); err != nil {
		return nil, errRequestConflictFiles
	}
	f := &requestConflictFiles{mailboxRoot: mailboxRoot, root: filepath.Join(mailboxRoot, "conflicts")}
	d, err := f.open(true, nil)
	if err != nil {
		return nil, err
	}
	d.close()
	return f, nil
}

func (f *requestConflictFiles) open(create bool, record *requestConflictRecord) (_ *requestConflictDirectories, result error) {
	if f == nil || f.mailboxRoot == "" || f.root != filepath.Join(f.mailboxRoot, "conflicts") {
		return nil, errRequestConflictFiles
	}
	before, err := os.Lstat(f.mailboxRoot)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != MailboxDirectoryMode || !mailboxFileOwnedByCurrentUser(before) {
		return nil, errRequestConflictFiles
	}
	fd, err := unix.Open(f.mailboxRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errRequestConflictFiles
	}
	mailbox := os.NewFile(uintptr(fd), f.mailboxRoot)
	defer mailbox.Close()
	opened, err := mailbox.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errRequestConflictFiles
	}
	d := &requestConflictDirectories{}
	defer func() {
		if result != nil {
			d.close()
		}
	}()
	if d.root, err = openRequestConflictDirectory(mailbox, "conflicts", create); err != nil {
		return nil, err
	}
	// Internal names start with a dot, which a safe request ID cannot use.
	// Thus even a request literally named "pending" cannot collide with them.
	if d.pending, err = openRequestConflictDirectory(d.root, ".pending", create); err != nil {
		return nil, err
	}
	if d.staging, err = openRequestConflictDirectory(d.root, ".staging", create); err != nil {
		return nil, err
	}
	if record != nil {
		request, err := openRequestConflictDirectory(d.root, record.RequestID, create)
		if err != nil {
			return nil, err
		}
		defer request.Close()
		if d.archive, err = openRequestConflictDirectory(request, record.InputSHA256, create); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func openRequestConflictDirectory(parent *os.File, name string, create bool) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, errRequestConflictFiles
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errRequestConflictFiles
	}
	f := os.NewFile(uintptr(fd), name)
	st, err := f.Stat()
	if err != nil || !st.IsDir() || st.Mode().Perm() != MailboxDirectoryMode || !mailboxFileOwnedByCurrentUser(st) {
		_ = f.Close()
		return nil, errRequestConflictFiles
	}
	// Retry may see a directory created before an interrupted/failed parent
	// sync. Make its name durable even when Mkdirat found it already present.
	if create {
		if err := parent.Sync(); err != nil {
			_ = f.Close()
			return nil, errRequestConflictFiles
		}
	}
	return f, nil
}

func validRequestConflictRecord(record requestConflictRecord) bool {
	_, mailboxOK := safeMailboxID(record.MailboxID)
	_, requestOK := safeRequestID(record.RequestID)
	return record.FormatVersion == 1 && mailboxOK && requestOK &&
		validRequestConflictDigest(record.InputSHA256) && validRequestConflictDigest(record.OriginalBindingSHA256) &&
		record.Code == requestConflictCode && record.Message == requestConflictMessage && !record.Accepted && !record.Executed
}

func validRequestConflictDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func requestConflictIndexName(record requestConflictRecord) string {
	return record.RequestID + "." + record.InputSHA256 + ".json"
}

// Preserve makes the exact refused bytes, empty publication marker and frozen
// diagnostic durable before publishing their recovery-index entry. It never
// opens inbox, outbox, events or ACK artifacts.
func (f *requestConflictFiles) Preserve(ctx context.Context, record requestConflictRecord, raw []byte) error {
	if err := requestConflictContext(ctx); err != nil {
		return err
	}
	if !validRequestConflictRecord(record) || len(raw) == 0 || len(raw) > domain.MaxSerializedRequestBytes ||
		hex.EncodeToString(hashRequestConflictBytes(raw)) != record.InputSHA256 {
		return errRequestConflictFiles
	}
	var identity struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &identity) != nil || identity.RequestID != record.RequestID {
		return errRequestConflictFiles
	}
	d, err := f.open(true, &record)
	if err != nil {
		return err
	}
	defer d.close()
	encoded, _ := json.Marshal(record)
	for _, artifact := range []struct {
		name string
		data []byte
	}{{"request.json", raw}, {"request.ready", nil}, {"diagnostic.json", encoded}} {
		if err := writeRequestConflictImmutable(d.staging, d.archive, artifact.name, artifact.data); err != nil {
			return err
		}
	}
	if err := requestConflictContext(ctx); err != nil {
		return err
	}
	return writeRequestConflictImmutable(d.staging, d.pending, requestConflictIndexName(record), encoded)
}

func hashRequestConflictBytes(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

// Pending reads at most limit unfinished records. Finished history and crash
// leftovers in .staging are not enumerated. Invalid pending evidence refuses
// the batch rather than authorizing generic draft cleanup.
func (f *requestConflictFiles) Pending(ctx context.Context, limit int) ([]requestConflictRecord, error) {
	if err := requestConflictContext(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 256 {
		return nil, errRequestConflictFiles
	}
	d, err := f.open(false, nil)
	if err != nil {
		return nil, err
	}
	defer d.close()
	names, err := d.pending.Readdirnames(limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errRequestConflictFiles
	}
	result := make([]requestConflictRecord, 0, len(names))
	if len(names) == 0 {
		return result, nil
	}
	// A prior Preserve may have linked an index before its directory sync
	// failed. Make visible entries durable before yielding recovery authority,
	// while an empty idle scan needs no filesystem write.
	if err := d.pending.Sync(); err != nil {
		return nil, errRequestConflictFiles
	}
	for _, name := range names {
		if err := requestConflictContext(ctx); err != nil {
			return nil, err
		}
		data, err := readRequestConflictFile(d.pending, name, 4096)
		if err != nil {
			return nil, err
		}
		var record requestConflictRecord
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&record) != nil || !validRequestConflictRecord(record) || name != requestConflictIndexName(record) {
			return nil, errRequestConflictFiles
		}
		encoded, _ := json.Marshal(record)
		if !bytes.Equal(data, encoded) {
			return nil, errRequestConflictFiles
		}
		archive, err := f.open(false, &record)
		if err != nil {
			return nil, err
		}
		verifyErr := verifyRequestConflictArchive(archive.archive, record)
		archive.close()
		if verifyErr != nil {
			return nil, verifyErr
		}
		result = append(result, record)
	}
	return result, nil
}

// Finish is called only after the importer has proved withdrawal. The retired
// marker is durable before the unfinished index is removed, making repetition
// after either write idempotent. It makes no capacity or authority mutation.
func (f *requestConflictFiles) Finish(ctx context.Context, record requestConflictRecord) error {
	if err := requestConflictContext(ctx); err != nil {
		return err
	}
	if !validRequestConflictRecord(record) {
		return errRequestConflictFiles
	}
	d, err := f.open(false, &record)
	if err != nil {
		return err
	}
	defer d.close()
	if err := verifyRequestConflictArchive(d.archive, record); err != nil {
		return err
	}
	encoded, _ := json.Marshal(record)
	name := requestConflictIndexName(record)
	pending, pendingErr := readRequestConflictFile(d.pending, name, 4096)
	if pendingErr != nil && !errors.Is(pendingErr, unix.ENOENT) {
		return pendingErr
	}
	if pendingErr == nil && !bytes.Equal(pending, encoded) {
		return errRequestConflictFiles
	}
	if errors.Is(pendingErr, unix.ENOENT) {
		retired, err := readRequestConflictFile(d.archive, "retired", 0)
		if err != nil || len(retired) != 0 {
			return errRequestConflictFiles
		}
		return nil
	}
	if err := writeRequestConflictImmutable(d.staging, d.archive, "retired", nil); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(d.pending.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return errRequestConflictFiles
	}
	if err := d.pending.Sync(); err != nil {
		return errRequestConflictFiles
	}
	return nil
}

func verifyRequestConflictArchive(archive *os.File, record requestConflictRecord) error {
	encoded, _ := json.Marshal(record)
	diagnostic, err := readRequestConflictFile(archive, "diagnostic.json", 4096)
	if err != nil || !bytes.Equal(diagnostic, encoded) {
		return errRequestConflictFiles
	}
	raw, err := readRequestConflictFile(archive, "request.json", domain.MaxSerializedRequestBytes)
	if err != nil || hex.EncodeToString(hashRequestConflictBytes(raw)) != record.InputSHA256 {
		return errRequestConflictFiles
	}
	marker, err := readRequestConflictFile(archive, "request.ready", 0)
	if err != nil || len(marker) != 0 {
		return errRequestConflictFiles
	}
	return nil
}

func requestConflictContext(ctx context.Context) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func readRequestConflictFile(directory *os.File, name string, maximum int) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, unix.ENOENT
	}
	if err != nil {
		return nil, errRequestConflictFiles
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !safePrivateMailboxFileInfo(st) || !mailboxFileOwnedByCurrentUser(st) || st.Size() < 0 || st.Size() > int64(maximum) {
		return nil, errRequestConflictFiles
	}
	var metadata unix.Stat_t
	// A crash after linkat and before temporary unlink can leave the same
	// immutable inode linked in private .staging as well. Neither location is
	// ever rewritten; accepting that second link makes preservation retryable.
	if unix.Fstat(fd, &metadata) != nil || metadata.Nlink < 1 || metadata.Nlink > 2 {
		return nil, errRequestConflictFiles
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil || int64(len(data)) != st.Size() || len(data) > maximum {
		return nil, errRequestConflictFiles
	}
	return data, nil
}

// Publish immutable files through a synced temporary inode and linkat's
// no-overwrite boundary. All names are relative to already verified directory
// descriptors; changing a parent pathname cannot redirect a later operation.
func writeRequestConflictImmutable(staging, directory *os.File, name string, data []byte) error {
	got, err := readRequestConflictFile(directory, name, len(data))
	if err == nil {
		if bytes.Equal(got, data) {
			// A previous publication may have stopped after linkat but before
			// the directory sync. Exact bytes alone are not durability proof.
			if err := directory.Sync(); err != nil {
				return errRequestConflictFiles
			}
			return nil
		}
		return errRequestConflictFiles
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errRequestConflictFiles
	}
	temporary := hex.EncodeToString(nonce[:]) + ".tmp"
	fd, err := unix.Openat(int(staging.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errRequestConflictFiles
	}
	f := os.NewFile(uintptr(fd), temporary)
	defer unix.Unlinkat(int(staging.Fd()), temporary, 0)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return errRequestConflictFiles
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errRequestConflictFiles
	}
	if err := f.Close(); err != nil {
		return errRequestConflictFiles
	}
	if err := unix.Linkat(int(staging.Fd()), temporary, int(directory.Fd()), name, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return errRequestConflictFiles
		}
		got, readErr := readRequestConflictFile(directory, name, len(data))
		if readErr != nil || !bytes.Equal(got, data) {
			return errRequestConflictFiles
		}
	}
	if err := unix.Unlinkat(int(staging.Fd()), temporary, 0); err != nil {
		return errRequestConflictFiles
	}
	if err := directory.Sync(); err != nil {
		return errRequestConflictFiles
	}
	return nil
}
