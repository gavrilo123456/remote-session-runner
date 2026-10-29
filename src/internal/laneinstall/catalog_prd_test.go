package laneinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

type prdFenceOrderService struct {
	active bool
	lockFD int
	events []string
}

func (s *prdFenceOrderService) Status(_ context.Context, _ PRDUpgradePlan) (bool, error) {
	if s.active {
		s.events = append(s.events, "status-active")
	} else {
		s.events = append(s.events, "status-stopped")
	}
	return s.active, nil
}

func (s *prdFenceOrderService) Stop(_ context.Context, _ PRDUpgradePlan) error {
	s.events = append(s.events, "stop")
	if !s.active || s.lockFD < 0 {
		return ErrPRDUpgradeGuard
	}
	if err := unix.Flock(s.lockFD, unix.LOCK_UN); err != nil {
		return err
	}
	if err := unix.Close(s.lockFD); err != nil {
		return err
	}
	s.lockFD = -1
	s.active = false
	return nil
}

func TestPRDCohortFenceStopsServiceBeforeAcquiringLifecycleLock(t *testing.T) {
	root := simpleTestTempDir(t)
	lockDirectory := filepath.Join(root, "private-lane")
	if err := os.Mkdir(lockDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	plan.Namespace.LifecycleLock = filepath.Join(lockDirectory, "prd-lifecycle.lock")
	daemonLockFD, err := unix.Open(plan.Namespace.LifecycleLock, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(daemonLockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(daemonLockFD)
		t.Fatal(err)
	}
	service := &prdFenceOrderService{active: true, lockFD: daemonLockFD}
	if _, err := acquirePRDUpgradeLock(plan); !errors.Is(err, ErrSimpleLaneLockHeld) {
		t.Fatalf("running PRD lifecycle lock was not held before stop: %v", err)
	}
	lockFD, err := stopPRDServiceAndAcquireFenceLock(context.Background(), plan, true, service, func(plan PRDUpgradePlan) (int, error) {
		service.events = append(service.events, "acquire-lock")
		return acquirePRDUpgradeLock(plan)
	})
	if err != nil {
		t.Fatalf("stop selected service and acquire its released lifecycle lock: %v", err)
	}
	defer releasePRDUpgradeLock(lockFD)
	want := []string{"status-active", "stop", "status-stopped", "acquire-lock", "status-stopped"}
	if !reflect.DeepEqual(service.events, want) {
		t.Fatalf("fence lock lifecycle order=%v, want %v", service.events, want)
	}
	if service.active {
		t.Fatal("service remained active while the fence lifecycle lock was held")
	}
}

func TestPRDLegacyBundleDigestBindsHostAndEveryInstalledFile(t *testing.T) {
	preflight := PRDPreflight{HostID: "linux-host", BinarySHA256: map[string]string{
		"runnerd": strings.Repeat("a", 64), "runner-log-capture": strings.Repeat("b", 64),
	}}
	first, err := PRDLegacyBundleDigest(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.BinarySHA256["runnerd"] = strings.Repeat("c", 64)
	second, err := PRDLegacyBundleDigest(preflight)
	if err != nil || first == second {
		t.Fatalf("legacy identity did not change with binary bytes: first=%s second=%s err=%v", first, second, err)
	}
	preflight.HostID = "sandbox-host"
	third, err := PRDLegacyBundleDigest(preflight)
	if err != nil || second == third {
		t.Fatalf("legacy identity did not change with host identity: second=%s third=%s err=%v", second, third, err)
	}
	preflight.BinarySHA256["../runnerd"] = strings.Repeat("d", 64)
	if _, err := PRDLegacyBundleDigest(preflight); err == nil {
		t.Fatal("legacy identity accepted a path-bearing component name")
	}
}

func TestFreshPRDStagedArtifactsAllowOnlyExactCatalogLaunchFiles(t *testing.T) {
	artifacts, err := BuildPRDCatalogServiceArtifacts("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	var exact = make(map[string]string)
	for _, artifact := range artifacts {
		if artifact.Kind == ArtifactEntrypoint || artifact.Kind == ArtifactSSHWrapper {
			exact[filepath.Base(artifact.InstallPath)] = sha256HexString([]byte(artifact.Contents))
		}
	}
	for name, observed := range map[string]map[string]string{
		"initial empty bin":           {},
		"entrypoint installed":        {filepath.Base(artifacts[0].InstallPath): exact[filepath.Base(artifacts[0].InstallPath)]},
		"both launch files installed": exact,
	} {
		if err := validateFreshPRDStagedArtifactInventory("linux-host", observed); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	for name, observed := range map[string]map[string]string{
		"wrong host":        exact,
		"changed artifact":  {filepath.Base(artifacts[0].InstallPath): strings.Repeat("a", 64)},
		"unexpected binary": {"runnerd": strings.Repeat("b", 64)},
	} {
		host := "linux-host"
		if name == "wrong host" {
			host = "sandbox-host"
		}
		if err := validateFreshPRDStagedArtifactInventory(host, observed); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestFreshPRDEntrypointCreationUsesCanonicalOwnerBranch(t *testing.T) {
	artifacts, err := BuildPRDCatalogServiceArtifacts("linux-host")
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("build canonical Linux PRD artifacts: count=%d err=%v", len(artifacts), err)
	}
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil || !freshPRDCatalogArtifactAllowed(plan, artifacts[0]) {
		t.Fatalf("canonical entrypoint is not admitted by the fresh-creation path: artifact=%+v err=%v", artifacts[0], err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatalf("current owner group unavailable: group=%v err=%v", group, err)
	}
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	entrypoint := artifacts[0]
	entrypoint.InstallPath = filepath.Join(root, "runner-entrypoint.sh")
	// BuildPRDCatalogServiceArtifacts must name the deployed Linux identity;
	// the same creation helper is then exercised with the current test account
	// so this branch runs on the Mac as well as on native Ubuntu workers.
	entrypoint.Owner, entrypoint.Group = current.Username, group.Name
	if err := createFreshPRDCatalogUserArtifact(entrypoint); err != nil {
		t.Fatalf("create exact owner-managed fresh entrypoint: %v", err)
	}
	if err := verifyPRDCatalogArtifact(entrypoint); err != nil {
		t.Fatalf("created entrypoint failed the production verifier: %v", err)
	}
}

func TestFreshPRDSystemdUnitCreateScriptResumesAtomicInstallAndRejectsConflict(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the production systemd helper uses Linux stat, install, and sync utilities")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "systemd")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "runnerd.service")
	source := filepath.Join(root, "staged-unit")
	contents := []byte("[Service]\nExecStart=/fixture/runnerd\n")
	sha := sha256HexString(contents)
	if err := os.WriteFile(source, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	script := renderAtomicPRDSystemdCreateScript(target, current.Username, current.Username, group.Name)
	run := func() error {
		t.Helper()
		command := exec.Command("/bin/sh", "-c", script, "runner-lane", target, source, sha)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("atomic unit script: %w: %s", err, output)
		}
		return nil
	}
	unitSHA := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read unit fixture %s: %v", path, err)
		}
		return sha256HexString(data)
	}

	t.Run("resume interruption after no-overwrite link", func(t *testing.T) {
		interruptedTemp := filepath.Join(root, ".runner-prd-catalog-unit-"+sha+".interrupted")
		if err := os.WriteFile(interruptedTemp, contents, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(interruptedTemp, target); err != nil {
			t.Fatal(err)
		}
		if err := run(); err != nil {
			t.Fatalf("exact interrupted unit retry: %v", err)
		}
		if unitSHA(target) != sha {
			t.Fatal("retry changed the installed unit bytes")
		}
		if _, err := os.Lstat(interruptedTemp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("matching interrupted hardlink was not finalized: %v", err)
		}
		if err := run(); err != nil {
			t.Fatalf("idempotent installed-unit retry: %v", err)
		}
	})

	t.Run("reject conflicting unit without overwrite", func(t *testing.T) {
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		conflict := []byte("[Service]\nExecStart=/unexpected/runnerd\n")
		if err := os.WriteFile(target, conflict, 0o644); err != nil {
			t.Fatal(err)
		}
		before := unitSHA(target)
		if err := run(); err == nil {
			t.Fatal("unit creation accepted a conflicting installed file")
		}
		if unitSHA(target) != before {
			t.Fatal("conflicting unit was overwritten")
		}
	})

	t.Run("clean exact pre-link stage and install", func(t *testing.T) {
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		staleStage := filepath.Join(root, ".runner-prd-catalog-unit-"+sha+".prelink")
		if err := os.WriteFile(staleStage, contents, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run(); err != nil {
			t.Fatalf("retry after a staged pre-link interruption: %v", err)
		}
		if unitSHA(target) != sha {
			t.Fatal("retry did not install the exact unit bytes")
		}
		if _, err := os.Lstat(staleStage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("matching stale unit stage was not cleaned: %v", err)
		}
	})
}

func TestCatalogLegacyCompatibilityInputIsCanonicalOwnerOnlyAndHashBound(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "legacy-compatibility.json")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	compatibility := CatalogLegacyRollbackCompatibility{
		FormatVersion: 1, LegacyBundleSHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64),
		DatabaseSchema: store.CurrentSchemaVersion, ReadSchemaMin: store.CurrentSchemaVersion, ReadSchemaMax: store.CurrentSchemaVersion,
		TestSuite: catalogLegacyRollbackTestSuite, TestEvidenceSHA256: strings.Repeat("c", 64),
		SemanticChecks: append([]string(nil), catalogLegacyRollbackSemanticChecks...), TestedAt: now,
	}
	data, err := json.MarshalIndent(compatibility, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCatalogLegacyRollbackCompatibilityFile(path)
	if err != nil {
		t.Fatalf("load canonical owner-only compatibility input: %v", err)
	}
	if err := ValidateCatalogLegacyRollbackCompatibility(loaded, compatibility.LegacyBundleSHA256,
		compatibility.ConfigurationSHA256, compatibility.DatabaseSchema); err != nil {
		t.Fatalf("exact legacy binary/config/schema compatibility was rejected: %v", err)
	}
	if err := ValidateCatalogLegacyRollbackCompatibility(loaded, strings.Repeat("d", 64),
		compatibility.ConfigurationSHA256, compatibility.DatabaseSchema); err == nil {
		t.Fatal("compatibility proof was accepted for a different installed binary set")
	}
	if err := ValidateCatalogLegacyRollbackCompatibility(loaded, compatibility.LegacyBundleSHA256,
		strings.Repeat("e", 64), compatibility.DatabaseSchema); err == nil {
		t.Fatal("compatibility proof was accepted for a different installed configuration")
	}
	if err := ValidateCatalogLegacyRollbackCompatibility(loaded, compatibility.LegacyBundleSHA256,
		compatibility.ConfigurationSHA256, compatibility.DatabaseSchema+1); err == nil {
		t.Fatal("compatibility proof was accepted for a different database schema")
	}
}

func TestPRDCatalogPreBackupCapacitySeparatesBackupBufferAndSelectorReserve(t *testing.T) {
	selectorReserve := PRDCatalogSelectorHeadroomBytes()
	if selectorReserve != 64*1024*1024 {
		t.Fatalf("selector reserve=%d, want 64 MiB", selectorReserve)
	}
	backupAndSelector := uint64(400 * 1024 * 1024)
	required, err := prdCatalogPreBackupRequiredBytes(backupAndSelector)
	if err != nil {
		t.Fatal(err)
	}
	if required != backupAndSelector+500_000_000 {
		t.Fatalf("pre-backup required=%d, want backup/staging estimate %d plus separate 0.5 GB operating buffer", required, backupAndSelector)
	}
	// Keep the observed Sandbox budget separate from the policy reserve:
	// reducing the reserve does not remove the backup or selector allowance.
	const sandboxBackupAndSelector = uint64(349_205_489)
	if sandboxRequired, err := prdCatalogPreBackupRequiredBytes(sandboxBackupAndSelector); err != nil || sandboxRequired != 849_205_489 {
		t.Fatalf("Sandbox pre-backup required=%d, err=%v; want 849205489 bytes", sandboxRequired, err)
	}
	if _, err := prdCatalogPreBackupRequiredBytes(^uint64(0)); !errors.Is(err, ErrPRDUpgradeGuard) {
		t.Fatalf("overflowing pre-backup requirement error=%v", err)
	}
}

func TestPRDCatalogHostPreflightRejectsOperatingBufferAsSelectorReserveBeforeBackup(t *testing.T) {
	hostID := "linux-host"
	if runtime.GOOS == "darwin" {
		hostID = "mac-control-plane"
	} else if runtime.GOARCH == "arm64" {
		hostID = "sandbox-host"
	}
	plan, err := BuildPRDUpgradePlan(hostID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadPRDCatalogCohortHostPreflight(context.Background(), plan, nil, "fixture-release", prdCatalogOperatingBufferBytes, nil)
	if !errors.Is(err, ErrPRDUpgradeGuard) {
		t.Fatalf("preflight accepted the 0.5 GB operating buffer as selector reserve or touched dependencies first: %v", err)
	}
}

func TestPRDCatalogOnlinePreflightDoesNotContendWithRunningServiceLock(t *testing.T) {
	tempBase, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(tempBase, "prd-online-preflight-lock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := simpleTestPlanAt(t, root)
	plan.Lane = config.ReleaseLanePRD
	plan.Namespace.Lane = config.ReleaseLanePRD
	if err := os.MkdirAll(filepath.Dir(plan.Namespace.LifecycleLock), 0o700); err != nil {
		t.Fatal(err)
	}
	// A running PRD service holds this lock for its lifetime. Online preflight
	// must remain possible while the service is active; the later cohort fence
	// is responsible for acquiring the lifecycle lock before it stops the host.
	serviceFD, err := acquireSimpleLaneLock(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePRDUpgradeLock(serviceFD)

	catalogRoot := filepath.Join(root, "runner-releases")
	if err := os.Mkdir(catalogRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	preflightFD, err := acquirePRDCatalogOnlinePreflightLock(catalogRoot)
	if err != nil {
		t.Fatalf("online preflight was blocked by the running service lifecycle lock: %v", err)
	}
	releaseCatalogRootPublicationLock(preflightFD)
}

func TestPRDCatalogArtifactGroupOwnershipIsCheckedOnlyWhenDeclared(t *testing.T) {
	if !catalogArtifactGroupMatches("", 2002, 1001) {
		t.Fatal("an unspecified artifact group rejected an otherwise owner-verified group")
	}
	if catalogArtifactGroupMatches("runner", 2002, 1001) {
		t.Fatal("an explicitly declared artifact group accepted a different group")
	}
	if !catalogArtifactGroupMatches("runner", 1001, 1001) {
		t.Fatal("an explicitly declared current-account group was rejected")
	}
}

func TestPRDSelectorMutationRequiresGuardedMaintenanceAndSupportsUnselectedState(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	fixture.layout.lane = config.ReleaseLanePRD
	if err := validateCatalogMutationLayout(fixture.layout); err == nil {
		t.Fatal("generic Dev/QAS selector mutation accepted PRD")
	}
	if err := validateCatalogTransitionLayout(fixture.layout); err != nil {
		t.Fatalf("guarded PRD transition layout rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("initial PRD selector status=%+v err=%v", status, err)
	}
	target := filepath.Join(fixture.catalogRoot, fixture.firstRelease, fixture.layout.hostID)
	if err := switchCatalogSelector(fixture.layout, "", target); err != nil {
		t.Fatalf("select initial catalog release: %v", err)
	}
	if err := switchCatalogSelector(fixture.layout, target, ""); err != nil {
		t.Fatalf("return selector to preserved legacy layout: %v", err)
	}
	status, err = inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("restored legacy selector status=%+v err=%v", status, err)
	}
}

func TestPRDReconciliationRecognizesExactUnselectedLegacyIdentity(t *testing.T) {
	now := time.Now().UTC()
	legacySHA := strings.Repeat("a", 64)
	configurationSHA := strings.Repeat("b", 64)
	host := CatalogCohortHostPreflight{
		HostID: "mac-control-plane", PreviousBundleSHA256: legacySHA,
		PreviousReadSchemaMax: store.CurrentSchemaVersion, ConfigurationSHA256: configurationSHA, SchemaVersion: store.CurrentSchemaVersion,
		LegacyRollback: validLegacyRollbackEvidence(legacySHA, configurationSHA, store.CurrentSchemaVersion, now),
	}
	status := CatalogSelectorStatus{Kind: "unselected"}
	if !catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("an exact evidence-bound legacy PRD identity was rejected after restoring the unselected launcher")
	}
	status.Kind = "catalog"
	if catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("unselected legacy receipt accepted a catalog selector")
	}
	status = CatalogSelectorStatus{Kind: "unselected"}
	host.LegacyRollback.ConfigurationSHA256 = strings.Repeat("c", 64)
	if catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("legacy rollback identity accepted evidence bound to another configuration")
	}
}

func TestPRDHostFenceCanRetryAfterStopBeforeReceiptWrite(t *testing.T) {
	if !validPRDCohortFenceServiceState(true, false) {
		t.Fatal("fence retry refused the selected service already stopped before receipt persistence")
	}
	if !validPRDCohortFenceServiceState(true, true) || !validPRDCohortFenceServiceState(false, false) {
		t.Fatal("fence service state accepted/rejected a valid preflight state incorrectly")
	}
	if validPRDCohortFenceServiceState(false, true) {
		t.Fatal("fence retry accepted a service started after a stopped preflight")
	}
}

func TestPRDCatalogArtifactReplacementIsExpectedHashBound(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "entrypoint.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runnerd\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactEntrypoint, InstallPath: path, Mode: 0o700, Contents: "#!/bin/sh\nexec /new/current/bin/runnerd\n"}
	service := &PRDServiceController{}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace backed-up entrypoint: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("replacement did not match exact catalog artifact: %v", err)
	}
	conflict := []byte("#!/bin/sh\nexec /unexpected/runnerd\n")
	if err := os.WriteFile(path, conflict, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err == nil {
		t.Fatal("artifact replacement overwrote a file that differed from both expected identities")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(conflict) {
		t.Fatalf("conflicting artifact was changed: %q err=%v", actual, err)
	}
}

func TestPRDCatalogReplacementSupportsOnlyCurrentAccountOwnedQueuedWrapper(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "runner-ssh-bridge-forced.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runner-ssh-bridge\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactSSHWrapper, InstallPath: path, Mode: 0o700, Owner: current.Username, Group: group.Name,
		Contents: "#!/bin/sh\nexec /new/current/bin/runner-ssh-bridge\n"}
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace current-account queued wrapper: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("installed queued wrapper does not match expected owner, group, mode, and bytes: %v", err)
	}
	foreign := artifact
	foreign.Owner = "nobody"
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, foreign, sha256HexString([]byte(artifact.Contents)), 0o700); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("replacement did not retain the conflict category for a foreign artifact owner: %v", err)
	}
}

func TestPRDLegacyRollbackRestoresLauncherAndQueuedWrapperWithoutChangingDatabase(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	backupRoot := filepath.Join(root, "backup")
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceRoot := filepath.Join(root, "service")
	if err := os.Mkdir(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	productionDatabase := filepath.Join(root, "production-local.db")
	if err := os.WriteFile(productionDatabase, []byte("preserved production database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := fileSHA256(productionDatabase)
	if err != nil {
		t.Fatal(err)
	}
	legacyBinDir := filepath.Join(root, "legacy", "bin")
	if err := os.MkdirAll(legacyBinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyRunner := filepath.Join(legacyBinDir, "runnerd")
	legacyBridge := filepath.Join(legacyBinDir, "runner-ssh-bridge")
	for path, marker := range map[string]string{legacyRunner: "legacy-runner-resolved", legacyBridge: "legacy-bridge-resolved"} {
		script := []byte("#!/bin/sh\nprintf '" + marker + "\\n'\nprintf '%s\\n' \"$@\"\n")
		if err := os.WriteFile(path, script, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	definitions := []struct {
		name    string
		kind    ArtifactKind
		legacy  []byte
		catalog string
	}{
		{name: "runner-entrypoint.sh", kind: ArtifactEntrypoint, legacy: []byte(fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", legacyRunner)), catalog: "#!/bin/sh\nexec /catalog/current/bin/runnerd\n"},
		{name: "runner-ssh-bridge-forced.sh", kind: ArtifactSSHWrapper, legacy: []byte(fmt.Sprintf("#!/bin/sh\nexec %q --stdio \"$@\"\n", legacyBridge)), catalog: "#!/bin/sh\nexec /catalog/current/bin/runner-ssh-bridge\n"},
	}
	manifest := PRDBackupManifest{FormatVersion: prdUpgradeBackupFormat, HostID: "linux-host", SchemaVersion: 40,
		DatabasePath: productionDatabase, DatabaseBackupPath: filepath.Join(backupRoot, "database.sqlite")}
	if err := os.WriteFile(manifest.DatabaseBackupPath, []byte("private snapshot placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	var artifacts []Artifact
	for _, definition := range definitions {
		installPath := filepath.Join(serviceRoot, definition.name)
		catalogBytes := []byte(definition.catalog)
		if err := os.WriteFile(installPath, catalogBytes, 0o700); err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(backupRoot, "service", definition.name)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backupPath, definition.legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, PRDBackupFile{
			TargetPath: installPath, BackupPath: filepath.Join("service", definition.name),
			SHA256: sha256HexString(definition.legacy), Bytes: int64(len(definition.legacy)), Mode: 0o700,
		})
		artifacts = append(artifacts, Artifact{Kind: definition.kind, InstallPath: installPath, Mode: 0o700, Contents: definition.catalog})
	}
	if err := restorePRDServiceArtifactsFromBackup(context.Background(), &PRDServiceController{}, artifacts, manifest); err != nil {
		t.Fatalf("restore exact legacy launcher and queued wrapper: %v", err)
	}
	for index, definition := range definitions {
		actual, err := os.ReadFile(artifacts[index].InstallPath)
		if err != nil || string(actual) != string(definition.legacy) {
			t.Fatalf("restored %s=%q err=%v", definition.name, actual, err)
		}
		info, err := os.Stat(artifacts[index].InstallPath)
		if err != nil {
			t.Fatalf("stat restored %s: %v", definition.name, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("restored %s mode=%v", definition.name, info.Mode().Perm())
		}
		output, err := exec.Command(artifacts[index].InstallPath, "fixture-argument").CombinedOutput()
		if err != nil {
			t.Fatalf("execute restored legacy artifact %s: %v; output=%s", definition.name, err, output)
		}
		want := "legacy-runner-resolved"
		if definition.kind == ArtifactSSHWrapper {
			want = "legacy-bridge-resolved"
		}
		if !strings.Contains(string(output), want) || !strings.Contains(string(output), "fixture-argument") {
			t.Fatalf("restored %s did not resolve its preserved binary: %q", definition.name, output)
		}
	}
	databaseAfter, err := fileSHA256(productionDatabase)
	if err != nil || databaseAfter != databaseBefore {
		t.Fatalf("rollback artifact restore changed production database: before=%s after=%s err=%v", databaseBefore, databaseAfter, err)
	}
}

func TestPRDMacLegacyRollbackKeepsDisabledQASHandoffConfigAndStoppedService(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	backupRoot := filepath.Join(root, "backup")
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceRoot := filepath.Join(root, "service")
	if err := os.Mkdir(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(serviceRoot, "mac.yaml")
	currentDisabledConfig := []byte("mac:\n  mailbox_ingress_enabled: false\nmailboxes: {}\n")
	if err := os.WriteFile(configPath, currentDisabledConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	oldQASOwnedConfig := []byte("mac:\n  mailbox_ingress_enabled: true\nmailboxes:\n  project-a:\n    root: /Users/runner/qas-mailbox/project-a\n")
	oldConfigBackupPath := filepath.Join(backupRoot, "config", "mac.yaml")
	if err := os.MkdirAll(filepath.Dir(oldConfigBackupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldConfigBackupPath, oldQASOwnedConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(serviceRoot, "local.db")
	retainedDatabase := []byte("preserved schema-40 PRD database sentinel")
	if err := os.WriteFile(databasePath, retainedDatabase, 0o600); err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := fileSHA256(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	definitions := []struct {
		name     string
		original []byte
		catalog  []byte
	}{
		{name: "com.remote-session-runner.local.plist", original: []byte("legacy local LaunchAgent\n"), catalog: []byte("catalog local LaunchAgent\n")},
		{name: "com.remote-session-runner.locald.plist", original: []byte("legacy locald LaunchAgent\n"), catalog: []byte("catalog locald LaunchAgent\n")},
	}
	manifest := PRDBackupManifest{FormatVersion: prdUpgradeBackupFormat, HostID: "mac-control-plane",
		SchemaVersion: 40, DatabasePath: databasePath, DatabaseBackupPath: filepath.Join(backupRoot, "local.db")}
	if err := os.WriteFile(manifest.DatabaseBackupPath, []byte("consistent pre-switch database snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Files = append(manifest.Files, PRDBackupFile{
		TargetPath: configPath, BackupPath: filepath.Join("config", "mac.yaml"),
		SHA256: sha256HexString(oldQASOwnedConfig), Bytes: int64(len(oldQASOwnedConfig)), Mode: 0o600,
	})
	var artifacts []Artifact
	for _, definition := range definitions {
		target := filepath.Join(serviceRoot, definition.name)
		backupPath := filepath.Join(backupRoot, "service", definition.name)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, definition.catalog, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backupPath, definition.original, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(backupPath, 0o644); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, PRDBackupFile{
			TargetPath: target, BackupPath: filepath.Join("service", definition.name),
			SHA256: sha256HexString(definition.original), Bytes: int64(len(definition.original)), Mode: 0o644,
		})
		artifacts = append(artifacts, Artifact{Kind: ArtifactLaunchAgent, InstallPath: target,
			Mode: 0o644, Contents: string(definition.catalog)})
	}

	runner := &simpleServiceTestRunner{macActive: map[string]bool{}}
	service := &PRDServiceController{base: simpleTestServiceController(runner, "501", config.MacAccount, "darwin", "arm64")}
	if err := restorePRDServiceArtifactsFromBackup(context.Background(), service, artifacts, manifest); err != nil {
		t.Fatalf("restore only the preserved Mac launcher artifacts: %v", err)
	}
	for index, definition := range definitions {
		actual, err := os.ReadFile(artifacts[index].InstallPath)
		if err != nil || string(actual) != string(definition.original) {
			t.Fatalf("legacy launch artifact %s=%q err=%v", definition.name, actual, err)
		}
	}
	currentConfigAfter, err := os.ReadFile(configPath)
	if err != nil || string(currentConfigAfter) != string(currentDisabledConfig) {
		t.Fatalf("rollback changed the current disabled-ingress/QAS-handoff config: %q err=%v", currentConfigAfter, err)
	}
	databaseAfter, err := fileSHA256(databasePath)
	if err != nil || databaseAfter != databaseBefore {
		t.Fatalf("rollback changed the preserved PRD database: before=%s after=%s err=%v", databaseBefore, databaseAfter, err)
	}
	if len(runner.calls) != 0 || runner.macActive["com.remote-session-runner.local"] || runner.macActive["com.remote-session-runner.locald"] {
		t.Fatalf("artifact rollback started or controlled PRD services: calls=%v active=%v", runner.calls, runner.macActive)
	}
}

func catalogPRDArtifactFixtureDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	return root
}
