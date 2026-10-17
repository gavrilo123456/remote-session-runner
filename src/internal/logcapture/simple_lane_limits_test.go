package logcapture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSimpleLaneLogLimitsAreBoundedAndDoNotChangePRDDefaults(t *testing.T) {
	simple := SimpleLaneLimits()
	if simple.ActiveFileBytes != 16*MiB || simple.RotatedFileBytes != 16*MiB || simple.MaxRotatedFiles != 4 || simple.TempFileBytes != 16*MiB || simple.validate() != nil {
		t.Fatalf("simple lane log limits=%+v", simple)
	}
	prd := DefaultLimits()
	if prd.ActiveFileBytes != 64*MiB || prd.RotatedFileBytes != 64*MiB || prd.MaxRotatedFiles != 7 || prd.TempFileBytes != 64*MiB {
		t.Fatalf("unsuffixed PRD default changed: %+v", prd)
	}
}

func TestSimpleLaneLogLimitsAreAppliedByCaptureCLI(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "task.log")
	var stdout, stderr bytes.Buffer
	args := []string{
		"--log-path", logPath,
		"--active-file-bytes", "16777216",
		"--rotated-file-bytes", "16777216",
		"--max-rotated-files", "4",
		"--temp-file-bytes", "16777216",
		"--", "/bin/sh", "-c", "printf lane-log-policy",
	}
	if exit := RunCLIContext(t.Context(), args, &stdout, &stderr); exit != 0 {
		t.Fatalf("bounded capture CLI exit=%d stderr=%q", exit, stderr.String())
	}
	contents, err := os.ReadFile(logPath)
	if err != nil || string(contents) != "lane-log-policy" {
		t.Fatalf("captured output=%q err=%v", contents, err)
	}
}

func TestCaptureCLIAcceptsNoPartialLimitOverrides(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := RunCLIContext(t.Context(), []string{"--log-path", filepath.Join(t.TempDir(), "task.log"), "--active-file-bytes", "16777216", "--", "/bin/true"}, &stdout, &stderr); exit != 78 {
		t.Fatalf("partial custom limits exit=%d stderr=%q, want usage/config refusal", exit, stderr.String())
	}
}
