package runnerlocal

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBUG008MacInstallerEmbedsAndAttestsServingBuildRevision(t *testing.T) {
	repositoryRoot := bug008MacRepositoryRoot(t)
	installerPath := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	installer := bug008ReadShellScript(t, installerPath)

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
		"health=$(/usr/bin/curl --silent --show-error --fail --unix-socket \"$socket\" http://runner/health/ready 2>/dev/null) || return 1",
		"printf '%s' \"$health\" |",
		"report.get(\"build_revision\") == sys.argv[1]",
		"quiesce_candidate_agents_after_start_failure()",
		"stop_candidate_agent_after_start_failure()",
		"candidate_startup_attempted=1",
		"candidate_services_quiesced=0",
		"Candidate startup was attempted; quiescing candidate LaunchAgents",
		"Build-revision verification failed; quiescing candidate LaunchAgents",
		"wait_for_build_revision \"$service_root/run/locald.sock\" 'runner-locald'",
		"wait_for_build_revision \"$service_root/run/local-api.sock\" 'runner-local'",
	} {
		if !strings.Contains(installer, fragment) {
			t.Fatalf("Mac installer omits BUG-008 build-provenance fragment %q", fragment)
		}
	}
	configBootstrap := strings.Index(installer, "Created selected Mac config for review")
	cleanCheckout := strings.LastIndex(installer, "\nrequire_source_checkout\n")
	build := strings.Index(installer, "build -ldflags \"$build_ldflags\"")
	bootstrap := strings.LastIndex(installer, "launchctl bootstrap")
	attestLocald := strings.LastIndex(installer, "wait_for_build_revision \"$service_root/run/locald.sock\" 'runner-locald'")
	attestLocal := strings.LastIndex(installer, "wait_for_build_revision \"$service_root/run/local-api.sock\" 'runner-local'")
	quiesce := strings.LastIndex(installer, "quiesce_candidate_agents_after_start_failure")
	if configBootstrap < 0 || cleanCheckout < 0 || build < 0 || bootstrap < 0 || attestLocald < 0 || attestLocal < 0 || quiesce < 0 || !(configBootstrap < cleanCheckout && cleanCheckout < build && build < bootstrap && bootstrap < attestLocald && attestLocald < attestLocal && attestLocal < quiesce) {
		t.Fatalf("Mac installer provenance sequence is unsafe: config=%d clean=%d build=%d bootstrap=%d locald=%d local=%d quiesce=%d", configBootstrap, cleanCheckout, build, bootstrap, attestLocald, attestLocal, quiesce)
	}
	onExit := strings.Index(installer, "on_exit() {")
	quiesceGuard := strings.Index(installer, "if [ \"$candidate_startup_attempted\" -eq 1 ] && [ \"$candidate_services_quiesced\" -eq 0 ]; then")
	quiesceOnExit := strings.Index(installer, "if quiesce_candidate_agents_after_start_failure; then")
	bootstrapState := strings.Index(installer, "candidate_startup_attempted=1\n\tcandidate_services_quiesced=0\n\tlaunchctl bootstrap")
	if onExit < 0 || quiesceGuard < 0 || quiesceOnExit < 0 || bootstrapState < 0 || !(onExit < quiesceGuard && quiesceGuard < quiesceOnExit && bootstrapState < attestLocald) {
		t.Fatalf("Mac installer does not quiesce candidates after every post-start failure: on_exit=%d guard=%d quiesce=%d bootstrap_state=%d attest=%d", onExit, quiesceGuard, quiesceOnExit, bootstrapState, attestLocald)
	}

	hostGatePath := filepath.Join(repositoryRoot, "deploy", "macos", "test-launchagents.sh")
	hostGate := bug008ReadShellScript(t, hostGatePath)
	for _, fragment := range []string{
		"expected_build_revision=$(git -C \"$repo_root\" rev-parse HEAD)",
		"health_reports_build_revision()",
		"health=$(/usr/bin/curl --silent --show-error --fail --unix-socket \"$socket\" http://runner/health/ready 2>/dev/null) || return 1",
		"printf '%s' \"$health\" |",
		"report.get(\"build_revision\") == sys.argv[1]",
		"runner-locald initial start",
		"runner-local initial start",
		"runner-locald restart",
		"runner-local restart",
	} {
		if !strings.Contains(hostGate, fragment) {
			t.Fatalf("Mac LaunchAgent host gate omits BUG-008 build-provenance fragment %q", fragment)
		}
	}
	initialLocal := strings.Index(hostGate, "runner-local initial start")
	restartStop := strings.LastIndex(hostGate, "launchctl bootout \"gui/$uid\" \"$local_plist\"")
	restartedLocal := strings.Index(hostGate, "runner-local restart")
	if initialLocal < 0 || restartStop < 0 || restartedLocal < 0 || !(initialLocal < restartStop && restartStop < restartedLocal) {
		t.Fatalf("Mac host gate does not attest both start epochs: initial=%d stop=%d restart=%d", initialLocal, restartStop, restartedLocal)
	}
}

func bug008MacRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate BUG-008 Mac provenance test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func bug008ReadShellScript(t *testing.T, path string) string {
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
