package runnerd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBUG016LinuxInstallerStagesCandidateBeforeOfflineRecovery(t *testing.T) {
	repositoryRoot := bug008LinuxRepositoryRoot(t)
	installer := bug008LinuxReadShellScript(t, filepath.Join(repositoryRoot, "deploy", "linux", "install-systemd-service.sh"))

	for _, fragment := range []string{
		"linux_recover_stalled_mode=0",
		"linux_recovery_jobs=''",
		"linux_recovery_pairs=''",
		"linux_recovery_sessions=''",
		"--recover-stalled",
		"--job-id",
		"--lost-pair",
		"--lost-session",
		"--recover-stalled requires at least one --job-id, --lost-pair, or --lost-session.",
		"run_linux_recover_stalled() {",
		`set -- recover-stalled --config "$service_root/config/linux.yaml" --apply`,
		`set -- "$@" --job-id "$job"`,
		`set -- "$@" --lost-pair "$pair"`,
		`set -- "$@" --lost-session "$session"`,
		`"$service_root/bin/runnerd" "$@"`,
		"install_candidate() {",
		`mv -f "$temporary" "$service_root/bin/runnerd"`,
		"temporary=''",
		"Stalled Linux recovery did not complete; leaving runnerd.service stopped.",
	} {
		if !strings.Contains(installer, fragment) {
			t.Fatalf("BUG-016 Linux recovery installer omits %q", fragment)
		}
	}

	installFunction := strings.Index(installer, "install_candidate() {")
	if installFunction < 0 {
		t.Fatal("could not locate candidate-install function")
	}
	installFunctionEnd := strings.Index(installer[installFunction:], "\n}\n\n# Replacing a binary")
	if installFunctionEnd < 0 {
		t.Fatalf("could not isolate candidate-install function end: start=%d end=%d", installFunction, installFunctionEnd)
	}
	installFunctionText := installer[installFunction : installFunction+installFunctionEnd]
	for _, lifecycleCall := range []string{
		"systemctl stop runnerd.service",
		"systemctl restart runnerd.service",
		"systemctl start runnerd.service",
	} {
		if strings.Contains(installFunctionText, lifecycleCall) {
			t.Fatalf("candidate install must not signal the running service: %q", lifecycleCall)
		}
	}

	recoveryBranch := strings.LastIndex(installer, `if [ "$linux_recover_stalled_mode" -eq 1 ]; then`)
	startCandidate := strings.LastIndex(installer, "\tinstall_candidate\n\tif ! stop_runnerd_for_offline_recovery; then")
	markStopped := strings.LastIndex(installer, "\twas_active=0\n\tif ! run_linux_recover_stalled; then")
	postflight := strings.LastIndex(installer, "\trequire_no_active_work\nelse\n\tinstall_candidate")
	startService := strings.LastIndex(installer, "sudo -n systemctl start runnerd.service")
	if recoveryBranch < 0 || startCandidate < 0 || markStopped < 0 || postflight < 0 || startService < 0 || !(recoveryBranch < startCandidate && startCandidate < markStopped && markStopped < postflight && postflight < startService) {
		t.Fatalf("BUG-016 recovery ordering is unsafe: branch=%d stage_stop=%d mark_stopped=%d postflight=%d start=%d", recoveryBranch, startCandidate, markStopped, postflight, startService)
	}
}
