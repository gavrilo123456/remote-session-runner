package releaseflow

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type promotionCLIBackendState struct {
	Selected               map[string]bool `json:"selected"`
	Prepared               bool            `json:"prepared"`
	FailPrepareAfterEffect bool            `json:"fail_prepare_after_effect"`
	FailSwitchAfterEffect  string          `json:"fail_switch_after_effect"`
	FailBranch             bool            `json:"fail_branch"`
}

// TestPromotionCLIHelper is the subprocess entry point for public-command
// tests. The fixture seam exists only in this _test.go binary; production
// commands have no environment-variable or fixture-root backend switch.
func TestPromotionCLIHelper(t *testing.T) {
	if os.Getenv("PROMO_CLI_HELPER") != "1" {
		return
	}
	statePath := os.Getenv("PROMO_CLI_STATE")
	state := promotionCLIBackendState{Selected: map[string]bool{}}
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			os.Exit(90)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		os.Exit(90)
	}
	backend := &fakeBackend{selected: state.Selected, prepared: state.Prepared,
		failPrepareAfterEffect: state.FailPrepareAfterEffect, failSwitchAfterEffect: state.FailSwitchAfterEffect,
		failBranch: state.FailBranch}
	promotionBackendForTests = backend
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(91)
	}
	var code int
	command := os.Args[separator+1]
	if command == "runner-release" {
		code = RunAdminCLI(os.Args[separator+2:], os.Stdout, os.Stderr)
	} else if command == "promote-to" {
		code = RunPromoteCLI(os.Args[separator+2:], os.Stdout, os.Stderr)
	} else {
		os.Exit(91)
	}
	state = promotionCLIBackendState{Selected: backend.selected, Prepared: backend.prepared,
		FailPrepareAfterEffect: backend.failPrepareAfterEffect, FailSwitchAfterEffect: backend.failSwitchAfterEffect,
		FailBranch: backend.failBranch}
	data, err := json.Marshal(state)
	if err != nil || os.WriteFile(statePath, data, 0o600) != nil {
		os.Exit(92)
	}
	os.Exit(code)
}

func TestBuildCLIHelper(t *testing.T) {
	if os.Getenv("PROMO_BUILD_CLI_HELPER") != "1" {
		return
	}
	repository := os.Getenv("PROMO_BUILD_REPOSITORY")
	controller := os.Getenv("PROMO_BUILD_CONTROLLER")
	artifactRoot := os.Getenv("PROMO_BUILD_ARTIFACT_ROOT")
	buildBackendForTests = &fakeBuildBackend{root: artifactRoot}
	buildCLIEnvironmentForTests = &buildCLIEnvironment{Repository: repository, ControllerRevision: controller,
		Resolver: GitResolver{Repository: repository, Primary: "origin", Mirror: "github", TempRoot: os.TempDir()}}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) || os.Args[separator+1] != "build-release" {
		os.Exit(91)
	}
	os.Exit(RunBuildCLI(os.Args[separator+2:], os.Stdout, os.Stderr))
}

