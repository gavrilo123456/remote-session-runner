package commandstub

import (
	"bytes"
	"strings"
	"testing"
)

func TestP001HelpAndVersion(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "help", args: []string{"--help"}, want: "Usage: runner [--help|--version]"},
		{name: "version", args: []string{"--version"}, want: "runner 0.0.0-dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run("runner", "test summary", tt.args, &stdout, &stderr); code != 0 {
				t.Fatalf("Run() exit code = %d, want 0", code)
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), tt.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestP001UnsupportedArgumentsDoNotEchoInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run("runner", "test summary", []string{"secret-script-content"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run() exit code = %d, want 2", code)
	}
	if strings.Contains(stderr.String(), "secret-script-content") {
		t.Fatalf("stderr echoed argument: %q", stderr.String())
	}
}
