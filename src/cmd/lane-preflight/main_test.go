package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLanesL209PreflightCommandRequiresCompleteInputsWithoutWriting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run without inputs exit=%d, want 2", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "--profiles FILE") {
		t.Fatalf("unexpected preflight usage output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