func runPromotionCLIChild(t *testing.T, statePath string, args ...string) (string, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandArgs := []string{"-test.run=^TestPromotionCLIHelper$", "--"}
	commandArgs = append(commandArgs, args...)
	cmd := exec.Command(executable, commandArgs...)
	cmd.Env = append(os.Environ(), "PROMO_CLI_HELPER=1", "PROMO_CLI_STATE="+statePath)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func writePromotionCLIState(t *testing.T, path string, state promotionCLIBackendState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func saveCLIRelease(t *testing.T, store *Store) Release {
	t.Helper()
	release := testRelease()
	saveTestRelease(t, store, release)
	return release
}

func cliPromotionArgs(lane, target, release, operation, root string) []string {
	return []string{lane, target, "--release", release, "--operation-id", operation, "--profile", "profile-v1",
		"--store-root", root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json"}
}

func TestPublicPromoteSubprocessInterruptionRetryAndRollback(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailSwitchAfterEffect: "linux-host"})
	opID := strings.Repeat("1", 32)
	args := cliPromotionArgs("dev", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err == nil {
		t.Fatalf("expected interruption after the first selector effect; output=%s", output)
	}
	op, loadErr := store.LoadOperation("dev", opID)
	if loadErr != nil || op.Stage != StageBlocked {
		t.Fatalf("interrupted operation not durably blocked: stage=%s err=%v output=%s", op.Stage, loadErr, output)
	}
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state promotionCLIBackendState
	if json.Unmarshal(stateData, &state) != nil || !state.Selected["linux-host"] {
		t.Fatalf("fixture did not preserve the completed selector effect: %s", stateData)
	}
	output, err = runPromotionCLIChild(t, statePath, "promote-to", "dev", "all", "--release", release.ID,
		"--resume", opID, "--profile", "profile-v1", "--store-root", store.Root,
		"--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control", "--publishers-paused", "--no-new-accepted", "--json")
	if err != nil {
		t.Fatalf("resume failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("resumed public command did not complete: stage=%s err=%v output=%s", op.Stage, err, output)
	}
	output, err = runPromotionCLIChild(t, statePath, "runner-release", "rollback", "--lane", "dev", "--operation", opID,
		"--store-root", store.Root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json")
	if err != nil {
		t.Fatalf("public rollback failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageRollbackComplete {
		t.Fatalf("public rollback did not complete: stage=%s err=%v output=%s", op.Stage, err, output)
	}
}

func TestPublicPromoteSubprocessRetriesPartialPreparation(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailPrepareAfterEffect: true})
	opID := strings.Repeat("2", 32)
	args := cliPromotionArgs("dev", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err == nil {
		t.Fatalf("expected partial common-preparation interruption; output=%s", output)
	}
	op, loadErr := store.LoadOperation("dev", opID)
	if loadErr != nil || op.Stage != StageBlocked {
		t.Fatalf("partial preparation was not recorded: stage=%s err=%v", op.Stage, loadErr)
	}
	resumeArgs := []string{"promote-to", "dev", "all", "--release", release.ID, "--resume", opID, "--profile", "profile-v1",
		"--store-root", store.Root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json"}
	output, err = runPromotionCLIChild(t, statePath, resumeArgs...)
	if err != nil {
		t.Fatalf("retry after partial preparation failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("retry did not finish the exact operation: stage=%s err=%v", op.Stage, err)
	}
}

func TestPublicPromotionCLIRejectsExpiredPRDApproval(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	plan, err := BuildPlan("prd", "all", release)
	if err != nil {
		t.Fatal(err)
	}
	predecessors := map[string]string{}
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("predecessor:" + host))
	}
	approval := Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest, HostSet: append([]string(nil), HostOrder...),
		WindowStart: "2020-01-01T00:00:00Z", WindowEnd: "2020-01-01T01:00:00Z", Owner: "owner", Scope: "fixture only",
		PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: plan.BackupRecoveryPackageSHA}
	approvalPath := filepath.Join(t.TempDir(), "expired-approval.json")
	encoded, _ := json.Marshal(approval)
	if err := os.WriteFile(approvalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"promote-to", "prd", "all", "--release", release.ID, "--approval-file", approvalPath,
		"--store-root", store.Root, "--json"}
	output, err := runPromotionCLIChild(t, filepath.Join(t.TempDir(), "state.json"), args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(output, ErrApprovalRequired.Error()) {
		t.Fatalf("expired PRD approval was not rejected before effects: err=%v output=%s", err, output)
	}
	if entries, readErr := os.ReadDir(filepath.Join(store.Root, "operations", "prd")); readErr == nil && len(entries) != 0 {
		t.Fatalf("expired approval created a deployment operation: %v", entries)
	}
}

func TestPublicBranchSyncCommandRetriesGitOnly(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	saveTestAcceptance(t, store, "dev", release, time.Now())
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailBranch: true})
	opID := strings.Repeat("3", 32)
	args := cliPromotionArgs("qas", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err != nil || !strings.Contains(output, "runtime_accepted_branch_sync_pending") {
		t.Fatalf("expected accepted runtime with Git-only pending status: err=%v output=%s", err, output)
	}
	op, err := store.LoadOperation("qas", opID)
	if err != nil || op.Stage != StageBranchSyncPending || op.BranchSync != "pending" {
		t.Fatalf("branch race was not represented as pending: stage=%s sync=%s err=%v", op.Stage, op.BranchSync, err)
	}
	output, err = runPromotionCLIChild(t, statePath, "runner-release", "branch-sync", "--lane", "qas", "--operation", opID, "--store-root", store.Root, "--json")
	if err != nil {
		t.Fatalf("Git-only branch synchronization retry failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("qas", opID)
	if err != nil || op.Stage != StageComplete || op.BranchSync != "complete" {
		t.Fatalf("branch-only retry did not complete: stage=%s sync=%s err=%v", op.Stage, op.BranchSync, err)
	}
}

func TestAcceptanceExpiryFixtureClock(t *testing.T) {
	// The CLI approval expiry check is wall-clock-bound by design; this fixed
	// reference makes the boundary rule itself explicit in test diagnostics.
	if time.Now().Before(time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC)) {
		t.Fatal("unexpected system time before expired fixture")
	}
}

func TestPublicCommandHelpDescribesEffectsAndGuards(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := RunBuildCLI([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "without selecting or restarting") {
		t.Fatalf("build-release help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunPromoteCLI([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "all four hosts") || !strings.Contains(stdout.String(), "owner approval") {
		t.Fatalf("promote-to help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI([]string{"help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "branch-sync") || !strings.Contains(stdout.String(), "does not restore databases") {
		t.Fatalf("runner-release help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPublicBuildCLISealsAndPublishesFrozenBranchCommit(t *testing.T) {
	root := t.TempDir()
	resolver, commit := buildSourceFixture(t, root)
	store, err := NewStore(filepath.Join(root, "runtime", "operations"))
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hostsPath := writeTestHostConfiguration(t, root)
	if _, err := LoadHostConfiguration(hostsPath); err != nil {
		data, _ := os.ReadFile(hostsPath)
		t.Fatalf("fixture host configuration rejected before subprocess: %v; %s", err, data)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestBuildCLIHelper$", "--", "build-release", "--branch", "dev", "--commit", commit,
		"--release-id", "release-cli-public", "--store-root", store.Root, "--hosts-file", hostsPath}
	for _, receipt := range fixtureSourceTestReceipts(commit) {
		data, marshalErr := json.MarshalIndent(receipt, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		path := filepath.Join(root, "source-test-"+receipt.HostID+".json")
		if writeErr := os.WriteFile(path, append(data, '\n'), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		args = append(args, "--source-test-receipt", path)
	}
	args = append(args, "--json")
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), "PROMO_BUILD_CLI_HELPER=1", "PROMO_BUILD_REPOSITORY="+resolver.Repository,
		"PROMO_BUILD_CONTROLLER="+strings.Repeat("b", 40), "PROMO_BUILD_ARTIFACT_ROOT="+artifactRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("public build-release failed: %v: %s", err, output)
	}
	var cliResult BuildResult
	if err := json.Unmarshal(output, &cliResult); err != nil {
		t.Fatalf("decode public build result: %v: %s", err, output)
	}
	release, err := store.LoadRelease("release-cli-public")
	if err != nil {
		t.Fatalf("sealed public-command release missing: %v: %s", err, output)
	}
	buildRecord, err := store.LoadBuild(cliResult.Record.BuildID)
	if err != nil {
		t.Fatalf("public build operation record missing: %v", err)
	}
	if release.Source.Commit != commit || release.Source.Branch != "dev" || !release.Sealed || len(buildRecord.PublishedHosts) != len(HostOrder) || len(buildRecord.QualificationReceiptRefs) != len(HostOrder) {
		t.Fatalf("public build did not freeze and publish the expected release: %+v", release)
	}
	var statusOutput, statusErrors strings.Builder
	if code := RunAdminCLI([]string{"status", "--release", release.ID, "--store-root", store.Root, "--json"}, &statusOutput, &statusErrors); code != 0 || !strings.Contains(statusOutput.String(), "qualification_receipt_refs") {
		t.Fatalf("release status omitted verified provenance: code=%d stdout=%s stderr=%s", code, statusOutput.String(), statusErrors.String())
	}
	for _, host := range HostOrder {
		if release.Bundles[host].SourceRevision != commit || release.Bundles[host].HostID != host {
			t.Fatalf("public build manifest has wrong source or host bundle for %s: %+v", host, release.Bundles[host])
		}
	}
}

func writeTestHostConfiguration(t *testing.T, root string) string {
	t.Helper()
	configRoot := filepath.Join(root, "host-config")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := make(map[string]HostProfile, len(HostOrder))
	for _, host := range HostOrder {
		identity := filepath.Join(configRoot, host+".key")
		knownHosts := filepath.Join(configRoot, host+".known_hosts")
		for path, body := range map[string][]byte{identity: []byte("test identity\n"), knownHosts: []byte("test host key\n")} {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		profile := HostProfile{HostID: host, GitIdentityFile: identity, GitKnownHostsFile: knownHosts,
			Repository: filepath.Join(root, "worker", host), GoBinary: "/usr/bin/go", RuntimeRoot: filepath.Join(root, "runtime", host)}
		if host != "mac-control-plane" {
			profile.Target = "ubuntu@" + host
			profile.IdentityFile = identity
			profile.KnownHostsFile = knownHosts
		}
		hosts[host] = profile
	}
	data, err := json.Marshal(HostConfiguration{FormatVersion: 1, Hosts: hosts})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configRoot, "runner-release-hosts.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
