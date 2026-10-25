package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

const catalogSubprocessFixtureRootEnv = "RSR_CATALOG_SUBPROCESS_FIXTURE_ROOT"

var catalogFixtureSwitchOrder = []string{"linux-host", "sandbox-host", "factory-sandbox", "mac-control-plane"}

// TestCatalogPublicCommandSequenceSubprocess invokes the actual runner-lane
// public dispatcher in a child test process. The child installs a package
// test-only backend over disposable PRD-shaped state; no production command
// flag or environment variable can select this backend.
func TestCatalogPublicCommandSequenceSubprocess(t *testing.T) {
	root := t.TempDir()
	first := prepareCatalogSubprocessFixture(t, root, "fixture-first", "first-bundle", nil)
	if err := os.WriteFile(filepath.Join(root, "interrupt-sandbox-once"), []byte("fixture-next\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := prepareCatalogSubprocessFixture(t, root, "fixture-next", "second-bundle", &first)
	first.RecoveryPairs = map[string][]string{
		"mac-control-plane": {"sess-mac-one:cmd-mac-one", "sess-mac-two:cmd-mac-two", "sess-mac-three:cmd-mac-three"},
		"linux-host":        {"sess-linux-one:cmd-linux-one"},
		"sandbox-host":      {"sess-sandbox-one:cmd-sandbox-one", "sess-sandbox-two:cmd-sandbox-two", "sess-sandbox-three:cmd-sandbox-three"},
	}
	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		state.SelectorTarget = ""
		state.ServiceActive = false
		state.AdmissionsClosed = true
		state.RetainedLostPairs = append([]string(nil), first.RecoveryPairs[hostID]...)
		state.RetainedHistory = []string{"original-command-result", "original-terminal-event", "held-ack-unchanged"}
		if err := writeCatalogFixtureJSON(filepath.Join(root, "hosts", hostID, "host-state.json"), state); err != nil {
			t.Fatal(err)
		}
	}
	refreshCatalogFixturePreflights(t, root, first)
	if err := writeCatalogFixtureJSON(filepath.Join(root, "seeds", "fixture-first.json"), first); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestCatalogPublicCommandSequenceSubprocessHelper$")
	command.Env = append(os.Environ(), catalogSubprocessFixtureRootEnv+"="+root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("public runner-lane subprocess sequence failed: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("CATALOG_SUBPROCESS_FIXTURE_SEQUENCE_OK")) {
		t.Fatalf("child process did not report the acceptance sequence result:\n%s", output)
	}
	first = readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-first-final.json"))
	second = readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-next-final.json"))
	third := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-third-final.json"))
	fourth := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-fourth-final.json"))

	firstReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "coordinator", first.OperationID+".json"))
	secondReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "coordinator", second.OperationID+".json"))
	thirdReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "coordinator", third.OperationID+".json"))
	fourthReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "coordinator", fourth.OperationID+".json"))
	if firstReceipt.Stage != laneinstall.CatalogCohortReleased {
		t.Fatalf("successful legacy adoption and return release stage=%s, want released", firstReceipt.Stage)
	}
	if secondReceipt.Stage != laneinstall.CatalogCohortRolledBack {
		t.Fatalf("interrupted migration recovery stage=%s, want rolled_back", secondReceipt.Stage)
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(firstReceipt); err != nil {
		t.Fatalf("completed historical receipt is invalid: %v", err)
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(secondReceipt); err != nil {
		t.Fatalf("rolled-back receipt is invalid: %v", err)
	}
	if thirdReceipt.Stage != laneinstall.CatalogCohortSuperseded || fourthReceipt.Stage != laneinstall.CatalogCohortRolledBack {
		t.Fatalf("verifying replacement stages predecessor=%s successor=%s", thirdReceipt.Stage, fourthReceipt.Stage)
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(thirdReceipt); err != nil {
		t.Fatalf("superseded verifying predecessor lost valid history: %v", err)
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(fourthReceipt); err != nil {
		t.Fatalf("held replacement successor receipt invalid: %v", err)
	}
	if len(thirdReceipt.Transitions) <= 4 || thirdReceipt.Transitions[len(thirdReceipt.Transitions)-1].Action != "supersede" {
		t.Fatalf("predecessor history does not include the supported replacement: %+v", thirdReceipt.Transitions)
	}

	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		if got := catalogFixtureDatabaseSentinel(t, state.DatabasePath); got != "retained" {
			t.Fatalf("host %s database sentinel=%q after switch and rollback", hostID, got)
		}
		if got := fileSHA256ForTest(t, state.DatabasePath); got != state.DatabaseSHA256 {
			t.Fatalf("host %s database bytes changed during binary-only rollout", hostID)
		}
		want := "releases/" + third.TargetBundleSHA[hostID]
		if state.SelectorTarget != want || state.ServiceActive || !state.AdmissionsClosed {
			t.Fatalf("replacement changed the selected baseline or reopened the stopped cohort on %s: %+v", hostID, state)
		}
		for _, operationID := range []string{first.OperationID, second.OperationID, third.OperationID, fourth.OperationID} {
			for _, peer := range catalogFixtureSwitchOrder {
				receipt := readCatalogFixtureReceipt(t, filepath.Join(root, "hosts", peer, "receipts", operationID+".json"))
				if receipt.OperationID != operationID {
					t.Fatalf("host %s has wrong synchronized receipt %s", peer, receipt.OperationID)
				}
				var coordinator laneinstall.CatalogCohortReceipt
				readCatalogFixtureJSON(t, filepath.Join(root, "coordinator", operationID+".json"), &coordinator)
				if len(receipt.Transitions) != len(coordinator.Transitions) || len(receipt.Transitions) == 0 ||
					receipt.Transitions[len(receipt.Transitions)-1].SHA256 != coordinator.Transitions[len(coordinator.Transitions)-1].SHA256 {
					t.Fatalf("host %s receipt %s is not synchronized to coordinator history", peer, operationID)
				}
			}
		}
	}
	if !fileExists(filepath.Join(root, "hosts", "sandbox-host", "journals", second.OperationID+"-sandbox-host.json")) {
		t.Fatal("interrupted selector journal was not retained")
	}
	if !fileExists(filepath.Join(root, "hosts", "sandbox-host", "journals", second.OperationID+"-sandbox-host-rollback.json")) {
		t.Fatal("reverse rollback journal was not retained")
	}
}

func TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocess(t *testing.T) {
	root := t.TempDir()
	prepareCatalogSubprocessFixture(t, root, "fixture-recovery-candidate", "recovery-candidate", nil)
	hostID := "mac-control-plane"
	previousID := "cat-fixture-prd-previous"
	previousSHA := fixtureDigest("previous sealed catalog release")
	previousBundleSHA := fixtureDigest("previous Mac bundle")
	state := readCatalogFixtureHost(t, root, hostID)
	state.SelectorTarget = "catalog/" + previousID + "/" + hostID
	state.SelectedReleaseID = previousID
	state.SelectedReleaseSHA = previousSHA
	state.SelectedBundleSHA = previousBundleSHA
	state.ServiceActive = false
	state.AdmissionsClosed = true
	state.RetainedLostPairs = []string{"sess-mac-one:cmd-mac-one", "sess-mac-two:cmd-mac-two"}
	state.RetainedHistory = []string{"original-lost-result", "original-terminal-event", "held-ack-unchanged"}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "hosts", hostID, "host-state.json"), state); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocessHelper$")
	command.Env = append(os.Environ(), catalogSubprocessFixtureRootEnv+"="+root)
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("CATALOG_SELECTED_PRD_RECOVERY_OK")) {
		t.Fatalf("selected-predecessor public recovery command failed: %v\n%s", err, output)
	}
	state = readCatalogFixtureHost(t, root, hostID)
	if state.SelectorTarget != "catalog/"+previousID+"/"+hostID || state.SelectedReleaseID != previousID ||
		state.SelectedReleaseSHA != previousSHA || state.SelectedBundleSHA != previousBundleSHA ||
		len(state.RetainedLostPairs) != 0 || state.ReplayCount != 0 ||
		strings.Join(state.RetainedHistory, ",") != "original-lost-result,original-terminal-event,held-ack-unchanged,recovery-release-proof" {
		t.Fatalf("recovery did not preserve selected release and retained history: %+v", state)
	}
}

// TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocessHelper is
// invoked only by the isolated parent fixture above.
func TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocessHelper(t *testing.T) {
	root := os.Getenv(catalogSubprocessFixtureRootEnv)
	if root == "" {
		return
	}
	backend := &catalogSubprocessFixtureBackend{root: root}
	catalogCommandSubprocessFixtureBackendForTests = backend
	seed := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-recovery-candidate.json"))
	state := readCatalogFixtureHost(t, root, "mac-control-plane")
	var preflight laneinstall.CatalogCohortHostPreflightReport
	readCatalogFixtureJSON(t, seed.PreflightPaths["mac-control-plane"], &preflight)
	base := []string{"catalog-prd-recover", "--host", "mac-control-plane", "--release-id", seed.ReleaseID,
		"--release-sha256", seed.ReleaseSHA, "--bundle-sha256", seed.TargetBundleSHA["mac-control-plane"],
		"--expected-selected-release-id", state.SelectedReleaseID,
		"--expected-selected-release-sha256", state.SelectedReleaseSHA,
		"--expected-selected-bundle-sha256", state.SelectedBundleSHA,
		"--backup-dir", preflight.Preflight.BackupDirectory,
		"--backup-manifest-sha256", preflight.Preflight.BackupManifestSHA256}

	var out, stderr bytes.Buffer
	wrong := append([]string(nil), base...)
	wrong[12] = fixtureDigest("different previous release")
	wrong = append(wrong, "--lost-pair", state.RetainedLostPairs[0], "--lost-pair", state.RetainedLostPairs[1])
	if status := run(wrong, &out, &stderr); status == 0 || !strings.Contains(stderr.String(), "fixture recovery pair set mismatch") {
		t.Fatalf("mismatched selected predecessor status=%d stdout=%q stderr=%q", status, out.String(), stderr.String())
	}
	stateAfterReject := readCatalogFixtureHost(t, root, "mac-control-plane")
	if !equalStrings(stateAfterReject.RetainedLostPairs, state.RetainedLostPairs) ||
		strings.Join(stateAfterReject.RetainedHistory, ",") != strings.Join(state.RetainedHistory, ",") {
		t.Fatal("mismatched predecessor attempt changed recovery state")
	}
	out.Reset()
	stderr.Reset()
	args := append([]string(nil), base...)
	args = append(args, "--lost-pair", state.RetainedLostPairs[0], "--lost-pair", state.RetainedLostPairs[1])
	if status := run(args, &out, &stderr); status != 0 {
		t.Fatalf("selected-predecessor recovery status=%d stdout=%q stderr=%q", status, out.String(), stderr.String())
	}
	fmt.Fprintln(os.Stdout, "CATALOG_SELECTED_PRD_RECOVERY_OK")
}

