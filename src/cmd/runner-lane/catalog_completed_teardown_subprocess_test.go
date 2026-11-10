package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/laneinstall"
)

// This disposable public-command fixture checks CLI selection and history
// preservation. It does not substitute for the sealed executor's process proof.
func TestCatalogCompletedTeardownPublicCommandSubprocess(t *testing.T) {
	root := t.TempDir()
	prepareCatalogSubprocessFixture(t, root, "fixture-completed-teardown", "completed-teardown", nil)
	seed := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-completed-teardown.json"))
	state := readCatalogFixtureHost(t, root, "mac-control-plane")
	state.SelectedReleaseID = seed.ReleaseID
	state.SelectedReleaseSHA = seed.ReleaseSHA
	state.SelectedBundleSHA = seed.TargetBundleSHA[state.HostID]
	state.SelectorTarget = "catalog/" + state.SelectedReleaseID + "/" + state.HostID
	state.ServiceActive = false
	state.AdmissionsClosed = true
	state.RetainedTeardownPairs = []string{"sess-completed-one:cmd-completed-one"}
	state.TerminalCommandState = "failed"
	state.TerminalCommandExitCode = 2
	state.TerminalEventCursor = 10
	state.TeardownSessionHolds = 1
	state.RetainedHistory = []string{"failed-exit2", "events1-through10", "unacked-original-response"}
	if err := writeCatalogFixtureJSON(filepath.Join(root, "hosts", state.HostID, "host-state.json"), state); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCatalogCompletedTeardownPublicCommandSubprocessHelper$")
	command.Env = append(os.Environ(), catalogSubprocessFixtureRootEnv+"="+root)
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("CATALOG_COMPLETED_TEARDOWN_OK")) {
		t.Fatalf("completed-teardown public command failed: %v\n%s", err, output)
	}
	after := readCatalogFixtureHost(t, root, state.HostID)
	if after.SelectedReleaseID != state.SelectedReleaseID || after.SelectedReleaseSHA != state.SelectedReleaseSHA ||
		after.SelectedBundleSHA != state.SelectedBundleSHA || after.SelectorTarget != state.SelectorTarget ||
		after.ServiceActive || len(after.RetainedTeardownPairs) != 0 || after.TeardownSessionHolds != 0 ||
		after.TerminalCommandState != "failed" || after.TerminalCommandExitCode != 2 || after.TerminalEventCursor != 10 ||
		after.ReplayCount != 0 || strings.Join(after.RetainedHistory, ",") != "failed-exit2,events1-through10,unacked-original-response,recovery-release-proof" {
		t.Fatalf("completed teardown changed the selected release or truthful result: %+v", after)
	}
}

func TestCatalogCompletedTeardownPublicCommandSubprocessHelper(t *testing.T) {
	root := os.Getenv(catalogSubprocessFixtureRootEnv)
	if root == "" {
		return
	}
	catalogCommandSubprocessFixtureBackendForTests = &catalogSubprocessFixtureBackend{root: root}
	seed := readCatalogFixtureSeed(t, filepath.Join(root, "seeds", "fixture-completed-teardown.json"))
	state := readCatalogFixtureHost(t, root, "mac-control-plane")
	var preflight laneinstall.CatalogCohortHostPreflightReport
	readCatalogFixtureJSON(t, seed.PreflightPaths[state.HostID], &preflight)
	base := []string{"catalog-prd-recover", "--host", state.HostID, "--release-id", seed.ReleaseID,
		"--release-sha256", seed.ReleaseSHA, "--bundle-sha256", seed.TargetBundleSHA[state.HostID],
		"--expected-selected-release-id", state.SelectedReleaseID,
		"--expected-selected-release-sha256", state.SelectedReleaseSHA,
		"--expected-selected-bundle-sha256", state.SelectedBundleSHA,
		"--backup-dir", preflight.Preflight.BackupDirectory,
		"--backup-manifest-sha256", preflight.Preflight.BackupManifestSHA256}
	valid := append(append([]string(nil), base...), "--teardown-pair", state.RetainedTeardownPairs[0])
	replace := func(args []string, flag, value string) []string {
		out := append([]string(nil), args...)
		for i := range out {
			if out[i] == flag && i+1 < len(out) {
				out[i+1] = value
				return out
			}
		}
		t.Fatalf("missing fixture flag %s", flag)
		return nil
	}
	bad := map[string][]string{
		"mixed selection":   append(append([]string(nil), valid...), "--lost-pair", "sess-lost-one:cmd-lost-one"),
		"Linux selection":   replace(valid, "--host", "linux-host"),
		"wrong pair":        replace(valid, "--teardown-pair", "sess-other:cmd-other"),
		"wrong backup":      replace(valid, "--backup-manifest-sha256", fixtureDigest("other backup")),
		"wrong selector":    replace(valid, "--expected-selected-bundle-sha256", fixtureDigest("other selection")),
		"duplicate pair":    append(append([]string(nil), valid...), "--teardown-pair", state.RetainedTeardownPairs[0]),
		"missing selection": base,
	}
	statePath := filepath.Join(root, "hosts", state.HostID, "host-state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range bad {
		var out, stderr bytes.Buffer
		if status := run(args, &out, &stderr); status == 0 || out.Len() != 0 {
			t.Fatalf("%s was accepted: status=%d stdout=%q stderr=%q", name, status, out.String(), stderr.String())
		}
		after, err := os.ReadFile(statePath)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("%s changed fixture recovery state", name)
		}
	}
	var out, stderr bytes.Buffer
	if status := run(valid, &out, &stderr); status != 0 || !strings.Contains(out.String(), `"recovered_teardown_pairs": 1`) || !strings.Contains(out.String(), `"recovered_lost_pairs": 0`) {
		t.Fatalf("completed teardown status=%d stdout=%q stderr=%q", status, out.String(), stderr.String())
	}
	fmt.Fprintln(os.Stdout, "CATALOG_COMPLETED_TEARDOWN_OK")
}
