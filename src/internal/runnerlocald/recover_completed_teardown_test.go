package runnerlocald

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestBUG024MacTeardownPairParsesExactAndMixedSelections(t *testing.T) {
	valid := "sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001"
	for _, values := range [][]string{{"invalid"}, {valid, valid}, {valid + ":extra"}, {""}, {valid, "sess-00000000000000000000000000000002:cmd-00000000000000000000000000000001"}} {
		if _, err := parseMacCompletedTeardownRecoveryPairs(values); err == nil {
			t.Fatalf("invalid completed teardown selection accepted: %q", values)
		}
	}
	parsed, err := parseMacCompletedTeardownRecoveryPairs([]string{valid})
	if err != nil || len(parsed) != 1 || parsed[0].CommandID != "cmd-00000000000000000000000000000001" {
		t.Fatalf("valid selection=%+v err=%v", parsed, err)
	}
	for _, args := range [][]string{
		{"--config", "/fixture/mac.yaml", "--teardown-pair", valid},
		{"--config", "/fixture/mac.yaml", "--apply", "--teardown-pair", "invalid"},
		{"--config", "/fixture/mac.yaml", "--apply", "--teardown-pair", valid, "--teardown-pair", valid},
		{"--config", "/fixture/mac.yaml", "--apply", "--teardown-pair", valid, "--lost-session", "sess-00000000000000000000000000000002"},
	} {
		var stdout, stderr strings.Builder
		if code := runRecoverStalled(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr strings.Builder
	if code := runRecoverStalled([]string{"--config", "/fixture/mac.yaml", "--apply", "--teardown-pair", valid, "--lost-pair", "sess-00000000000000000000000000000002:cmd-00000000000000000000000000000002"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "configuration is not ready") {
		t.Fatalf("mixed pair/teardown selection was rejected before config load: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func seedBUG024MacCompletedTeardown(t *testing.T, authority *store.AuthorityStore, service *execution.Service, runtime *bug011P4Runtime, suffix string) execution.CompletedTeardownRecoveryRequest {
	t.Helper()
	ctx := context.Background()
	sessionID := domain.SessionID("session-bug024-cli-" + suffix)
	commandID := domain.CommandID("command-bug024-cli-" + suffix)
	jobID := domain.JobID("job-bug024-cli-" + suffix)
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"operation": "run", "environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"job_id": jobID, "session_id": sessionID, "command_id": commandID, "script": "printf completed", "source": map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.markRetained(sessionID)
	result, err := service.RunJob(ctx, execution.RunJobRequest{Acceptance: store.JobAcceptance{
		JobID: jobID, SessionID: sessionID, CommandID: commandID, Controller: controller,
		IdempotencyKey: "key-bug024-cli-" + suffix, RequestHash: hash, Environment: "mac-dev", Target: target,
		Source: domain.NewEmptySource(), Script: "printf completed", CanonicalPayload: canonical, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}, MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: store.DefaultSessionIdempotencyRetention})
	if !errors.Is(err, execution.ErrStopUnconfirmed) || result.Command.State != domain.CommandStateSucceeded || result.Session.State != domain.SessionStateLost || result.Job.Phase != store.JobPhaseLost {
		t.Fatalf("seed completed teardown=%+v err=%v", result, err)
	}
	return execution.CompletedTeardownRecoveryRequest{SessionID: sessionID, CommandID: commandID}
}

func TestBUG024MacCompletedTeardownInventoryRequiresExactOwnershipAndCompleteSet(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	requests := []execution.CompletedTeardownRecoveryRequest{
		seedBUG024MacCompletedTeardown(t, authority, service, runtime, "one"),
		seedBUG024MacCompletedTeardown(t, authority, service, runtime, "two"),
	}
	audits := 0
	auditor := macRecoveryOwnershipAuditor(func(_ context.Context, attributable map[string]struct{}) error {
		audits++
		if len(attributable) != len(requests) {
			t.Fatalf("ownership set=%+v", attributable)
		}
		for _, request := range requests {
			if _, ok := attributable[string(request.SessionID)]; !ok {
				t.Fatalf("missing selected owner %s", request.SessionID)
			}
		}
		return nil
	})
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, requests[:1]); err == nil || audits != 0 {
		t.Fatalf("omitted retained set err=%v audits=%d", err, audits)
	}
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, requests); err != nil || audits != 1 || runtime.recoveryCount() != 0 {
		t.Fatalf("complete read-only inventory err=%v audits=%d proof_calls=%d", err, audits, runtime.recoveryCount())
	}
	refusingAuditor := macRecoveryOwnershipAuditor(func(context.Context, map[string]struct{}) error { return errors.New("unattributed owner") })
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, refusingAuditor, requests); err == nil {
		t.Fatal("uncertain ownership passed completed teardown preflight")
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 2 {
		t.Fatalf("read-only preflight changed reservations=%d err=%v", reservations, err)
	}
	var stdout, stderr strings.Builder
	if code := recoverMacCompletedTeardownSet(context.Background(), authority, service, auditor, requests, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "recovered_teardown_pairs=2") {
		t.Fatalf("recover code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := requireMacStalledRecoveryPostflight(context.Background(), authority); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		command, err := authority.GetCommand(context.Background(), request.CommandID)
		if err != nil || command.State != domain.CommandStateSucceeded || runtime.executionCount(request.CommandID) != 1 {
			t.Fatalf("completed result changed/replayed: command=%+v err=%v executions=%d", command, err, runtime.executionCount(request.CommandID))
		}
	}
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, requests); err == nil {
		t.Fatal("fully finalized historical set accepted for offline recovery")
	}
}

func TestBUG024MacCompletedTeardownFinalizationRetrySelectsAllPendingAndDoesNotSignalAgain(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	requests := []execution.CompletedTeardownRecoveryRequest{
		seedBUG024MacCompletedTeardown(t, authority, service, runtime, "pending-one"),
		seedBUG024MacCompletedTeardown(t, authority, service, runtime, "pending-two"),
	}
	runtime.setFinalizationError(errors.New("marker finalization unavailable"))
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), requests); !errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed pending teardown err=%v", err)
	}
	if err := requireMacStalledRecoveryPostflight(context.Background(), authority); err == nil {
		t.Fatal("pending completed teardown passed postflight")
	}
	auditor := macRecoveryOwnershipAuditor(func(context.Context, map[string]struct{}) error { return nil })
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, requests[:1]); err == nil {
		t.Fatal("omitted pending teardown passed inventory")
	}
	if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, requests); err != nil {
		t.Fatalf("exact pending retry inventory: %v", err)
	}
	if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, []execution.LostRuntimeRecoveryRequest{{SessionID: requests[0].SessionID, CommandID: requests[0].CommandID}}); err == nil {
		t.Fatal("lost-command mode accepted pending completed teardown")
	}
	if err := requireMacFailedStartupRecoveryInventory(context.Background(), authority, auditor, []domain.SessionID{requests[0].SessionID}); err == nil {
		t.Fatal("failed-startup mode accepted pending completed teardown")
	}
	runtime.setFinalizationError(nil)
	var stdout, stderr strings.Builder
	if code := recoverMacCompletedTeardownSet(context.Background(), authority, service, auditor, requests, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "already_recovered_teardown_pairs=2") {
		t.Fatalf("retry code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if runtime.recoveryCount() != 2 || runtime.finalizationCount() != 4 {
		t.Fatalf("retry repeated cleanup proof/signals: proof=%d finalizations=%d", runtime.recoveryCount(), runtime.finalizationCount())
	}
}

