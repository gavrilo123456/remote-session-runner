package lanefixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/logcapture"
)

func probeFixtureLogBudget(t *testing.T, projectPath, fixtureID string) error {
	t.Helper()
	logDir := filepath.Join(projectPath, ".rsr-log-budget-"+fixtureID)
	if err := os.Mkdir(logDir, 0o700); err != nil {
		return fmt.Errorf("create exact fixture log directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(logDir); err != nil {
			t.Errorf("remove task-owned log fixture %s: %v", fixtureID, err)
		}
	}()
	if err := verifyOwnedFixtureDirectory(logDir); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "runner.log")
	store, err := logcapture.Open(logPath)
	if err != nil {
		return fmt.Errorf("open bounded fixture log: %w", err)
	}
	limits := logcapture.DefaultLimits()
	chunk := make([]byte, 1<<20)
	for written := uint64(0); written < limits.TotalBytes()+uint64(len(chunk)); written += uint64(len(chunk)) {
		count, writeErr := store.Write(chunk)
		if writeErr != nil || count != len(chunk) {
			_ = store.Close()
			return fmt.Errorf("write bounded fixture log at %d bytes: count=%d err=%v", written, count, writeErr)
		}
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("sync bounded fixture log: %w", err)
	}
	status, err := logcapture.ReadStatus(logPath)
	if err != nil || status.DroppedLogBytes != 0 {
		return fmt.Errorf("bounded fixture log status=%+v err=%v", status, err)
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}
	var retained uint64
	var active, rotated int
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture log directory contains a non-regular file")
		}
		switch {
		case entry.Name() == "runner.log.dropped_log_bytes":
			retained += uint64(info.Size())
		case entry.Name() == "runner.log":
			active++
			if uint64(info.Size()) > limits.ActiveFileBytes {
				return fmt.Errorf("fixture active log exceeds its per-file cap")
			}
			retained += uint64(info.Size())
		case strings.HasPrefix(entry.Name(), "runner.log.rot."):
			rotated++
			if uint64(info.Size()) > limits.RotatedFileBytes {
				return fmt.Errorf("fixture rotated log exceeds its per-file cap")
			}
			retained += uint64(info.Size())
		default:
			return fmt.Errorf("fixture log emitted unexpected file %q", entry.Name())
		}
	}
	if active != 1 || rotated > limits.MaxRotatedFiles || retained > limits.TotalBytes() {
		return fmt.Errorf("fixture log retention exceeded cap: active=%d rotated=%d bytes=%d cap=%d", active, rotated, retained, limits.TotalBytes())
	}
	t.Logf("fixture log capped at %d retained bytes (policy maximum %d)", retained, limits.TotalBytes())
	return nil
}
