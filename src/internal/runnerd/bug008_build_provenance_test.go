package runnerd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBUG008LinuxInstallerEmbedsAndAttestsServingBuildRevision(t *testing.T) {
	repositoryRoot := bug008LinuxRepositoryRoot(t)
	installerPath := filepath.Join(repositoryRoot, "deploy", "linux", "install-systemd-service.sh")
	installer := bug008LinuxReadShellScript(t, installerPath)

	for _, fragment := range []string{
		"require_source_checkout()",
		"branch --show-current",
		"status --porcelain --untracked-files=all",
		"source_revision=$(git -C \"$repo_root\" rev-parse HEAD)",
		"rev-parse origin/dev",
		"^[0-9a-f]{40}$",
		"if [ \"$source_revision\" != \"$source_origin_revision\" ]; then",
		"build_ldflags=\"-X remote-session-runner/src/internal/buildinfo.SourceRevision=$source_revision\"",
		"build -ldflags \"$build_ldflags\"",
		"health_reports_build_revision()",
		"health=$(curl --silent --show-error --fail --unix-socket \"$socket\" http://runner/health/ready 2>/dev/null) || return 1",
		"printf '%s' \"$health\" |",
		"report.get(\"build_revision\") == sys.argv[1]",
		"quiesce_candidate_service_after_start_failure()",
		"systemctl stop runnerd.service",
		"candidate_startup_attempted=1",
		"candidate_service_quiesced=0",
		"Candidate startup was attempted; stopping candidate runnerd.service",
		"Build-revision verification failed; stopping candidate runnerd.service",
		"wait_for_build_revision \"$service_root/run/runnerd.sock\" 'runnerd.service'",
		"go_build_cache='/home/ubuntu/.cache/go-build'",
		"go_mod_cache='/home/ubuntu/go/pkg/mod'",
		"GOCACHE=\"$go_build_cache\" GOMODCACHE=\"$go_mod_cache\"",
	} {
		if !strings.Contains(installer, fragment) {
			t.Fatalf("Linux installer omits BUG-008 build-provenance fragment %q", fragment)
		}
	}
	for _, obsolete := range []string{
		"go_cache_root",
		"install-go-cache.",
		"queued-bridge-go-cache.",
		"remove_go_cache()",
		"rm -rf -- \"$go_cache_root\"",
	} {
		if strings.Contains(installer, obsolete) {
			t.Fatalf("Linux installer must reuse the selected account Go caches, found obsolete private-cache fragment %q", obsolete)
		}
	}
	if got := strings.Count(installer, "GOCACHE=\"$go_build_cache\" GOMODCACHE=\"$go_mod_cache\""); got < 2 {
		t.Fatalf("Linux installer uses the normal Go-cache pair %d times, want both status and build paths", got)
	}
	cleanCheckout := strings.Index(installer, "require_source_checkout\n\nensure_private_directory")
	build := strings.Index(installer, "build -ldflags \"$build_ldflags\"")
	restart := strings.Index(installer, "systemctl restart runnerd.service")
	attest := strings.LastIndex(installer, "wait_for_build_revision \"$service_root/run/runnerd.sock\" 'runnerd.service'")
	stopCandidate := strings.LastIndex(installer, "quiesce_candidate_service_after_start_failure")
	if cleanCheckout < 0 || build < 0 || restart < 0 || attest < 0 || stopCandidate < 0 || !(cleanCheckout < build && build < restart && restart < attest && attest < stopCandidate) {
		t.Fatalf("Linux installer provenance sequence is unsafe: clean=%d build=%d restart=%d attest=%d stop=%d", cleanCheckout, build, restart, attest, stopCandidate)
	}
	cleanup := strings.Index(installer, "cleanup() {")
	quiesceGuard := strings.Index(installer, "if [ \"$cleanup_status\" -ne 0 ] && [ \"$candidate_startup_attempted\" -eq 1 ] && [ \"$candidate_service_quiesced\" -eq 0 ]; then")
	quiesceOnFailure := strings.Index(installer, "if quiesce_candidate_service_after_start_failure; then")
	restartState := strings.Index(installer, "candidate_startup_attempted=1\n\tcandidate_service_quiesced=0\n\tsudo -n systemctl restart runnerd.service")
	startState := strings.Index(installer, "candidate_startup_attempted=1\n\tcandidate_service_quiesced=0\n\tsudo -n systemctl start runnerd.service")
	if cleanup < 0 || quiesceGuard < 0 || quiesceOnFailure < 0 || restartState < 0 || startState < 0 || !(cleanup < quiesceGuard && quiesceGuard < quiesceOnFailure) {
		t.Fatalf("Linux installer does not quiesce candidates after every post-start failure: cleanup=%d guard=%d quiesce=%d restart_state=%d start_state=%d", cleanup, quiesceGuard, quiesceOnFailure, restartState, startState)
	}

	hostGatePath := filepath.Join(repositoryRoot, "deploy", "linux", "test-systemd-service.sh")
	hostGate := bug008LinuxReadShellScript(t, hostGatePath)
	for _, fragment := range []string{
		"expected_build_revision=$(git -C \"$repo_root\" rev-parse HEAD)",
		"health_reports_build_revision()",
		"health=$(curl --silent --show-error --fail --unix-socket \"$service_root/run/runnerd.sock\" http://runner/health/ready 2>/dev/null) || return 1",
		"printf '%s' \"$health\" |",
		"report.get(\"build_revision\") == sys.argv[1]",
		"expected build revision",
		"installer_cache_snapshot()",
		"installer_caches_before=$(installer_cache_snapshot)",
		"installer_caches_after=$(installer_cache_snapshot)",
		"queued-bridge-go-cache.*",
		"runnerd installer created a new persistent private Go cache",
	} {
		if !strings.Contains(hostGate, fragment) {
			t.Fatalf("Linux systemd host gate omits BUG-008 build-provenance fragment %q", fragment)
		}
	}
}

func TestBUG008QueuedBridgeReusesSelectedAccountGoCaches(t *testing.T) {
	repositoryRoot := bug008LinuxRepositoryRoot(t)
	bridgePath := filepath.Join(repositoryRoot, "deploy", "ssh", "install-queued-bridge.sh")
	bridge := bug008LinuxReadShellScript(t, bridgePath)
	for _, fragment := range []string{
		"go_build_cache='/home/ubuntu/.cache/go-build'",
		"go_mod_cache='/home/ubuntu/go/pkg/mod'",
		"GOCACHE=\"$go_build_cache\" GOMODCACHE=\"$go_mod_cache\"",
	} {
		if !strings.Contains(bridge, fragment) {
			t.Fatalf("queued bridge omits normal Ubuntu Go-cache fragment %q", fragment)
		}
	}
	for _, obsolete := range []string{
		"work_dir",
		"install-go-cache.",
		"queued-bridge-go-cache.",
		"remove_go_cache()",
		"GOCACHE=\"$work_dir/build\"",
	} {
		if strings.Contains(bridge, obsolete) {
			t.Fatalf("queued bridge must not create or delete a private per-invocation Go cache, found %q", obsolete)
		}
	}
}

func bug008LinuxRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate BUG-008 Linux provenance test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func bug008LinuxReadShellScript(t *testing.T, path string) string {
	t.Helper()
	if err := exec.Command("sh", "-n", path).Run(); err != nil {
		t.Fatalf("shell syntax %s: %v", path, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
