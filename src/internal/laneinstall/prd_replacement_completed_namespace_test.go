package laneinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestCompletedReplacementBootstrapNamespaceAcceptsRecordedLayout(t *testing.T) {
	fixture := newCompletedBootstrapNamespaceFixture(t)
	first, err := validatePRDReplacementBootstrapCompletedNamespace(fixture.plan, fixture.journal)
	if err != nil {
		t.Fatalf("validate completed namespace: %v", err)
	}
	second, err := validatePRDReplacementBootstrapCompletedNamespace(fixture.plan, fixture.journal)
	if err != nil || first != second {
		t.Fatalf("repeat validation changed identity: first=%s second=%s err=%v", first, second, err)
	}
	if err := os.Chmod(fixture.plan.Namespace.ServiceRoot, prdBootstrapRootModeBefore); err != nil {
		t.Fatal(err)
	}
	afterModeCorrection, err := validatePRDReplacementBootstrapCompletedNamespace(fixture.plan, fixture.journal)
	if err != nil {
		t.Fatalf("completed namespace at the recorded pre-correction root mode was rejected: %v", err)
	}
	if afterModeCorrection != first {
		t.Fatal("root-mode-only correction changed the normalized namespace artifact digest")
	}
}

func TestCompletedReplacementBootstrapNamespaceRejectsDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, completedBootstrapNamespaceFixture)
	}{
		{name: "unknown file", mutate: func(t *testing.T, fixture completedBootstrapNamespaceFixture) {
			writeCompletedBootstrapFixtureFile(t, filepath.Join(filepath.Dir(fixture.plan.Namespace.ConfigFile), "unexpected"), []byte("extra"), 0o600)
		}},
		{name: "database identity", mutate: func(t *testing.T, fixture completedBootstrapNamespaceFixture) {
			writeCompletedBootstrapFixtureFile(t, fixture.plan.Namespace.Database, []byte("different database"), 0o600)
		}},
		{name: "unsafe directory mode", mutate: func(t *testing.T, fixture completedBootstrapNamespaceFixture) {
			if err := os.Chmod(filepath.Join(fixture.plan.Namespace.ServiceRoot, "secrets"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink file", mutate: func(t *testing.T, fixture completedBootstrapNamespaceFixture) {
			path := filepath.Join(fixture.plan.Namespace.ServiceRoot, "secrets", "server.pem")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(fixture.plan.Namespace.ServiceRoot, "secrets", "server.key"), path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "authorized keys changed", mutate: func(t *testing.T, fixture completedBootstrapNamespaceFixture) {
			writeCompletedBootstrapFixtureFile(t, fixture.plan.Namespace.SSHAuthorityPath, []byte("changed key list\n"), 0o600)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompletedBootstrapNamespaceFixture(t)
			test.mutate(t, fixture)
			if _, err := validatePRDReplacementBootstrapCompletedNamespace(fixture.plan, fixture.journal); err == nil {
				t.Fatal("drifted completed namespace passed validation")
			}
		})
	}
}

func TestCompletedReplacementBootstrapConfigurationBindsReplacementRoute(t *testing.T) {
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	data, err := config.BuildFreshPRDReplacementLinuxConfig([]byte(prdReplacementBootstrapLinuxTemplate))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "linux.yaml")
	writeCompletedBootstrapFixtureFile(t, path, data, 0o600)
	plan.Namespace.ConfigFile = path
	journal := PRDReplacementBootstrapJournal{ConfigurationSHA256: digestBytes(data)}
	if err := validatePRDReplacementBootstrapConfiguration(plan.Namespace, journal); err != nil {
		t.Fatalf("replacement Linux configuration rejected: %v", err)
	}

	changed := []byte(strings.Replace(string(data), config.SimpleReplacementLinuxPublicIP, "129.151.232.40", 1))
	writeCompletedBootstrapFixtureFile(t, path, changed, 0o600)
	journal.ConfigurationSHA256 = digestBytes(changed)
	if err := validatePRDReplacementBootstrapConfiguration(plan.Namespace, journal); err == nil {
		t.Fatal("retired Linux endpoint was accepted in replacement PRD configuration")
	}

	changed = []byte(strings.ReplaceAll(string(data), "linux-host", "sandbox-host"))
	writeCompletedBootstrapFixtureFile(t, path, changed, 0o600)
	journal.ConfigurationSHA256 = digestBytes(changed)
	if err := validatePRDReplacementBootstrapConfiguration(plan.Namespace, journal); err == nil {
		t.Fatal("replacement namespace accepted a valid but foreign remote target profile")
	}
}

type completedBootstrapNamespaceFixture struct {
	plan    PRDReplacementBootstrapPlan
	journal PRDReplacementBootstrapJournal
}

func newCompletedBootstrapNamespaceFixture(t *testing.T) completedBootstrapNamespaceFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "remote-session-runner")
	ns := config.LaneNamespace{Lane: config.ReleaseLanePRD, Host: config.HostKindLinux, RemoteProfile: "linux-host",
		Account: config.LinuxAccount, ServiceRoot: root, ConfigFile: filepath.Join(root, "config", "linux.yaml"),
		Database: filepath.Join(root, "state", "remote.db"), Workspaces: filepath.Join(root, "workspaces"),
		ScriptTempRoot: filepath.Join(root, "tmp", "scripts"), Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Binaries: filepath.Join(root, "bin"), Staging: filepath.Join(root, "staging"), LifecycleLock: filepath.Join(root, "run", "runnerd-lifecycle.lock"),
		PrivateSocket: filepath.Join(root, "run", "runnerd.sock"), SSHAuthorityPath: filepath.Join(t.TempDir(), ".ssh", "authorized_keys"),
		RunnerUnit: "runnerd.service", DirectPort: 8443}
	ns.DatabaseWAL, ns.DatabaseSHM = ns.Database+"-wal", ns.Database+"-shm"
	for _, path := range []string{root, filepath.Join(root, "config"), filepath.Join(root, "secrets"), filepath.Join(root, "state"),
		filepath.Join(root, "run"), ns.Workspaces, filepath.Join(root, "tmp"), ns.ScriptTempRoot,
		ns.Backups, ns.Logs, ns.Binaries, ns.Staging, filepath.Dir(ns.SSHAuthorityPath)} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configBytes := []byte("test configuration identity\n")
	databaseBytes := []byte("test database identity")
	writeCompletedBootstrapFixtureFile(t, ns.ConfigFile, configBytes, 0o600)
	for _, item := range []struct{ path, contents string }{
		{filepath.Join(root, "config", "ssh-controller-map.yaml"), "controller map\n"},
		{filepath.Join(root, "config", "client-principals.yaml"), "principal map\n"},
		{filepath.Join(root, "secrets", "server.pem"), "server cert\n"},
		{filepath.Join(root, "secrets", "server.key"), "server key\n"},
		{filepath.Join(root, "secrets", "client-ca.pem"), "client CA\n"},
	} {
		writeCompletedBootstrapFixtureFile(t, item.path, []byte(item.contents), 0o600)
	}
	writeCompletedBootstrapFixtureFile(t, ns.Database, databaseBytes, 0o600)
	writeCompletedBootstrapFixtureFile(t, ns.DatabaseWAL, nil, 0o600)
	writeCompletedBootstrapFixtureFile(t, ns.DatabaseSHM, []byte("shm"), 0o600)
	writeCompletedBootstrapFixtureFile(t, ns.LifecycleLock, nil, 0o600)
	marker, err := json.MarshalIndent(struct {
		OperationID string `json:"operation_id"`
		InputSHA256 string `json:"input_sha256"`
	}{prdBootstrapRootModeOperationID, digestBytes([]byte("input"))}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	marker = append(marker, '\n')
	writeCompletedBootstrapFixtureFile(t, filepath.Join(ns.Staging, "catalog-prd-bootstrap-database.json"), marker, 0o600)
	writeCompletedBootstrapFixtureFile(t, filepath.Join(ns.Staging, catalogFreshInstallBaselineFile), []byte("baseline receipt\n"), 0o600)
	beforeKeys := []byte("existing key\n")
	afterKeys := append(append([]byte(nil), beforeKeys...), []byte("runner key\n")...)
	writeCompletedBootstrapFixtureFile(t, ns.SSHAuthorityPath, afterKeys, 0o600)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	absence := CatalogNamespaceAbsenceReport{FormatVersion: 1, HostID: "linux-host", Lane: config.ReleaseLanePRD,
		SelectedLane: config.ReleaseLanePRD, State: "absent", ServiceRoot: root, ServiceRootState: "absent",
		ConfigurationPath: ns.ConfigFile, ConfigurationState: "absent", DatabasePath: ns.Database, DatabaseState: "absent",
		SelectorJournalRoot: filepath.Join(ns.Staging, "catalog-operations"), SelectorJournalsState: "absent",
		CohortReceiptRoot: filepath.Join(ns.Staging, "catalog-cohorts"), CohortReceiptsState: "absent",
		BackupRoot: ns.Backups, BackupState: "absent", LifecycleLock: ns.LifecycleLock, LifecycleLockState: "absent",
		ServiceUnit: ns.RunnerUnit, UnitLoadState: "not-found", UnitFileState: "not-found", UnitActiveState: "inactive", UnitSubState: "dead",
		DirectListenerPort: ns.DirectPort, DirectListenerAbsent: true, ProcessScanComplete: true, ObservedAtUTC: now}
	absence.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(absence)
	plan := PRDReplacementBootstrapPlan{OperationID: prdBootstrapRootModeOperationID, Absence: absence, Namespace: ns}
	journal := PRDReplacementBootstrapJournal{FormatVersion: prdReplacementBootstrapJournalVersion,
		OperationID: plan.OperationID, Absence: absence, InputSHA256: digestBytes([]byte("input")),
		CompletedSteps: append([]string(nil), prdReplacementBootstrapSteps...), DatabaseSchema: 40,
		ConfigurationSHA256: digestBytes(configBytes), WorkInventorySHA256: (CatalogWorkInventory{}).SHA256(),
		DatabaseStagingPath: filepath.Join(filepath.Dir(ns.Database), "catalog-prd-bootstrap-"+plan.OperationID+".db"),
		DatabaseSHA256:      digestBytes(databaseBytes), AuthorizedKeysPath: ns.SSHAuthorityPath,
		AuthorizedKeysBeforeContents: beforeKeys, AuthorizedKeysBefore: digestBytes(beforeKeys),
		AuthorizedKeysAfter: digestBytes(afterKeys), AuthorizedKeysExisted: true, AuthorizedKeysMode: 0o600}
	return completedBootstrapNamespaceFixture{plan: plan, journal: journal}
}

func writeCompletedBootstrapFixtureFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