func TestBUG024MacCompletedTeardownInventoryRefusesOtherWorkAndPendingLostFinalization(t *testing.T) {
	for _, kind := range []string{"queued_command", "nonterminal_job", "pending_lost_finalization"} {
		t.Run(kind, func(t *testing.T) {
			authority, service, runtime := newBUG011P4Service(t, true)
			var request execution.CompletedTeardownRecoveryRequest
			if kind == "pending_lost_finalization" {
				session, command := seedBUG011P4LostPair(t, authority, service, runtime, "pending-lost")
				runtime.setFinalizationError(errors.New("marker unavailable"))
				if _, err := service.RecoverLostRuntimeBatch(context.Background(), []execution.LostRuntimeRecoveryRequest{{SessionID: session.SessionID, CommandID: command.CommandID}}); !errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) {
					t.Fatalf("seed old finalization: %v", err)
				}
				runtime.setFinalizationError(nil)
			}
			request = seedBUG024MacCompletedTeardown(t, authority, service, runtime, kind)
			switch kind {
			case "queued_command":
				seedBUG011P4QueuedCommand(t, authority, service, "other-queued")
			case "nonterminal_job":
				acceptBUG011P4QueuedOneOff(t, authority, service)
			}
			audited := false
			auditor := macRecoveryOwnershipAuditor(func(context.Context, map[string]struct{}) error { audited = true; return nil })
			if err := requireMacCompletedTeardownRecoveryInventory(context.Background(), authority, service, auditor, []execution.CompletedTeardownRecoveryRequest{request}); err == nil || audited {
				t.Fatalf("other work accepted: err=%v audited=%v", err, audited)
			}
			reservation, err := authority.GetSessionReservation(context.Background(), request.SessionID)
			if err != nil || reservation.ReleasedAt != nil {
				t.Fatalf("refusal released selected reservation=%+v err=%v", reservation, err)
			}
		})
	}
}