// TestCatalogPublicCommandSequenceSubprocessHelper is entered only by the
// subprocess parent above. The fixture root is consumed here, in test code;
// the normal runner-lane executable does not read this environment variable.
func TestCatalogPublicCommandSequenceSubprocessHelper(t *testing.T) {
	root := os.Getenv(catalogSubprocessFixtureRootEnv)
	if root == "" {
		return
	}
	backend := &catalogSubprocessFixtureBackend{root: root}
	catalogCommandSubprocessFixtureBackendForTests = backend
	first := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-first.json"))
	second := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-next.json"))
	var commandOutput bytes.Buffer
	invoke := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if status := run(args, &stdout, &stderr); status != 0 {
			t.Fatalf("runner-lane %v exited %d: %s", args, status, stderr.String())
		}
		commandOutput.Write(stdout.Bytes())
		return strings.TrimSpace(stdout.String())
	}
	invokeExpectError := func(want string, args ...string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if status := run(args, &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), want) {
			t.Fatalf("runner-lane %v status=%d stderr=%q; want error containing %q", args, status, stderr.String(), want)
		}
	}
	for _, hostID := range catalogFixtureSwitchOrder {
		pairs := first.RecoveryPairs[hostID]
		if len(pairs) == 0 {
			continue
		}
		var preflight laneinstall.CatalogCohortHostPreflightReport
		readCatalogFixtureJSON(t, first.PreflightPaths[hostID], &preflight)
		base := []string{"catalog-prd-recover", "--host", hostID, "--release-id", first.ReleaseID,
			"--release-sha256", first.ReleaseSHA, "--bundle-sha256", first.TargetBundleSHA[hostID],
			"--backup-dir", preflight.Preflight.BackupDirectory,
			"--backup-manifest-sha256", preflight.Preflight.BackupManifestSHA256}
		wrongBundle := append([]string(nil), base...)
		wrongBundle[8] = fixtureDigest("wrong candidate bundle")
		wrongBundle = append(wrongBundle, "--lost-pair", pairs[0])
		invokeExpectError("fixture recovery identity mismatch", wrongBundle...)
		incomplete := append([]string(nil), base...)
		badPairs := pairs[:len(pairs)-1]
		if len(badPairs) == 0 {
			badPairs = []string{"sess-extra:cmd-extra"}
		}
		for _, pair := range badPairs {
			incomplete = append(incomplete, "--lost-pair", pair)
		}
		invokeExpectError("fixture recovery pair set mismatch", incomplete...)
		args := append([]string(nil), base...)
		for _, pair := range pairs {
			args = append(args, "--lost-pair", pair)
		}
		invoke(args...)
		state := readCatalogFixtureHost(t, root, hostID)
		if state.ServiceActive || len(state.RetainedLostPairs) != 0 || state.ReplayCount != 0 ||
			!state.AdmissionsClosed || strings.Join(state.RetainedHistory, ",") != "original-command-result,original-terminal-event,held-ack-unchanged,recovery-release-proof" {
			t.Fatalf("fixture recovery changed unsafe state on %s: %+v", hostID, state)
		}
	}
	first.RecoveryPairs = nil
	refreshCatalogFixturePreflights(t, root, first)
	syncAll := func(operationID, sourceHost string) {
		t.Helper()
		path := filepath.Join(root, "hosts", sourceHost, "receipts", operationID+".json")
		for _, hostID := range catalogFixtureSwitchOrder {
			invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "sync", "--receipt-file", path)
		}
	}

	// Initial legacy adoption through the public command grammar.
	create := []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "create",
		"--release-manifest", first.ManifestPath, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause",
		"--publishers-paused", "--no-new-accepted"}
	for _, hostID := range catalogFixtureSwitchOrder {
		create = append(create, "--preflight-file", first.PreflightPaths[hostID])
	}
	created := invoke(create...)
	first.OperationID = fixtureOutputOperationID(t, created)
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "prepare", "--receipt-file",
		filepath.Join(root, "coordinator", first.OperationID+".json"))
	syncAll(first.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "fence-host", "--operation-id", first.OperationID,
			"--subject-host", hostID)
		syncAll(first.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "hold", "--operation-id", first.OperationID,
		"--evidence-sha256", fixtureDigest("initial hold"))
	syncAll(first.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "preflight", "--operation-id", first.OperationID,
			"--subject-host", hostID)
		syncAll(first.OperationID, hostID)
		target := ""
		selected := invoke("catalog-select", "--host", hostID, "--lane", "prd", "--release-id", first.ReleaseID,
			"--expected-target", target, "--cohort-operation-id", first.OperationID, "--evidence-sha256", fixtureDigest("select "+hostID))
		if fixtureOutputOperationID(t, selected) == "" {
			t.Fatalf("catalog-select %s did not return a selector journal", hostID)
		}
		syncAll(first.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "verify-start", "--operation-id", first.OperationID,
		"--evidence-sha256", fixtureDigest("verify-start"))
	syncAll(first.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "verify", "--operation-id", first.OperationID,
			"--subject-host", hostID, "--evidence-sha256", fixtureDigest("verify "+hostID))
		syncAll(first.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "complete", "--operation-id", first.OperationID,
		"--evidence-sha256", fixtureDigest("initial complete"))
	syncAll(first.OperationID, "mac-control-plane")

	// Return preparation releases the completed cohort while the existing
	// configuration still matches, then stops the services and synchronizes
	// that exact released history to every host before mailbox restoration.
	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		currentReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "hosts", hostID, "receipts", first.OperationID+".json"))
		if state.SelectorTarget != "releases/"+first.TargetBundleSHA[hostID] ||
			state.ConfigurationSHA != fixtureHostPreflight(currentReceipt, hostID).ConfigurationSHA256 {
			t.Fatalf("release precondition changed on %s: %+v", hostID, state)
		}
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "open", "--operation-id", first.OperationID,
		"--evidence-sha256", fixtureDigest("release before mailbox return"))
	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		state.ServiceActive = false // The supported selected-lane stop is represented by test-backend state.
		if err := backend.writeHost(state); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(first.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		released := readCatalogFixtureReceipt(t, filepath.Join(root, "hosts", hostID, "receipts", first.OperationID+".json"))
		state := readCatalogFixtureHost(t, root, hostID)
		if released.Stage != laneinstall.CatalogCohortReleased || len(released.Transitions) == 0 ||
			released.Transitions[len(released.Transitions)-1].Action != "open" || state.ServiceActive {
			t.Fatalf("release and stop state on %s = stage %s services_active=%t", hostID, released.Stage, state.ServiceActive)
		}
	}

	// A second candidate switches Linux, then crashes after Sandbox's durable
	// selector write. The public reconcile command records that switch, after
	// which supported rollback runs Sandbox then Linux with a sync after each.
	refreshCatalogFixturePreflights(t, root, second)
	create = []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "create",
		"--release-manifest", second.ManifestPath, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause",
		"--publishers-paused", "--no-new-accepted"}
	for _, hostID := range catalogFixtureSwitchOrder {
		create = append(create, "--preflight-file", second.PreflightPaths[hostID])
	}
	created = invoke(create...)
	second.OperationID = fixtureOutputOperationID(t, created)
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "prepare", "--receipt-file",
		filepath.Join(root, "coordinator", second.OperationID+".json"))
	syncAll(second.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "fence-host", "--operation-id", second.OperationID,
			"--subject-host", hostID)
		syncAll(second.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "hold", "--operation-id", second.OperationID,
		"--evidence-sha256", fixtureDigest("second hold"))
	syncAll(second.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder[:2] {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "preflight", "--operation-id", second.OperationID,
			"--subject-host", hostID)
		syncAll(second.OperationID, hostID)
		expected := "releases/" + first.TargetBundleSHA[hostID]
		selectArgs := []string{"catalog-select", "--host", hostID, "--lane", "prd", "--release-id", second.ReleaseID,
			"--expected-target", expected, "--cohort-operation-id", second.OperationID, "--evidence-sha256", fixtureDigest("second select " + hostID)}
		if hostID == "sandbox-host" {
			invokeExpectError("simulated crash after selector journal", selectArgs...)
			journal := filepath.Join(root, "hosts", hostID, "journals", second.OperationID+"-"+hostID+".json")
			var stored fixtureSelectorJournal
			readCatalogFixtureJSON(t, journal, &stored)
			invoke("catalog-reconcile", "--host", hostID, "--lane", "prd", "--cohort-operation-id", second.OperationID,
				"--selector-operation-id", stored.OperationID)
		} else {
			selected := invoke(selectArgs...)
			_ = fixtureOutputOperationID(t, selected)
		}
		syncAll(second.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "block", "--operation-id", second.OperationID,
		"--evidence-sha256", fixtureDigest("interrupted-switch block"))
	syncAll(second.OperationID, "mac-control-plane")
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "rollback-start", "--operation-id", second.OperationID,
		"--evidence-sha256", fixtureDigest("rollback-start"))
	syncAll(second.OperationID, "mac-control-plane")
	for _, hostID := range []string{"sandbox-host", "linux-host"} {
		journal := filepath.Join(root, "hosts", hostID, "journals", second.OperationID+"-"+hostID+".json")
		var stored fixtureSelectorJournal
		readCatalogFixtureJSON(t, journal, &stored)
		invoke("catalog-rollback", "--host", hostID, "--lane", "prd", "--cohort-operation-id", second.OperationID,
			"--selector-operation-id", stored.OperationID)
		syncAll(second.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "rollback-complete", "--operation-id", second.OperationID,
		"--evidence-sha256", fixtureDigest("rollback-complete"))
	syncAll(second.OperationID, "mac-control-plane")

	// Install a fully switched but unaccepted release and keep its services
	// stopped. A held successor must replace this exact verifying predecessor.
	third := prepareCatalogSubprocessFixture(t, root, "fixture-third", "third-bundle", &first)
	refreshCatalogFixturePreflights(t, root, third)
	create = []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "create",
		"--release-manifest", third.ManifestPath, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause",
		"--publishers-paused", "--no-new-accepted"}
	for _, hostID := range catalogFixtureSwitchOrder {
		create = append(create, "--preflight-file", third.PreflightPaths[hostID])
	}
	created = invoke(create...)
	third.OperationID = fixtureOutputOperationID(t, created)
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "prepare", "--receipt-file",
		filepath.Join(root, "coordinator", third.OperationID+".json"))
	syncAll(third.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "fence-host", "--operation-id", third.OperationID,
			"--subject-host", hostID)
		syncAll(third.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "hold", "--operation-id", third.OperationID,
		"--evidence-sha256", fixtureDigest("third hold"))
	syncAll(third.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "preflight", "--operation-id", third.OperationID,
			"--subject-host", hostID)
		syncAll(third.OperationID, hostID)
		expected := "releases/" + fixtureHostPreflight(readCatalogFixtureReceipt(t,
			filepath.Join(root, "hosts", hostID, "receipts", third.OperationID+".json")), hostID).PreviousBundleSHA256
		invoke("catalog-select", "--host", hostID, "--lane", "prd", "--release-id", third.ReleaseID,
			"--expected-target", expected, "--cohort-operation-id", third.OperationID,
			"--evidence-sha256", fixtureDigest("third select "+hostID))
		syncAll(third.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "verify-start", "--operation-id", third.OperationID,
		"--evidence-sha256", fixtureDigest("third verify-start"))
	syncAll(third.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		state.ServiceActive = false // Acceptance failed; recovery stops every selected service.
		state.AdmissionsClosed = true
		if err := backend.writeHost(state); err != nil {
			t.Fatal(err)
		}
	}
	thirdReceipt := readCatalogFixtureReceipt(t, filepath.Join(root, "coordinator", third.OperationID+".json"))
	if thirdReceipt.Stage != laneinstall.CatalogCohortVerifying || len(fixtureSwitchedHosts(thirdReceipt)) != len(catalogFixtureSwitchOrder) {
		t.Fatalf("replacement predecessor stage=%s switched=%v", thirdReceipt.Stage, fixtureSwitchedHosts(thirdReceipt))
	}

	// The replacement's reports are generated from the exact selected release,
	// stopped services, unchanged configuration/database and new backups.
	fourth := prepareCatalogSubprocessFixture(t, root, "fixture-fourth", "fourth-bundle", &third)
	refreshCatalogFixturePreflights(t, root, fourth)
	create = []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "create",
		"--release-manifest", fourth.ManifestPath, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause",
		"--publishers-paused", "--no-new-accepted"}
	for _, hostID := range catalogFixtureSwitchOrder {
		create = append(create, "--preflight-file", fourth.PreflightPaths[hostID])
	}
	created = invoke(create...)
	fourth.OperationID = fixtureOutputOperationID(t, created)
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "prepare", "--receipt-file",
		filepath.Join(root, "coordinator", fourth.OperationID+".json"))
	syncAll(fourth.OperationID, "mac-control-plane")
	for _, hostID := range catalogFixtureSwitchOrder {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "fence-host", "--operation-id", fourth.OperationID,
			"--subject-host", hostID, "--replacement-predecessor", third.OperationID)
		syncAll(fourth.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "hold", "--operation-id", fourth.OperationID,
		"--evidence-sha256", fixtureDigest("fourth replacement hold"))
	syncAll(fourth.OperationID, "mac-control-plane")

	// Interrupt after two host observations. A new backend instance reloads
	// the durable receipt and continues, proving command retry uses history.
	for _, hostID := range catalogFixtureSwitchOrder[:2] {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "supersede-host",
			"--operation-id", third.OperationID, "--successor-operation-id", fourth.OperationID, "--subject-host", hostID)
		syncAll(third.OperationID, hostID)
	}
	partial, err := backend.readReceipt("coordinator", third.OperationID)
	if err != nil || len(currentSupersededHostsForFixture(partial.Transitions)) != 2 || partial.Stage != laneinstall.CatalogCohortVerifying {
		t.Fatalf("partial replacement history stage=%s superseded=%v err=%v", partial.Stage, currentSupersededHostsForFixture(partial.Transitions), err)
	}
	catalogCommandSubprocessFixtureBackendForTests = nil
	backend = &catalogSubprocessFixtureBackend{root: root}
	catalogCommandSubprocessFixtureBackendForTests = backend
	for _, hostID := range catalogFixtureSwitchOrder[2:] {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "supersede-host",
			"--operation-id", third.OperationID, "--successor-operation-id", fourth.OperationID, "--subject-host", hostID)
		syncAll(third.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "supersede",
		"--operation-id", third.OperationID, "--successor-operation-id", fourth.OperationID)
	syncAll(third.OperationID, "mac-control-plane")

	// Continue the accepted replacement attempt, interrupt after Sandbox has
	// published its selector journal, reconcile, and roll back to the exact
	// selected predecessor release. The database remains byte-identical.
	if err := os.WriteFile(filepath.Join(root, "interrupt-sandbox-once"), []byte(fourth.ReleaseID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogFixtureSwitchOrder[:2] {
		invoke("catalog-cohort", "--host", hostID, "--lane", "prd", "--action", "preflight", "--operation-id", fourth.OperationID,
			"--subject-host", hostID)
		syncAll(fourth.OperationID, hostID)
		local := readCatalogFixtureReceipt(t, filepath.Join(root, "hosts", hostID, "receipts", fourth.OperationID+".json"))
		expected := fixtureHostPreflight(local, hostID).PreviousTarget
		selectArgs := []string{"catalog-select", "--host", hostID, "--lane", "prd", "--release-id", fourth.ReleaseID,
			"--expected-target", expected, "--cohort-operation-id", fourth.OperationID,
			"--evidence-sha256", fixtureDigest("fourth select " + hostID)}
		if hostID == "sandbox-host" {
			invokeExpectError("simulated crash after selector journal", selectArgs...)
			journal := filepath.Join(root, "hosts", hostID, "journals", fourth.OperationID+"-"+hostID+".json")
			var stored fixtureSelectorJournal
			readCatalogFixtureJSON(t, journal, &stored)
			invoke("catalog-reconcile", "--host", hostID, "--lane", "prd", "--cohort-operation-id", fourth.OperationID,
				"--selector-operation-id", stored.OperationID)
		} else {
			invoke(selectArgs...)
		}
		syncAll(fourth.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "block", "--operation-id", fourth.OperationID,
		"--evidence-sha256", fixtureDigest("replacement switch interrupted"))
	syncAll(fourth.OperationID, "mac-control-plane")
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "rollback-start", "--operation-id", fourth.OperationID,
		"--evidence-sha256", fixtureDigest("replacement rollback-start"))
	syncAll(fourth.OperationID, "mac-control-plane")
	for _, hostID := range []string{"sandbox-host", "linux-host"} {
		journal := filepath.Join(root, "hosts", hostID, "journals", fourth.OperationID+"-"+hostID+".json")
		var stored fixtureSelectorJournal
		readCatalogFixtureJSON(t, journal, &stored)
		invoke("catalog-rollback", "--host", hostID, "--lane", "prd", "--cohort-operation-id", fourth.OperationID,
			"--selector-operation-id", stored.OperationID)
		syncAll(fourth.OperationID, hostID)
	}
	invoke("catalog-cohort", "--host", "mac-control-plane", "--lane", "prd", "--action", "rollback-complete",
		"--operation-id", fourth.OperationID, "--evidence-sha256", fixtureDigest("replacement rollback-complete"))
	syncAll(fourth.OperationID, "mac-control-plane")

	if err := os.WriteFile(filepath.Join(root, "command-output.txt"), commandOutput.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "seeds", "fixture-first-final.json"), first); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "seeds", "fixture-next-final.json"), second); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "seeds", "fixture-third-final.json"), third); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "seeds", "fixture-fourth-final.json"), fourth); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "CATALOG_SUBPROCESS_FIXTURE_SEQUENCE_OK")
}

