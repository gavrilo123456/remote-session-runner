package runnerd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRecoverLostRequiresExplicitTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"recover-lost", "--config", "/fixture/linux.yaml"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("recover-lost exit=%d stderr=%q, want usage error", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--config, --session-id, and --command-id are required") {
		t.Fatalf("recover-lost usage=%q", stderr.String())
	}
}