func TestBUG024MacInstallerTeardownSelectionAndForwarding(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "..", "deploy", "macos", "install-launchagents.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("installer syntax: %v %s", err, output)
	}
	text := string(script)
	start, end := strings.Index(text, "config_source=''"), strings.Index(text, "if [ ! -x \"$go_bin\" ]; then")
	if start < 0 || end <= start {
		t.Fatal("cannot isolate side-effect-free installer parsing")
	}
	parser := "#!/bin/sh\nset -eu\n" + text[start:end] + "printf 'accepted\\n'\n"
	parserPath := filepath.Join(t.TempDir(), "parse.sh")
	if err := os.WriteFile(parserPath, []byte(parser), 0o700); err != nil {
		t.Fatal(err)
	}
	pair := "sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001"
	for _, args := range [][]string{{"--teardown-pair", pair}, {"--recover-stalled", "--teardown-pair"}, {"--recover-stalled", "--teardown-pair", "unsafe/value"}, {"--recover-stalled", "--teardown-pair", pair, "--lost-session", "sess-00000000000000000000000000000002"}, {"--recover-failed-startup", "--teardown-pair", pair}} {
		output, err := exec.Command("sh", append([]string{parserPath}, args...)...).CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 || strings.Contains(string(output), "accepted") {
			t.Fatalf("installer invalid args=%q err=%v output=%s", args, err, output)
		}
	}
	if output, err := exec.Command("sh", parserPath, "--recover-stalled", "--teardown-pair", pair, "--lost-pair", pair).CombinedOutput(); err != nil || string(output) != "accepted\n" {
		t.Fatalf("installer mixed lost/teardown selection err=%v output=%s", err, output)
	}
	if output, err := exec.Command("sh", parserPath, "--recover-stalled", "--teardown-pair", pair).CombinedOutput(); err != nil || string(output) != "accepted\n" {
		t.Fatalf("installer valid selection err=%v output=%s", err, output)
	}
	functionStart := strings.Index(text, "run_mac_recover_stalled() {")
	if functionStart < 0 {
		t.Fatal("missing installer recovery function")
	}
	functionEnd := strings.Index(text[functionStart:], "\n}\n")
	if functionEnd < 0 {
		t.Fatal("cannot isolate installer recovery forwarding")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runner-locald"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	harness := "#!/bin/sh\nset -eu\nstaging_directory=$1\nconfig_file='/fixture/active config/mac.yaml'\nmac_recovery_pairs=''\nmac_recovery_sessions=''\nmac_teardown_pairs=$2\n" + text[functionStart:functionStart+functionEnd+3] + "\nrun_mac_recover_stalled\n"
	harnessPath := filepath.Join(root, "forward.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o700); err != nil {
		t.Fatal(err)
	}
	second := "sess-00000000000000000000000000000002:cmd-00000000000000000000000000000002"
	output, err := exec.Command("sh", harnessPath, root, pair+"\n"+second).CombinedOutput()
	want := "recover-stalled\n--config\n/fixture/active config/mac.yaml\n--apply\n--teardown-pair\n" + pair + "\n--teardown-pair\n" + second + "\n"
	if err != nil || string(output) != want {
		t.Fatalf("installer forwarding err=%v got=%q want=%q", err, output, want)
	}
}