type catalogSubprocessFixtureSeed struct {
	OperationID     string              `json:"operation_id,omitempty"`
	ReleaseID       string              `json:"release_id"`
	ReleaseSHA      string              `json:"release_sha256"`
	ManifestPath    string              `json:"manifest_path"`
	PreflightPaths  map[string]string   `json:"preflight_paths"`
	TargetBundleSHA map[string]string   `json:"target_bundle_sha256"`
	RecoveryPairs   map[string][]string `json:"recovery_pairs,omitempty"`
}

type catalogSubprocessFixtureHost struct {
	HostID             string   `json:"host_id"`
	SelectorTarget     string   `json:"selector_target"`
	SelectedReleaseID  string   `json:"selected_release_id,omitempty"`
	SelectedReleaseSHA string   `json:"selected_release_sha256,omitempty"`
	SelectedBundleSHA  string   `json:"selected_bundle_sha256,omitempty"`
	LegacyBundleSHA256 string   `json:"legacy_bundle_sha256"`
	ServiceActive      bool     `json:"service_active"`
	ConfigurationPath  string   `json:"configuration_path"`
	ConfigurationSHA   string   `json:"configuration_sha256"`
	DatabasePath       string   `json:"database_path"`
	DatabaseSHA256     string   `json:"database_sha256"`
	SchemaVersion      int      `json:"schema_version"`
	AdmissionsClosed   bool     `json:"admissions_closed,omitempty"`
	RetainedLostPairs  []string `json:"retained_lost_pairs,omitempty"`
	RetainedHistory    []string `json:"retained_history,omitempty"`
	ReplayCount        int      `json:"replay_count,omitempty"`
}

type catalogSubprocessFixtureBackup struct {
	HostID           string `json:"host_id"`
	DatabaseSHA256   string `json:"database_sha256"`
	ConfigurationSHA string `json:"configuration_sha256"`
	SchemaVersion    int    `json:"schema_version"`
}

type fixtureSelectorJournal struct {
	OperationID       string `json:"operation_id"`
	CohortOperationID string `json:"cohort_operation_id"`
	HostID            string `json:"host_id"`
	PreviousTarget    string `json:"previous_target"`
	NextTarget        string `json:"next_target"`
	Stage             string `json:"stage"`
	Rollback          bool   `json:"rollback"`
	EvidenceSHA256    string `json:"evidence_sha256"`
	CreatedAt         string `json:"created_at"`
}

type catalogSubprocessFixtureBackend struct{ root string }

