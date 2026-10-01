package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRevisionNormalizesOnlyFullLowercaseGitCommit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "default unattested", value: unattestedRevision, want: unattestedRevision},
		{name: "full lowercase commit", value: "ca3f08d2cf9143b280734ae4c23d783c0d39bde9", want: "ca3f08d2cf9143b280734ae4c23d783c0d39bde9"},
		{name: "short commit", value: "ca3f08d", want: unattestedRevision},
		{name: "uppercase commit", value: "CA3F08D2CF9143B280734AE4C23D783C0D39BDE9", want: unattestedRevision},
		{name: "non hexadecimal", value: "zz3f08d2cf9143b280734ae4c23d783c0d39bde9", want: unattestedRevision},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeRevision(test.value); got != test.want {
				t.Fatalf("normalizeRevision(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestRevisionUsesLinkerSettableSourceRevision(t *testing.T) {
	if got := Revision(); got != unattestedRevision {
		t.Fatalf("Revision() = %q, want default %q in an ordinary test build", got, unattestedRevision)
	}
}

func TestRevisionLinkerEmbeddingReachesVersionSurface(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate buildinfo test source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "runner-local")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")

	build := exec.Command(goBinary, "build",
		"-ldflags", "-X remote-session-runner/src/internal/buildinfo.SourceRevision="+revision,
		"-o", binaryPath, "./src/cmd/runner-local")
	build.Dir = repositoryRoot
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linker-attested runner-local: %v\n%s", err, output)
	}

	version := exec.Command(binaryPath, "--version")
	output, err := version.CombinedOutput()
	if err != nil {
		t.Fatalf("run linker-attested runner-local --version: %v\n%s", err, output)
	}
	want := "runner-local 0.0.0-dev build_revision=" + revision
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("linker-attested version = %q, want %q", got, want)
	}
}
