package releaseflow

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

func capacityFixtureDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostCapacityMeasurementIsReadOnlyAndRejectsMissingPath(t *testing.T) {
	root := capacityFixtureDirectory(t)
	marker := filepath.Join(root, "retained-marker")
	if err := os.WriteFile(marker, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := measureHostBackupCapacity(root); err != nil {
		t.Fatalf("existing backup directory rejected: %v", err)
	}
	missing := filepath.Join(root, "must-not-be-created", "backups")
	if _, err := measureHostBackupCapacity(missing); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing backup directory error lost exact path or cause: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capacity observation created a missing directory: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "retained-marker" {
		t.Fatalf("capacity observation changed directory contents: %v, %v", entries, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "preserved" {
		t.Fatalf("retained contents changed: %q, %v", data, err)
	}
	after, err := os.Stat(root)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("capacity observation modified backup directory: %v", err)
	}
}

func TestHostCapacityMeasurementRejectsSymlinksAndRegularFiles(t *testing.T) {
	root := capacityFixtureDirectory(t)
	real := filepath.Join(root, "actual", "backups")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	directLink := filepath.Join(root, "backup-link")
	ancestorLink := filepath.Join(root, "ancestor-link")
	if err := os.Symlink(real, directLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(real), ancestorLink); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "regular-file")
	if err := os.WriteFile(regular, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directLink, filepath.Join(ancestorLink, "backups"), regular} {
		if _, err := measureHostBackupCapacity(path); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("non-directory or symlink backup path %q was accepted: %v", path, err)
		}
	}
}

func TestHostCapacityChecksPlatformBeforeReadingRuntimePaths(t *testing.T) {
	for _, host := range HostOrder {
		if laneinstall.CatalogHostMatchesPlatform(host, runtime.GOOS, runtime.GOARCH) {
			continue
		}
		_, err := RunHostCapacity(context.Background(), host, "dev", capacityFixtureDirectory(t), strings.Repeat("a", 40))
		if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "does not match this platform") {
			t.Errorf("capacity helper accepted a remote host identity on this machine: host=%s err=%v", host, err)
		}
	}
}

func TestHostCapacityRequiresCleanPinnedControllerBeforeMeasurement(t *testing.T) {
	host := ""
	for _, candidate := range HostOrder {
		if laneinstall.CatalogHostMatchesPlatform(candidate, runtime.GOOS, runtime.GOARCH) {
			host = candidate
			break
		}
	}
	current, err := user.Current()
	if err != nil || host == "" || host == "mac-control-plane" && current.Username != config.MacAccount || host != "mac-control-plane" && current.Username != config.LinuxAccount {
		t.Skip("this process is not running as a configured host account")
	}
	repo := capacityFixtureDirectory(t)
	initGitTestBranch(t, repo, "dev")
	runGitTest(t, "-C", repo, "config", "user.email", "fixture@example.invalid")
	runGitTest(t, "-C", repo, "config", "user.name", "Capacity Fixture")
	marker := filepath.Join(repo, "fixture.txt")
	if err := os.WriteFile(marker, []byte("committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", repo, "add", "fixture.txt")
	runGitTest(t, "-C", repo, "commit", "-m", "fixture")
	revision := strings.TrimSpace(runGitTest(t, "-C", repo, "rev-parse", "HEAD"))
	if _, err := RunHostCapacity(context.Background(), host, "dev", repo, strings.Repeat("a", 40)); !errors.Is(err, ErrReleaseMismatch) {
		t.Fatalf("capacity helper did not reject a different controller revision: %v", err)
	}
	if err := os.WriteFile(marker, []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunHostCapacity(context.Background(), host, "dev", repo, revision); !errors.Is(err, ErrOperationCorrupted) {
		t.Fatalf("capacity helper did not reject changed controller source: %v", err)
	}
}

func TestHostCapacityByteCalculationRejectsOverflow(t *testing.T) {
	if got, err := checkedCapacityBytes(4096, 12345); err != nil || got != 50_565_120 {
		t.Fatalf("ordinary capacity calculation failed: %d, %v", got, err)
	}
	if got, err := checkedCapacityBytes(4096, 0); err != nil || got != 0 {
		t.Fatalf("full filesystem must report zero free bytes: %d, %v", got, err)
	}
	for _, item := range []struct {
		blockSize int64
		blocks    uint64
	}{{0, 1}, {-1, 1}, {4096, ^uint64(0)/4096 + 1}} {
		if _, err := checkedCapacityBytes(item.blockSize, item.blocks); !errors.Is(err, ErrOperationCorrupted) {
			t.Errorf("invalid capacity accepted: %+v, %v", item, err)
		}
	}
}

func TestHostCapacityCLIRejectsPathOverridesAndPositionals(t *testing.T) {
	for _, args := range [][]string{
		{"host-capacity", "unexpected"},
		{"host-capacity", "--path", "/tmp"},
		{"host-capacity", "--backup-path", "/tmp"},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunHostCLI(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Errorf("unsafe capacity invocation not rejected before observation: args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}