func (b *catalogSubprocessFixtureBackend) RunPRDRecovery(ctx context.Context, command catalogPRDRecoveryCommand, stdout, stderr io.Writer) error {
	if ctx == nil || !containsCatalogFixtureHost(command.HostID) || command.ReleaseID == "" ||
		!fixtureValidDigest(command.ExpectedReleaseSHA256) || !fixtureValidDigest(command.ExpectedBundleSHA256) ||
		!fixtureValidDigest(command.ExpectedBackupManifestSHA) || len(command.LostPairs) == 0 {
		return laneinstall.ErrCatalogPRDRecovery
	}
	var seed catalogSubprocessFixtureSeed
	if err := readCatalogFixtureJSON(nil, filepath.Join(b.root, "seeds", command.ReleaseID+".json"), &seed); err != nil {
		return err
	}
	if seed.ReleaseSHA != command.ExpectedReleaseSHA256 || seed.TargetBundleSHA[command.HostID] != command.ExpectedBundleSHA256 {
		return errors.New("fixture recovery identity mismatch")
	}
	state, err := b.readHost(command.HostID)
	selectorMatches := state.SelectorTarget == "" && command.ExpectedSelectedReleaseID == "" && command.ExpectedSelectedReleaseSHA256 == "" && command.ExpectedSelectedBundleSHA256 == ""
	if command.ExpectedSelectedReleaseID != "" && command.ExpectedSelectedReleaseSHA256 != "" && command.ExpectedSelectedBundleSHA256 != "" {
		selectorMatches = state.SelectorTarget == "catalog/"+command.ExpectedSelectedReleaseID+"/"+command.HostID &&
			state.SelectedReleaseID == command.ExpectedSelectedReleaseID && state.SelectedReleaseSHA == command.ExpectedSelectedReleaseSHA256 &&
			state.SelectedBundleSHA == command.ExpectedSelectedBundleSHA256
	}
	if err != nil || state.ServiceActive || !state.AdmissionsClosed || !selectorMatches ||
		!equalStrings(state.RetainedLostPairs, command.LostPairs) {
		return errors.New("fixture recovery pair set mismatch")
	}
	var preflight laneinstall.CatalogCohortHostPreflightReport
	if err := readCatalogFixtureJSON(nil, seed.PreflightPaths[command.HostID], &preflight); err != nil ||
		preflight.Preflight.BackupDirectory != command.BackupDirectory ||
		preflight.Preflight.BackupManifestSHA256 != command.ExpectedBackupManifestSHA {
		return errors.New("fixture recovery backup identity mismatch")
	}
	databaseBytes, readErr := os.ReadFile(state.DatabasePath)
	databaseHash := sha256.Sum256(databaseBytes)
	if readErr != nil || !catalogFixtureDatabaseIsValid(state.DatabasePath) || hex.EncodeToString(databaseHash[:]) != state.DatabaseSHA256 {
		return errors.New("fixture recovery database invalid")
	}
	state.RetainedLostPairs = nil
	state.RetainedHistory = append(state.RetainedHistory, "recovery-release-proof")
	if err := b.writeHost(state); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{
		"host_id": command.HostID, "release_id": command.ReleaseID,
		"release_sha256": command.ExpectedReleaseSHA256, "bundle_sha256": command.ExpectedBundleSHA256,
		"recovered_lost_pairs": len(command.LostPairs), "service_remains_stopped": true,
		"selector_remains_unselected": command.ExpectedSelectedReleaseID == "",
		"selector_remains_unchanged":  true, "replay_count": state.ReplayCount,
	})
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (b *catalogSubprocessFixtureBackend) RunCohort(ctx context.Context, command catalogCohortCommand, stdout, stderr io.Writer) error {
	if command.Lane != "prd" || !containsCatalogFixtureHost(command.HostID) {
		return errors.New("fixture backend accepts only canonical PRD fixture hosts")
	}
	if ctx == nil {
		return errors.New("missing command context")
	}
	switch command.Action {
	case "create":
		if command.HostID != "mac-control-plane" || command.OperationID != "" || command.SuccessorOperation != "" ||
			command.ReleaseManifest == "" || len(command.PreflightFiles) != len(catalogFixtureSwitchOrder) ||
			command.PublisherOwner == "" || command.PublisherControl == "" || !command.PublishersPaused || !command.NoNewAccepted {
			return laneinstall.ErrCatalogOperationInvalid
		}
		manifest, err := laneinstall.LoadCatalogReleaseManifest(command.ReleaseManifest)
		if err != nil {
			return err
		}
		reports := make([]laneinstall.CatalogCohortHostPreflightReport, 0, len(command.PreflightFiles))
		for _, path := range command.PreflightFiles {
			report, loadErr := laneinstall.LoadCatalogCohortHostPreflightReportFile(path)
			if loadErr != nil {
				return loadErr
			}
			reports = append(reports, report)
		}
		receipt, err := laneinstall.NewCatalogCohortReceipt(config.ReleaseLanePRD, manifest, reports,
			laneinstall.CatalogCohortPublisherPause{Owner: command.PublisherOwner, Control: command.PublisherControl,
				Paused: command.PublishersPaused, NoNewAccepted: command.NoNewAccepted}, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := b.writeReceipt("coordinator", receipt); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"operation_id": receipt.OperationID, "stage": string(receipt.Stage)})
	case "prepare", "sync":
		if command.ReceiptFile == "" || command.OperationID != "" || command.SuccessorOperation != "" {
			return laneinstall.ErrCatalogOperationInvalid
		}
		receipt, err := laneinstall.LoadCatalogCohortReceiptFile(command.ReceiptFile)
		if err != nil || receipt.Lane != config.ReleaseLanePRD {
			return laneinstall.ErrCatalogOperationInvalid
		}
		if command.Action == "prepare" && receipt.Stage != laneinstall.CatalogCohortPrepared {
			return laneinstall.ErrCatalogOperationInvalid
		}
		if err := b.writeReceipt(command.HostID, receipt); err != nil {
			return err
		}
		if command.Action == "sync" && command.HostID == "mac-control-plane" {
			if err := b.writeReceipt("coordinator", receipt); err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]string{"operation_id": receipt.OperationID, "stage": string(receipt.Stage)})
	default:
		if command.OperationID == "" || command.ReceiptFile != "" || command.ReleaseManifest != "" || len(command.PreflightFiles) != 0 ||
			len(command.HistoryFiles) != 0 || command.PublisherOwner != "" || command.PublisherControl != "" || command.PublishersPaused || command.NoNewAccepted ||
			(command.ReplacementPredecessor != "" && command.Action != "fence-host") {
			return laneinstall.ErrCatalogOperationInvalid
		}
		receipt, err := b.readReceipt(command.HostID, command.OperationID)
		if err != nil {
			return err
		}
		if command.SubjectHost != "" && command.SubjectHost != command.HostID {
			return laneinstall.ErrCatalogOperationInvalid
		}
		if command.EvidenceSHA256 != "" && !fixtureValidDigest(command.EvidenceSHA256) {
			return laneinstall.ErrCatalogOperationInvalid
		}
		switch command.Action {
		case "status":
			return writeJSON(stdout, receipt)
		case "fence-host":
			if command.SubjectHost != command.HostID || receipt.Stage != laneinstall.CatalogCohortPrepared || command.EvidenceSHA256 != "" {
				return laneinstall.ErrCatalogOperationInvalid
			}
			host, err := b.readHost(command.HostID)
			preflight := fixtureHostPreflight(receipt, command.HostID)
			if err != nil || host.ServiceActive != preflight.ServiceActiveAtPreflight || !catalogFixtureDatabaseIsValid(host.DatabasePath) {
				return laneinstall.ErrCatalogOperationInvalid
			}
			if command.ReplacementPredecessor != "" {
				predecessor, predecessorErr := b.readReceipt(command.HostID, command.ReplacementPredecessor)
				oldHost := fixtureHostPreflight(predecessor, command.HostID)
				if predecessorErr != nil || predecessor.Stage != laneinstall.CatalogCohortVerifying ||
					host.ServiceActive || !host.AdmissionsClosed || host.SelectorTarget != preflight.PreviousTarget ||
					preflight.PreviousBundleSHA256 != oldHost.Target.BundleSHA256 ||
					preflight.ConfigurationSHA256 != oldHost.ConfigurationSHA256 || preflight.SchemaVersion != oldHost.SchemaVersion ||
					preflight.WorkInventorySHA256 != oldHost.WorkInventorySHA256 || !preflight.WorkInventoryEmpty ||
					!catalogFixtureDatabaseIsValid(host.DatabasePath) || !fixtureFileSHA256Matches(host.DatabasePath, host.DatabaseSHA256) {
					return laneinstall.ErrCatalogOperationInvalid
				}
			}
			host.ServiceActive = false
			if err := b.writeHost(host); err != nil {
				return err
			}
			fence := laneinstall.CatalogCohortHostFence{HostID: command.HostID, ServiceWasActive: preflight.ServiceActiveAtPreflight, ServiceStopped: true,
				WorkInventorySHA256: preflight.WorkInventorySHA256, ConfigurationSHA256: host.ConfigurationSHA,
				SelectorTarget: preflight.PreviousTarget, PreviousBundleSHA256: preflight.PreviousBundleSHA256,
				DatabaseSchema: host.SchemaVersion, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			receipt, err = laneinstall.AdvanceCatalogCohortHostFence(receipt, fence, time.Now().UTC())
		case "supersede-host":
			if command.SubjectHost != command.HostID || command.SuccessorOperation == "" || command.EvidenceSHA256 != "" {
				return laneinstall.ErrCatalogOperationInvalid
			}
			successor, successorErr := b.readReceipt(command.HostID, command.SuccessorOperation)
			if successorErr != nil || successor.Stage != laneinstall.CatalogCohortHeld {
				return laneinstall.ErrCatalogOperationInvalid
			}
			index := -1
			for candidateIndex, candidate := range successor.Hosts {
				if candidate.HostID == command.HostID {
					index = candidateIndex
					break
				}
			}
			if index < 0 || index != len(currentSupersededHostsForFixture(receipt.Transitions)) {
				return laneinstall.ErrCatalogOperationInvalid
			}
			state, stateErr := b.readHost(command.HostID)
			oldHost := fixtureHostPreflight(receipt, command.HostID)
			newHost := successor.Hosts[index]
			journal, journalErr := b.readJournal(command.HostID, receipt.OperationID,
				fixtureOperationID(receipt.OperationID, command.HostID, false))
			if stateErr != nil || journalErr != nil || receipt.Stage != laneinstall.CatalogCohortVerifying ||
				state.ServiceActive || !state.AdmissionsClosed || state.SelectorTarget != newHost.PreviousTarget ||
				state.ConfigurationSHA != newHost.ConfigurationSHA256 || state.SchemaVersion != newHost.SchemaVersion ||
				newHost.PreviousBundleSHA256 != oldHost.Target.BundleSHA256 ||
				newHost.WorkInventorySHA256 != oldHost.WorkInventorySHA256 || !newHost.WorkInventoryEmpty ||
				journal.Stage != "complete" || journal.Rollback || journal.NextTarget != state.SelectorTarget ||
				!catalogFixtureDatabaseIsValid(state.DatabasePath) || !fixtureFileSHA256Matches(state.DatabasePath, state.DatabaseSHA256) ||
				fixtureHostDatabaseSentinel(ctx, state.DatabasePath) != "retained" {
				return laneinstall.ErrCatalogOperationInvalid
			}
			observation := laneinstall.CatalogCohortSupersessionEvidence{
				HostID: command.HostID, SuccessorOperationID: successor.OperationID,
				ExpectedSelector: newHost.PreviousTarget, ObservedSelector: state.SelectorTarget,
				SelectorBundleSHA256: oldHost.Target.BundleSHA256, ServiceActive: state.ServiceActive,
				WorkInventorySHA256: newHost.WorkInventorySHA256, WorkInventoryEmpty: newHost.WorkInventoryEmpty,
				ConfigurationSHA256: state.ConfigurationSHA, DatabaseSchema: state.SchemaVersion, SelectorOperations: "completed",
			}
			receipt, err = laneinstall.AdvanceCatalogCohortSupersedeHost(receipt, successor, observation, time.Now().UTC())
		case "supersede":
			if command.SuccessorOperation == "" || command.SubjectHost != "" || command.EvidenceSHA256 != "" {
				return laneinstall.ErrCatalogOperationInvalid
			}
			successor, successorErr := b.readReceipt("coordinator", command.SuccessorOperation)
			if successorErr != nil || successor.Stage != laneinstall.CatalogCohortHeld {
				return laneinstall.ErrCatalogOperationInvalid
			}
			receipt, err = laneinstall.AdvanceCatalogCohortSupersede(receipt, successor.OperationID, time.Now().UTC())
		case "hold", "preflight", "verify-start", "verify", "complete", "block", "rollback-start", "rollback-complete", "open":
			if command.Action == "preflight" {
				if command.SubjectHost != command.HostID || command.EvidenceSHA256 != "" {
					return laneinstall.ErrCatalogOperationInvalid
				}
			} else if command.Action == "verify" {
				if command.SubjectHost != command.HostID || !fixtureValidDigest(command.EvidenceSHA256) {
					return laneinstall.ErrCatalogOperationInvalid
				}
			} else if command.SubjectHost != "" || !fixtureValidDigest(command.EvidenceSHA256) {
				return laneinstall.ErrCatalogOperationInvalid
			}
			if command.Action == "preflight" {
				host, hostErr := b.readHost(command.HostID)
				if hostErr != nil {
					return fmt.Errorf("fixture preflight could not read host state: %w", hostErr)
				}
				expected := fixtureHostPreflight(receipt, command.HostID)
				if host.ServiceActive || host.SelectorTarget != expected.PreviousTarget ||
					host.ConfigurationSHA != expected.ConfigurationSHA256 || !catalogFixtureDatabaseIsValid(host.DatabasePath) {
					return fmt.Errorf("fixture preflight state mismatch for %s: active=%v selector=%q expected=%q config=%t db=%t",
						command.HostID, host.ServiceActive, host.SelectorTarget, expected.PreviousTarget,
						host.ConfigurationSHA == expected.ConfigurationSHA256, catalogFixtureDatabaseIsValid(host.DatabasePath))
				}
			}
			if command.Action == "verify-start" {
				for _, hostID := range catalogFixtureSwitchOrder {
					host, hostErr := b.readHost(hostID)
					if hostErr != nil || host.SelectorTarget != "releases/"+fixtureHostPreflight(receipt, hostID).Target.BundleSHA256 {
						return laneinstall.ErrCatalogOperationInvalid
					}
					host.ServiceActive = true
					if err := b.writeHost(host); err != nil {
						return err
					}
				}
			}
			if command.Action == "verify" {
				host, hostErr := b.readHost(command.HostID)
				if hostErr != nil {
					return fmt.Errorf("fixture verify could not read host state: %w", hostErr)
				}
				if !host.ServiceActive || fixtureHostDatabaseSentinel(ctx, host.DatabasePath) != "retained" {
					return fmt.Errorf("fixture verify state mismatch for %s: active=%v sentinel=%q", command.HostID,
						host.ServiceActive, fixtureHostDatabaseSentinel(ctx, host.DatabasePath))
				}
			}
			if command.Action == "open" {
				if receipt.Stage != laneinstall.CatalogCohortComplete && receipt.Stage != laneinstall.CatalogCohortRolledBack {
					return laneinstall.ErrCatalogOperationInvalid
				}
				for _, fence := range receipt.FenceProofs {
					if !fence.Paused || !fence.NoNewAccepted || fence.Owner == "" || fence.Control == "" {
						return laneinstall.ErrCatalogOperationInvalid
					}
				}
				for _, hostID := range catalogFixtureSwitchOrder {
					host, hostErr := b.readHost(hostID)
					preflight := fixtureHostPreflight(receipt, hostID)
					if hostErr != nil || host.SelectorTarget != "releases/"+preflight.Target.BundleSHA256 ||
						host.ConfigurationSHA != preflight.ConfigurationSHA256 || host.SchemaVersion != preflight.SchemaVersion ||
						!preflight.WorkInventoryEmpty || !catalogFixtureDatabaseIsValid(host.DatabasePath) ||
						!fixtureFileSHA256Matches(host.DatabasePath, host.DatabaseSHA256) {
						return fmt.Errorf("fixture release precondition failed for %s", hostID)
					}
				}
			}
			evidence := command.EvidenceSHA256
			if command.Action == "preflight" {
				evidence = fixtureDigest("preflight " + command.HostID + " " + receipt.OperationID + " " + fixtureHostPreflight(receipt, command.HostID).WorkInventorySHA256)
			}
			receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, command.Action, command.SubjectHost, evidence, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("advance fixture cohort %s: %w", command.Action, err)
			}
		default:
			return laneinstall.ErrCatalogOperationInvalid
		}
		if err != nil {
			return err
		}
		if err := b.writeReceipt(command.HostID, receipt); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"operation_id": receipt.OperationID, "stage": string(receipt.Stage)})
	}
}

