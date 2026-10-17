package logcapture

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func smallLimits() Limits {
	return Limits{ActiveFileBytes: 32, RotatedFileBytes: 32, MaxRotatedFiles: 2, TempFileBytes: 32}
}

func TestLanesL205BoundedFloodRotatesAndPersistsDropCounter(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.log")
	limits := smallLimits()
	store, err := openWithLimits(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Repeat([]byte("x"), 32*8+7)
	if count, err := store.Write(input); err != nil || count != len(input) {
		t.Fatalf("bounded flood write = %d err=%v", count, err)
	}
	rotated, err := store.rotatedFiles()
	if err != nil || len(rotated) != limits.MaxRotatedFiles {
		t.Fatalf("retained rotations=%d err=%v", len(rotated), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(path)
	if err != nil || status.DroppedLogBytes != 0 {
		t.Fatalf("successful bounded rotation status=%+v err=%v", status, err)
	}
	if got := limits.TotalBytes(); got != 128 || got > limits.ActiveFileBytes+limits.RotatedFileBytes*uint64(limits.MaxRotatedFiles)+limits.TempFileBytes {
		t.Fatalf("test cap arithmetic = %d", got)
	}

	store, err = openWithLimits(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	store.writeFile = func(file *os.File, data []byte) (int, error) { return 0, syscall.ENOSPC }
	if count, err := store.Write([]byte("storage-full-diagnostic")); err != nil || count != len("storage-full-diagnostic") {
		t.Fatalf("disk-full logging should drop and continue: count=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(path)
	if err != nil || status.DroppedLogBytes != uint64(len("storage-full-diagnostic")) {
		t.Fatalf("durable dropped counter=%+v err=%v", status, err)
	}
	store, err = openWithLimits(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(path)
	if err != nil || status.DroppedLogBytes != uint64(len("storage-full-diagnostic")) {
		t.Fatalf("counter did not survive reopen: status=%+v err=%v", status, err)
	}
}

func TestLanesL205RotationCrashRecoversSingleTemporaryFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.log")
	limits := smallLimits()
	store, err := openWithLimits(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(bytes.Repeat([]byte("a"), 32)); err != nil {
		t.Fatal(err)
	}
	store.afterTempRename = func() error { return ErrInjectedCrash }
	if _, err := store.Write([]byte("b")); !errors.Is(err, ErrInjectedCrash) {
		t.Fatalf("rotation crash injection error=%v", err)
	}
	if _, err := os.Stat(path + ".rotate.tmp"); err != nil {
		t.Fatalf("crash left no recoverable temp rotation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openWithLimits(path, limits)
	if err != nil {
		t.Fatalf("startup did not recover temp rotation: %v", err)
	}
	if _, err := os.Stat(path + ".rotate.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temporary rotation remains after recovery: %v", err)
	}
	rotated, err := store.rotatedFiles()
	if err != nil || len(rotated) != 1 {
		t.Fatalf("recovered rotations=%d err=%v", len(rotated), err)
	}
	contents, err := os.ReadFile(rotated[0].path)
	if err != nil || string(contents) != strings.Repeat("a", 32) {
		t.Fatalf("recovered rotation content=%q err=%v", contents, err)
	}
	if _, err := store.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(path)
	if err != nil || string(active) != "b" {
		t.Fatalf("post-recovery active file=%q err=%v", active, err)
	}
}

func TestLanesL205RejectsInconsistentActiveAndTemporaryRotation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.log")
	if err := os.WriteFile(path, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".rotate.tmp", []byte("temporary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openWithLimits(path, smallLimits()); !errors.Is(err, ErrUnsafeLogState) {
		t.Fatalf("ambiguous active plus temporary rotation opened: %v", err)
	}
}

func TestLanesL205RefusesRotationWithoutDeletingArchivesWhenTempExists(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.log")
	for sequence, content := range []string{"oldest", "newer"} {
		archive := path + ".rot." + strings.Repeat("0", 19) + string(rune('1'+sequence))
		if err := os.WriteFile(archive, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	limits := smallLimits()
	store, err := openWithLimits(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(bytes.Repeat([]byte("x"), int(limits.ActiveFileBytes))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".rotate.tmp", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write([]byte("next")); !errors.Is(err, ErrUnsafeLogState) {
		t.Fatalf("rotation with a stale temporary file error=%v", err)
	}
	rotated, err := store.rotatedFiles()
	if err != nil || len(rotated) != limits.MaxRotatedFiles {
		t.Fatalf("unsafe rotation deleted retained archives: count=%d err=%v", len(rotated), err)
	}
	oldest, err := os.ReadFile(rotated[0].path)
	if err != nil || string(oldest) != "oldest" {
		t.Fatalf("oldest retained archive=%q err=%v", oldest, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLanesL205FailedStartAndChildStreams(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner.log")
	if exit := RunChild(t.Context(), path, []string{filepath.Join(root, "missing-child")}); exit != 78 {
		t.Fatalf("missing child exit=%d, want fail-closed 78", exit)
	}
	status, err := ReadStatus(path)
	if err != nil || status.DroppedLogBytes != 0 {
		t.Fatalf("failed wrapper start status=%+v err=%v", status, err)
	}

	child := filepath.Join(root, "child.sh")
	contents := "#!/bin/sh\nprintf 'stdout-token\\n'\nprintf 'stderr-token\\n' >&2\nexit 7\n"
	if err := os.WriteFile(child, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if exit := RunChild(t.Context(), path, []string{"/bin/sh", child}); exit != 7 {
		t.Fatalf("child exit=%d, want preserved exit 7", exit)
	}
	log, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(log, []byte("stdout-token\n")) || !bytes.Contains(log, []byte("stderr-token\n")) {
		t.Fatalf("combined captured child streams=%q err=%v", log, err)
	}
	status, err = ReadStatus(path)
	if err != nil || status.DroppedLogBytes != 0 {
		t.Fatalf("successful child status=%+v err=%v", status, err)
	}

	missingDirectoryPath := filepath.Join(root, "missing", "runner.log")
	if exit := RunChild(t.Context(), missingDirectoryPath, []string{"/bin/sh", child}); exit != 78 {
		t.Fatalf("missing log directory start exit=%d, want 78", exit)
	}
}

func TestLanesL205ProductionBudgetIsExactly576MiB(t *testing.T) {
	limits := DefaultLimits()
	if limits.ActiveFileBytes != 64*MiB || limits.RotatedFileBytes != 64*MiB || limits.MaxRotatedFiles != 7 ||
		limits.TempFileBytes != 64*MiB || limits.TotalBytes() != 576*MiB || limits.validate() != nil {
		t.Fatalf("production log capture budget = %+v total=%d", limits, limits.TotalBytes())
	}
}

func TestLanesL205NativeProcessCaptureOnSelectedHost(t *testing.T) {
	if os.Getenv("LANES_NATIVE_VALIDATION") != "1" {
		t.Skip("native lane assertion is dispatched only by test-lanes tier N")
	}
	hostID := os.Getenv("LANES_NATIVE_HOST_ID")
	wantOS := map[string]string{
		"mac-control-plane": "darwin",
		"linux-host":        "linux",
		"sandbox-host":      "linux",
		"factory-sandbox":   "linux",
	}[hostID]
	if wantOS == "" || runtime.GOOS != wantOS {
		t.Fatalf("selected native host %q on %s; want a registered host on %s", hostID, runtime.GOOS, wantOS)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "native-child.sh")
	if err := os.WriteFile(child, []byte("#!/bin/sh\nprintf 'native-stdout\\n'\nprintf 'native-stderr\\n' >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "native.log")
	if exit := RunChild(t.Context(), logPath, []string{"/bin/sh", child}); exit != 0 {
		t.Fatalf("native wrapper/child exit=%d", exit)
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("native active log mode=%v err=%v; want 0600", info, err)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil || !bytes.Contains(contents, []byte("native-stdout\n")) || !bytes.Contains(contents, []byte("native-stderr\n")) {
		t.Fatalf("native combined stream=%q err=%v", contents, err)
	}
	status, err := ReadStatus(logPath)
	if err != nil || status.DroppedLogBytes != 0 {
		t.Fatalf("native dropped counter=%+v err=%v", status, err)
	}
}