func (b *catalogSubprocessFixtureBackend) RunSelector(ctx context.Context, action string, command catalogSelectorCommand, stdout, stderr io.Writer) error {
	if ctx == nil || command.Lane != "prd" || !containsCatalogFixtureHost(command.HostID) || command.CohortOperationID == "" {
		return laneinstall.ErrCatalogOperationInvalid
	}
	receipt, err := b.readReceipt(command.HostID, command.CohortOperationID)
	if err != nil {
		return err
	}
	host := fixtureHostPreflight(receipt, command.HostID)
	state, err := b.readHost(command.HostID)
	if err != nil {
		return err
	}
	switch action {
	case "catalog-select":
		if command.ReleaseID != receipt.ReleaseID || command.ExpectedTarget != host.PreviousTarget || !fixtureValidDigest(command.EvidenceSHA256) ||
			state.SelectorTarget != host.PreviousTarget || state.ServiceActive || !fixtureHostPreflightRecorded(receipt, command.HostID) {
			return laneinstall.ErrCatalogOperationInvalid
		}
		journal := fixtureSelectorJournal{OperationID: fixtureOperationID(command.CohortOperationID, command.HostID, false),
			CohortOperationID: command.CohortOperationID, HostID: command.HostID, PreviousTarget: host.PreviousTarget,
			NextTarget: "releases/" + host.Target.BundleSHA256, Stage: "complete", EvidenceSHA256: command.EvidenceSHA256,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		state.SelectorTarget = journal.NextTarget
		if err := b.writeHost(state); err != nil {
			return err
		}
		if err := b.writeJournal(journal); err != nil {
			return err
		}
		interruptPath := filepath.Join(b.root, "interrupt-sandbox-once")
		if command.HostID == "sandbox-host" && fileExists(interruptPath) {
			marker, readErr := os.ReadFile(interruptPath)
			if readErr != nil {
				return readErr
			}
			if strings.TrimSpace(string(marker)) == receipt.ReleaseID {
				if err := os.Remove(interruptPath); err != nil {
					return err
				}
				return errors.New("simulated crash after selector journal")
			}
		}
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "switch", command.HostID, fixtureJournalDigest(journal), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := b.writeReceipt(command.HostID, receipt); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"operation_id": journal.OperationID, "stage": journal.Stage})
	case "catalog-reconcile":
		journal, err := b.readJournal(command.HostID, command.CohortOperationID, command.SelectorOperation)
		if err != nil || journal.Stage != "complete" || state.SelectorTarget != journal.NextTarget || host.PreviousTarget != journal.PreviousTarget ||
			fixtureHostPreflightRecorded(receipt, command.HostID) == false || receipt.Stage != laneinstall.CatalogCohortHeld && receipt.Stage != laneinstall.CatalogCohortSwitching {
			return laneinstall.ErrCatalogOperationInvalid
		}
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "switch-reconciled", command.HostID, fixtureJournalDigest(journal), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := b.writeReceipt(command.HostID, receipt); err != nil {
			return err
		}
		return writeJSON(stdout, journal)
	case "catalog-rollback":
		journal, err := b.readJournal(command.HostID, command.CohortOperationID, command.SelectorOperation)
		if err != nil || journal.Stage != "complete" || receipt.Stage != laneinstall.CatalogCohortRollingBack || state.SelectorTarget != journal.NextTarget {
			return laneinstall.ErrCatalogOperationInvalid
		}
		switched := fixtureSwitchedHosts(receipt)
		rolledBack := fixtureRolledBackHosts(receipt)
		if len(rolledBack) >= len(switched) || switched[len(switched)-1-len(rolledBack)] != command.HostID {
			return laneinstall.ErrCatalogOperationInvalid
		}
		rollback := fixtureSelectorJournal{OperationID: fixtureOperationID(command.CohortOperationID, command.HostID, true),
			CohortOperationID: command.CohortOperationID, HostID: command.HostID, PreviousTarget: journal.NextTarget,
			NextTarget: journal.PreviousTarget, Stage: "complete", Rollback: true,
			EvidenceSHA256: journal.EvidenceSHA256, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		state.SelectorTarget = rollback.NextTarget
		if err := b.writeHost(state); err != nil {
			return err
		}
		if err := b.writeJournal(rollback); err != nil {
			return err
		}
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "rollback-host", command.HostID, fixtureJournalDigest(rollback), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := b.writeReceipt(command.HostID, receipt); err != nil {
			return err
		}
		return writeJSON(stdout, rollback)
	default:
		return laneinstall.ErrCatalogOperationInvalid
	}
}

func prepareCatalogSubprocessFixture(t *testing.T, root, releaseID, bundleLabel string, previous *catalogSubprocessFixtureSeed) catalogSubprocessFixtureSeed {
	t.Helper()
	for _, directory := range []string{"inputs", "coordinator", "seeds"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bundles := make(map[string]laneinstall.SimpleBundle, len(catalogFixtureSwitchOrder))
	for _, hostID := range catalogFixtureSwitchOrder {
		componentNames := []string{"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"}
		if hostID == "mac-control-plane" {
			componentNames = []string{"runner", "runner-local", "runner-locald", "runner-log-capture"}
		}
		components := make(map[string][]byte, len(componentNames))
		for _, name := range componentNames {
			components[name] = []byte(bundleLabel + ":" + hostID + ":" + name)
		}
		bundle, err := laneinstall.NewSimpleBundle(hostID, "0123456789abcdef0123456789abcdef01234567", components)
		if err != nil {
			t.Fatal(err)
		}
		bundles[hostID] = bundle
	}
	manifest, err := laneinstall.NewCatalogReleaseManifest(releaseID, bundles)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := laneinstall.CatalogReleaseManifestBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "inputs", releaseID+"-release.json")
	writeCatalogFixtureCanonical(t, manifestPath, manifestBytes)
	manifestSHA := sha256.Sum256(manifestBytes)
	manifestDigest := hex.EncodeToString(manifestSHA[:])
	seed := catalogSubprocessFixtureSeed{ReleaseID: releaseID, ReleaseSHA: manifestDigest, ManifestPath: manifestPath,
		PreflightPaths: make(map[string]string, len(catalogFixtureSwitchOrder)), TargetBundleSHA: make(map[string]string, len(catalogFixtureSwitchOrder))}

	for _, hostID := range catalogFixtureSwitchOrder {
		hostRoot := filepath.Join(root, "hosts", hostID)
		for _, directory := range []string{"receipts", "journals", "backups"} {
			if err := os.MkdirAll(filepath.Join(hostRoot, directory), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		configPath := filepath.Join(hostRoot, "config", "prd.json")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if !fileExists(configPath) {
			if err := os.WriteFile(configPath, []byte("{\"lane\":\"prd\",\"host\":\""+hostID+"\",\"ingress\":\"disabled\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		configSHA := fileSHA256ForTest(t, configPath)
		databasePath := filepath.Join(hostRoot, "db", "local.db")
		if !fileExists(databasePath) {
			if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
				t.Fatal(err)
			}
			db, openErr := store.Open(context.Background(), databasePath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			if _, execErr := db.Exec(`CREATE TABLE catalog_harness_sentinel (value TEXT NOT NULL); INSERT INTO catalog_harness_sentinel(value) VALUES ('retained')`); execErr != nil {
				_ = db.Close()
				t.Fatal(execErr)
			}
			if closeErr := db.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		databaseSHA := fileSHA256ForTest(t, databasePath)
		statePath := filepath.Join(hostRoot, "host-state.json")
		var state catalogSubprocessFixtureHost
		if fileExists(statePath) {
			readCatalogFixtureJSON(t, statePath, &state)
		} else {
			legacy := fixtureDigest("legacy-bundle-" + hostID)
			state = catalogSubprocessFixtureHost{HostID: hostID, SelectorTarget: "legacy-bin:" + legacy,
				LegacyBundleSHA256: legacy, ServiceActive: true, ConfigurationPath: configPath, ConfigurationSHA: configSHA,
				DatabasePath: databasePath, DatabaseSHA256: databaseSHA, SchemaVersion: store.CurrentSchemaVersion}
		}
		if state.ConfigurationSHA != configSHA || state.DatabaseSHA256 != databaseSHA {
			t.Fatalf("fixture host %s state drifted while constructing release", hostID)
		}
		hostBundleSHA := ""
		for _, host := range manifest.Hosts {
			if host.HostID == hostID {
				hostBundleSHA = host.BundleSHA256
				break
			}
		}
		seed.TargetBundleSHA[hostID] = hostBundleSHA
		backupDirectory := filepath.Join(hostRoot, "backups", releaseID)
		if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		backupDB := filepath.Join(backupDirectory, "local.db")
		if err := copyCatalogFixtureFile(databasePath, backupDB); err != nil {
			t.Fatal(err)
		}
		backupManifest := catalogSubprocessFixtureBackup{HostID: hostID, DatabaseSHA256: fileSHA256ForTest(t, backupDB),
			ConfigurationSHA: configSHA, SchemaVersion: store.CurrentSchemaVersion}
		backupBytes, err := json.MarshalIndent(backupManifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		backupBytes = append(backupBytes, '\n')
		backupManifestPath := filepath.Join(backupDirectory, "manifest.json")
		writeCatalogFixtureCanonical(t, backupManifestPath, backupBytes)
		backupSHA := fileSHA256ForTest(t, backupManifestPath)

		previousTarget := state.SelectorTarget
		previousBundle := state.LegacyBundleSHA256
		previousReadMax := store.CurrentSchemaVersion
		var legacy *laneinstall.CatalogLegacyRollbackCompatibility
		if strings.HasPrefix(previousTarget, "releases/") {
			previousBundle = strings.TrimPrefix(previousTarget, "releases/")
		} else {
			legacy = &laneinstall.CatalogLegacyRollbackCompatibility{
				FormatVersion: 1, LegacyBundleSHA256: previousBundle, ConfigurationSHA256: configSHA,
				DatabaseSchema: store.CurrentSchemaVersion, ReadSchemaMin: 1, ReadSchemaMax: store.CurrentSchemaVersion,
				TestSuite: "catalog-prd-legacy-semantics-v1", TestEvidenceSHA256: fixtureDigest("legacy semantic fixture " + hostID),
				SemanticChecks: []string{"current-database-read-without-migration", "retained-history-unchanged", "launcher-resolves-preserved-legacy-binaries", "queued-wrapper-resolves-preserved-legacy-binaries", "rollback-preserves-current-database"},
				TestedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			}
		}
		inventory := laneinstall.CatalogWorkInventory{}
		preflight := laneinstall.CatalogCohortHostPreflightReport{Preflight: laneinstall.CatalogCohortHostPreflight{
			HostID: hostID, ServiceActiveAtPreflight: state.ServiceActive, PreviousTarget: previousTarget,
			PreviousBundleSHA256: previousBundle, PreviousReadSchemaMax: previousReadMax, LegacyRollback: legacy,
			Target: laneinstall.CatalogReleaseIdentity{ReleaseID: releaseID, ReleaseSHA256: manifestDigest,
				HostID: hostID, BundleSHA256: hostBundleSHA}, ConfigurationSHA256: configSHA,
			SchemaVersion: store.CurrentSchemaVersion, WorkInventorySHA256: inventory.SHA256(), WorkInventoryEmpty: true,
			PreflightAt: time.Now().UTC().Format(time.RFC3339Nano), BackupDirectory: backupDirectory,
			BackupManifestSHA256: backupSHA, AvailableBytes: 4 << 30, RequiredBytes: laneinstall.PRDCatalogSelectorHeadroomBytes(),
		}, WorkInventory: inventory}
		preflightBytes, err := json.MarshalIndent(preflight, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		preflightBytes = append(preflightBytes, '\n')
		preflightPath := filepath.Join(root, "inputs", releaseID+"-"+hostID+"-preflight.json")
		writeCatalogFixtureCanonical(t, preflightPath, preflightBytes)
		seed.PreflightPaths[hostID] = preflightPath
		if err := writeCatalogFixtureJSON(statePath, state); err != nil {
			t.Fatal(err)
		}
	}
	if previous != nil {
		seed.OperationID = previous.OperationID
	}
	seedPath := filepath.Join(root, "seeds", releaseID+".json")
	if err := writeCatalogFixtureJSON(seedPath, seed); err != nil {
		t.Fatal(err)
	}
	return seed
}

func refreshCatalogFixturePreflights(t *testing.T, root string, seed catalogSubprocessFixtureSeed) {
	t.Helper()
	manifest, err := laneinstall.LoadCatalogReleaseManifest(seed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := laneinstall.CatalogReleaseManifestBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(manifestHash[:])
	for _, hostID := range catalogFixtureSwitchOrder {
		state := readCatalogFixtureHost(t, root, hostID)
		path := seed.PreflightPaths[hostID]
		var report laneinstall.CatalogCohortHostPreflightReport
		readCatalogFixtureJSON(t, path, &report)
		backupDirectory := report.Preflight.BackupDirectory
		backupDB := filepath.Join(backupDirectory, "local.db")
		if got, want := fileSHA256ForTest(t, backupDB), state.DatabaseSHA256; got != want {
			t.Fatalf("fixture backup database for %s changed: %s != %s", hostID, got, want)
		}
		backupManifest := catalogSubprocessFixtureBackup{HostID: hostID, DatabaseSHA256: state.DatabaseSHA256,
			ConfigurationSHA: state.ConfigurationSHA, SchemaVersion: state.SchemaVersion}
		backupManifestBytes, err := json.MarshalIndent(backupManifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		backupManifestBytes = append(backupManifestBytes, '\n')
		backupManifestPath := filepath.Join(backupDirectory, "manifest.json")
		writeCatalogFixtureCanonical(t, backupManifestPath, backupManifestBytes)
		inventory := laneinstall.CatalogWorkInventory{}
		previousTarget := state.SelectorTarget
		previousBundle := state.LegacyBundleSHA256
		legacy := (*laneinstall.CatalogLegacyRollbackCompatibility)(nil)
		if strings.HasPrefix(previousTarget, "releases/") {
			previousBundle = strings.TrimPrefix(previousTarget, "releases/")
		} else {
			legacy = &laneinstall.CatalogLegacyRollbackCompatibility{
				FormatVersion: 1, LegacyBundleSHA256: previousBundle, ConfigurationSHA256: state.ConfigurationSHA,
				DatabaseSchema: state.SchemaVersion, ReadSchemaMin: 1, ReadSchemaMax: state.SchemaVersion,
				TestSuite: "catalog-prd-legacy-semantics-v1", TestEvidenceSHA256: fixtureDigest("legacy semantic fixture " + hostID),
				SemanticChecks: []string{"current-database-read-without-migration", "retained-history-unchanged", "launcher-resolves-preserved-legacy-binaries", "queued-wrapper-resolves-preserved-legacy-binaries", "rollback-preserves-current-database"},
				TestedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			}
		}
		report.Preflight.HostID = hostID
		report.Preflight.ServiceActiveAtPreflight = state.ServiceActive
		report.Preflight.PreviousTarget = previousTarget
		report.Preflight.PreviousBundleSHA256 = previousBundle
		report.Preflight.PreviousReadSchemaMax = state.SchemaVersion
		report.Preflight.LegacyRollback = legacy
		report.Preflight.Target.ReleaseID = manifest.ReleaseID
		report.Preflight.Target.ReleaseSHA256 = manifestSHA
		report.Preflight.Target.HostID = hostID
		report.Preflight.Target.BundleSHA256 = seed.TargetBundleSHA[hostID]
		report.Preflight.ConfigurationSHA256 = state.ConfigurationSHA
		report.Preflight.SchemaVersion = state.SchemaVersion
		report.Preflight.WorkInventorySHA256 = inventory.SHA256()
		report.Preflight.WorkInventoryEmpty = true
		report.Preflight.PreflightAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Preflight.BackupManifestSHA256 = fileSHA256ForTest(t, backupManifestPath)
		report.Preflight.AvailableBytes = 4 << 30
		report.Preflight.RequiredBytes = laneinstall.PRDCatalogSelectorHeadroomBytes()
		report.WorkInventory = inventory
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeCatalogFixtureCanonical(t, path, append(data, '\n'))
	}
}

func (b *catalogSubprocessFixtureBackend) writeReceipt(hostID string, receipt laneinstall.CatalogCohortReceipt) error {
	if err := laneinstall.ValidateCatalogCohortReceipt(receipt); err != nil {
		return err
	}
	directory := filepath.Join(b.root, hostID, "receipts")
	if hostID == "coordinator" {
		directory = filepath.Join(b.root, "coordinator")
	} else {
		directory = filepath.Join(b.root, "hosts", hostID, "receipts")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeCatalogFixtureFile(filepath.Join(directory, receipt.OperationID+".json"), append(data, '\n'))
}

func (b *catalogSubprocessFixtureBackend) readReceipt(hostID, operationID string) (laneinstall.CatalogCohortReceipt, error) {
	path := filepath.Join(b.root, "hosts", hostID, "receipts", operationID+".json")
	if hostID == "coordinator" {
		path = filepath.Join(b.root, "coordinator", operationID+".json")
	}
	var receipt laneinstall.CatalogCohortReceipt
	if err := readCatalogFixtureJSON(nil, path, &receipt); err != nil {
		return laneinstall.CatalogCohortReceipt{}, err
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(receipt); err != nil {
		return laneinstall.CatalogCohortReceipt{}, err
	}
	return receipt, nil
}

func (b *catalogSubprocessFixtureBackend) readHost(hostID string) (catalogSubprocessFixtureHost, error) {
	var host catalogSubprocessFixtureHost
	err := readCatalogFixtureJSON(nil, filepath.Join(b.root, "hosts", hostID, "host-state.json"), &host)
	return host, err
}

func (b *catalogSubprocessFixtureBackend) writeHost(host catalogSubprocessFixtureHost) error {
	return writeCatalogFixtureJSON(filepath.Join(b.root, "hosts", host.HostID, "host-state.json"), host)
}

func (b *catalogSubprocessFixtureBackend) writeJournal(journal fixtureSelectorJournal) error {
	return writeCatalogFixtureJSON(filepath.Join(b.root, "hosts", journal.HostID, "journals", journal.CohortOperationID+"-"+journal.HostID+func() string {
		if journal.Rollback {
			return "-rollback"
		}
		return ""
	}()+".json"), journal)
}

func (b *catalogSubprocessFixtureBackend) readJournal(hostID, cohortOperationID, selectorOperationID string) (fixtureSelectorJournal, error) {
	var journal fixtureSelectorJournal
	path := filepath.Join(b.root, "hosts", hostID, "journals", cohortOperationID+"-"+hostID+".json")
	if strings.HasSuffix(selectorOperationID, "-rollback") {
		path = filepath.Join(b.root, "hosts", hostID, "journals", cohortOperationID+"-"+hostID+"-rollback.json")
	}
	if err := readCatalogFixtureJSON(nil, path, &journal); err != nil || journal.OperationID != selectorOperationID || journal.CohortOperationID != cohortOperationID || journal.HostID != hostID {
		return fixtureSelectorJournal{}, laneinstall.ErrCatalogOperationInvalid
	}
	return journal, nil
}

func fixtureHostPreflight(receipt laneinstall.CatalogCohortReceipt, hostID string) laneinstall.CatalogCohortHostPreflight {
	for _, host := range receipt.Hosts {
		if host.HostID == hostID {
			return host
		}
	}
	return laneinstall.CatalogCohortHostPreflight{}
}

func fixtureHostPreflightRecorded(receipt laneinstall.CatalogCohortReceipt, hostID string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "preflight" && transition.HostID == hostID {
			return true
		}
	}
	return false
}

func fixtureSwitchedHosts(receipt laneinstall.CatalogCohortReceipt) []string {
	var result []string
	for _, transition := range receipt.Transitions {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			result = append(result, transition.HostID)
		}
	}
	return result
}

func fixtureRolledBackHosts(receipt laneinstall.CatalogCohortReceipt) []string {
	var result []string
	for _, transition := range receipt.Transitions {
		if transition.Action == "rollback-host" {
			result = append(result, transition.HostID)
		}
	}
	return result
}

func currentSupersededHostsForFixture(transitions []laneinstall.CatalogCohortTransition) []string {
	start := 0
	for index, transition := range transitions {
		if transition.Action == "supersession-restart" {
			start = index + 1
		}
	}
	var hosts []string
	for _, transition := range transitions[start:] {
		if transition.Action == "supersede-host" {
			hosts = append(hosts, transition.HostID)
		}
	}
	return hosts
}

func fixtureJournalDigest(journal fixtureSelectorJournal) string {
	data, _ := json.Marshal(journal)
	return fixtureDigest(string(data))
}

func fixtureOperationID(cohortOperationID, hostID string, rollback bool) string {
	return fixtureDigest(cohortOperationID + "\x00" + hostID + fmt.Sprint(rollback))[:32]
}

func fixtureOutputOperationID(t *testing.T, output string) string {
	t.Helper()
	var result struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.OperationID == "" {
		t.Fatalf("decode public command operation identity from %q: %v", output, err)
	}
	return result.OperationID
}

func readCatalogFixtureSeed(t *testing.T, path string) catalogSubprocessFixtureSeed {
	t.Helper()
	var seed catalogSubprocessFixtureSeed
	readCatalogFixtureJSON(t, path, &seed)
	return seed
}

func readCatalogFixtureReceipt(t *testing.T, path string) laneinstall.CatalogCohortReceipt {
	t.Helper()
	var receipt laneinstall.CatalogCohortReceipt
	readCatalogFixtureJSON(t, path, &receipt)
	return receipt
}

func readCatalogFixtureHost(t *testing.T, root, hostID string) catalogSubprocessFixtureHost {
	t.Helper()
	var host catalogSubprocessFixtureHost
	readCatalogFixtureJSON(t, filepath.Join(root, "hosts", hostID, "host-state.json"), &host)
	return host
}

func readCatalogFixtureJSON(t *testing.T, path string, target any) error {
	if t != nil {
		t.Helper()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if t != nil {
			t.Fatal(err)
		}
		return err
	}
	return nil
}

func writeCatalogFixtureJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeCatalogFixtureFile(path, append(data, '\n'))
}

func writeCatalogFixtureCanonical(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := writeCatalogFixtureFile(path, data); err != nil {
		t.Fatal(err)
	}
}

func writeCatalogFixtureFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyCatalogFixtureFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeCatalogFixtureFile(destination, data)
}

func fileSHA256ForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func fixtureFileSHA256Matches(path, expected string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) == expected
}

func fixtureDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func fixtureValidDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func containsCatalogFixtureHost(hostID string) bool {
	for _, candidate := range catalogFixtureSwitchOrder {
		if candidate == hostID {
			return true
		}
	}
	return false
}

func fixtureHostDatabaseSentinel(ctx context.Context, path string) string {
	db, err := store.Open(ctx, path)
	if err != nil {
		return ""
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `SELECT value FROM catalog_harness_sentinel LIMIT 1`).Scan(&result); err != nil {
		return ""
	}
	return result
}

func catalogFixtureDatabaseSentinel(t *testing.T, path string) string {
	t.Helper()
	result := fixtureHostDatabaseSentinel(context.Background(), path)
	if result == "" {
		t.Fatalf("fixture database sentinel unavailable at %s", path)
	}
	return result
}

func catalogFixtureDatabaseIsValid(path string) bool {
	return fixtureHostDatabaseSentinel(context.Background(), path) == "retained"
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
