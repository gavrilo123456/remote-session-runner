package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestBuildPRDReplacementBootstrapPlanBindsV2IdentityAndPairedRoutes(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatalf("build bootstrap plan from existing V2 identity: %v", err)
	}
	if plan.Release.ReleaseID != input.ReleaseManifest.ReleaseID || plan.Release.HostID != "linux-host" ||
		plan.Release.ReleaseSHA256 != input.ExpectedReleaseSHA256 || plan.Release.BundleSHA256 != input.Bundle.Manifest.BundleSHA256 ||
		plan.AbsenceEvidenceSHA256 != input.Absence.EvidenceSHA256 ||
		plan.MacConfigSHA256 != digestBytes(input.MacConfiguration) || plan.TemplateConfigSHA256 != digestBytes(input.TemplateConfiguration) ||
		plan.ConfigurationSHA256 != digestBytes(plan.Configuration) || plan.MacCandidateSHA256 != digestBytes(plan.MacConfiguration) {
		t.Fatal("bootstrap plan lost an input or candidate digest binding")
	}
	if plan.DirectClientURISAN != prdReplacementLegacyDirectURI ||
		plan.MacClientCertificatePath != "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem" ||
		plan.MacClientPrivateKeyPath != "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key" ||
		plan.MacDispatcherKeyPath != "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519" {
		t.Fatalf("bootstrap plan replaced or lost the configured legacy identity: %+v", plan)
	}
	if err := config.ValidateFreshPRDReplacementLinuxRoute(input.MacConfiguration, plan.MacConfiguration, plan.Configuration); err != nil {
		t.Fatalf("bootstrap plan did not preserve the paired Mac/Linux route: %v", err)
	}
	files, err := PRDReplacementBootstrapFiles(plan)
	if err != nil {
		t.Fatalf("derive fixed first-install files: %v", err)
	}
	if len(files) != 6 {
		t.Fatalf("bootstrap file count=%d, want config, TLS, ingress and controller-map files", len(files))
	}
	for _, file := range files {
		if !strings.HasPrefix(file.InstallPath, plan.Namespace.ServiceRoot+string(filepath.Separator)) || file.Mode != 0o600 && file.Mode != 0o700 {
			t.Fatalf("bootstrap file escaped the fixed namespace or mode policy: path=%q mode=%o", file.InstallPath, file.Mode)
		}
		if file.InstallPath == plan.Namespace.Database || file.InstallPath == plan.Namespace.RunnerUnit || file.InstallPath == filepath.Join(plan.Namespace.ServiceRoot, "current") ||
			file.InstallPath == plan.Namespace.RunnerEntrypoint || file.InstallPath == plan.QueuedWrapper.InstallPath {
			t.Fatalf("bootstrap file list contains a database, launcher, unit, selector, or wrapper mutation: %s", file.InstallPath)
		}
	}
}

func TestPRDReplacementBootstrapDirectoriesPreserveValidatedToolchainRoot(t *testing.T) {
	makePlan := func(t *testing.T) PRDReplacementBootstrapPlan {
		t.Helper()
		input := prdReplacementBootstrapFixture(t)
		plan, err := BuildPRDReplacementBootstrapPlan(input)
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "remote-session-runner")
		if err := os.Mkdir(root, 0o775); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o775); err != nil {
			t.Fatal(err)
		}
		toolchain := filepath.Join(root, "toolchains", "go1.27.1")
		if err := os.MkdirAll(filepath.Join(toolchain, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(toolchain, "VERSION"), []byte("go1.27.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		goBinary := filepath.Join(toolchain, "bin", "go")
		goBytes := []byte("#!/bin/sh\nexit 0\n")
		if err := os.WriteFile(goBinary, goBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "run", "runnerd-lifecycle.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}

		ns := plan.Namespace
		ns.ServiceRoot = root
		ns.ConfigFile = filepath.Join(root, "config", "linux.yaml")
		ns.Database = filepath.Join(root, "state", "remote.db")
		ns.DatabaseWAL, ns.DatabaseSHM = ns.Database+"-wal", ns.Database+"-shm"
		ns.PrivateSocket = filepath.Join(root, "run", "runnerd.sock")
		ns.Workspaces = filepath.Join(root, "workspaces")
		ns.ScriptTempRoot = filepath.Join(root, "tmp", "scripts")
		ns.Backups = filepath.Join(root, "backups")
		ns.Logs = filepath.Join(root, "logs")
		ns.Binaries = filepath.Join(root, "bin")
		ns.Staging = filepath.Join(root, "staging")
		ns.LifecycleLock = filepath.Join(root, "run", "runnerd-lifecycle.lock")
		plan.Namespace = ns
		toolchainDigest := digestBytes(goBytes)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		plan.Absence = CatalogNamespaceAbsenceReport{
			FormatVersion: 1, HostID: "linux-host", Lane: config.ReleaseLanePRD, SelectedLane: config.ReleaseLanePRD,
			State: "absent", ServiceRoot: root, ServiceRootState: "toolchain-only",
			ToolchainRoot: filepath.Join(root, "toolchains"), ToolchainState: "owner-toolchain-only",
			ToolchainVersion: "go1.27.1", ToolchainBinarySHA256: toolchainDigest,
			ConfigurationPath: ns.ConfigFile, ConfigurationState: "absent",
			DatabasePath: ns.Database, DatabaseState: "absent",
			SelectorJournalRoot: filepath.Join(ns.Staging, "catalog-operations"), SelectorJournalsState: "absent",
			CohortReceiptRoot: filepath.Join(ns.Staging, "catalog-cohorts"), CohortReceiptsState: "absent",
			BackupRoot: ns.Backups, BackupState: "absent", LifecycleLock: ns.LifecycleLock, LifecycleLockState: "absent",
			ServiceUnit: ns.RunnerUnit, UnitLoadState: "not-found", UnitFileState: "not-found",
			UnitActiveState: "inactive", UnitSubState: "dead", DirectListenerPort: ns.DirectPort,
			DirectListenerAbsent: true, ProcessScanComplete: true, ObservedAtUTC: now,
		}
		plan.Absence.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(plan.Absence)
		return plan
	}

	t.Run("create and retry with the operation lifecycle lock", func(t *testing.T) {
		plan := makePlan(t)
		root := plan.Namespace.ServiceRoot
		if err := createPRDReplacementBootstrapDirectories(plan); err != nil {
			t.Fatalf("create private PRD directories under the preserved shared root: %v", err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); err != nil {
			t.Fatalf("exact directory retry failed: %v", err)
		}
		rootInfo, err := os.Lstat(root)
		if err != nil {
			t.Fatalf("stat shared service root: %v", err)
		}
		if rootInfo.Mode().Perm() != 0o775 {
			t.Fatalf("shared service-root mode changed: mode=%v", rootInfo.Mode().Perm())
		}
		for _, path := range []string{filepath.Join(root, "config"), filepath.Join(root, "secrets"), filepath.Join(root, "state"),
			filepath.Join(root, "workspaces"), filepath.Join(root, "tmp"), plan.Namespace.ScriptTempRoot, filepath.Join(root, "backups"),
			filepath.Join(root, "logs"), filepath.Join(root, "bin"), filepath.Join(root, "staging")} {
			info, statErr := os.Lstat(path)
			if statErr != nil {
				t.Fatalf("stat new bootstrap directory %s: %v", path, statErr)
			}
			if !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("new bootstrap directory is not private: %s mode=%v", path, info.Mode().Perm())
			}
		}
	})

	t.Run("create after an absent root was created only for its lifecycle lock", func(t *testing.T) {
		plan := makePlan(t)
		root := plan.Namespace.ServiceRoot
		if err := os.RemoveAll(filepath.Join(root, "toolchains")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		plan.Absence.ServiceRootState = "absent"
		plan.Absence.ToolchainRoot = ""
		plan.Absence.ToolchainState = ""
		plan.Absence.ToolchainVersion = ""
		plan.Absence.ToolchainBinarySHA256 = ""
		plan.Absence.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(plan.Absence)
		if err := createPRDReplacementBootstrapDirectories(plan); err != nil {
			t.Fatalf("bootstrap directories after operation-owned lock creation: %v", err)
		}
	})

	t.Run("resume an interrupted directory step with exact partial directories", func(t *testing.T) {
		plan := makePlan(t)
		configDir := filepath.Join(plan.Namespace.ServiceRoot, "config")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); err != nil {
			t.Fatalf("resume exact partial directory creation: %v", err)
		}
	})

	t.Run("reject unknown root entries before creating namespace", func(t *testing.T) {
		plan := makePlan(t)
		unexpected := filepath.Join(plan.Namespace.ServiceRoot, "unrelated")
		if err := os.Mkdir(unexpected, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); !errors.Is(err, ErrCatalogOperationInvalid) {
			t.Fatalf("unknown service-root entry accepted: %v", err)
		}
		if _, err := os.Lstat(plan.Namespace.ConfigFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("conflicting root caused namespace mutation: %v", err)
		}
	})

	t.Run("reject unexpected contents in a partially created directory", func(t *testing.T) {
		plan := makePlan(t)
		configDir := filepath.Join(plan.Namespace.ServiceRoot, "config")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "unrelated"), []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); !errors.Is(err, ErrCatalogOperationInvalid) {
			t.Fatalf("unexpected partial-directory contents accepted: %v", err)
		}
	})

	t.Run("reject toolchain drift", func(t *testing.T) {
		plan := makePlan(t)
		if err := os.WriteFile(filepath.Join(plan.Namespace.ServiceRoot, "toolchains", "go1.27.1", "bin", "go"), []byte("changed"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); !errors.Is(err, ErrCatalogOperationInvalid) {
			t.Fatalf("changed toolchain accepted: %v", err)
		}
	})

	t.Run("reject unsafe shared-root permissions", func(t *testing.T) {
		plan := makePlan(t)
		if err := os.Chmod(plan.Namespace.ServiceRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := createPRDReplacementBootstrapDirectories(plan); !errors.Is(err, ErrCatalogOperationInvalid) {
			t.Fatalf("world-writable shared root accepted: %v", err)
		}
	})
}

type prdBootstrapFixtureBackend struct {
	mu                   sync.Mutex
	plan                 PRDReplacementBootstrapPlan
	absence              CatalogNamespaceAbsenceReport
	completed            map[string]bool
	interruptStep        string
	interrupted          bool
	applyCounts          map[string]int
	captures             int
	operationLock        bool
	lockAcquires         int
	captureAtCurrentTime bool
	planTime             time.Time
}

func (b *prdBootstrapFixtureBackend) CaptureAbsence(context.Context) (CatalogNamespaceAbsenceReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.captures++
	if b.captureAtCurrentTime {
		b.absence.ObservedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
		b.absence.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(b.absence)
	}
	return b.absence, nil
}

func (b *prdBootstrapFixtureBackend) BuildPlan(_ PRDReplacementBootstrapRequest, absence CatalogNamespaceAbsenceReport,
	now time.Time) (PRDReplacementBootstrapPlan, error) {
	b.planTime = now
	plan := b.plan
	plan.Absence = absence
	plan.AbsenceEvidenceSHA256 = absence.EvidenceSHA256
	return plan, nil
}

func (b *prdBootstrapFixtureBackend) ApplyStep(_ context.Context, _ PRDReplacementBootstrapPlan, step string,
	journal *PRDReplacementBootstrapJournal) error {
	b.mu.Lock()
	if !b.operationLock {
		b.mu.Unlock()
		return errors.New("fixture mutation occurred without the lane lifecycle lock")
	}
	// Model a durable idempotent effect even when the operation journal was not
	// advanced before interruption. Re-entering the step must reconstruct the
	// same result from the already-applied effect.
	if step == "fresh_baseline" {
		journal.DatabaseSchema = 40
		journal.WorkInventorySHA256 = (CatalogWorkInventory{}).SHA256()
	}
	if b.completed[step] {
		b.mu.Unlock()
		return nil
	}
	b.completed[step] = true
	b.applyCounts[step]++
	if step == b.interruptStep && !b.interrupted {
		b.interrupted = true
		b.mu.Unlock()
		return errors.New("fixture interruption after durable step effect")
	}
	b.mu.Unlock()
	return nil
}

func (b *prdBootstrapFixtureBackend) AcquireOperationLock(context.Context, PRDReplacementBootstrapPlan) (func(), error) {
	b.mu.Lock()
	if b.operationLock {
		b.mu.Unlock()
		return nil, ErrCatalogOperationInvalid
	}
	b.operationLock = true
	b.lockAcquires++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.operationLock = false
		b.mu.Unlock()
	}, nil
}

func TestPRDReplacementBootstrapBaselineRetryRequiresSameIdentity(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	work := CatalogWorkInventory{}
	first, err := NewCatalogFreshInstallBaseline(input.OperationID, input.Absence,
		digestBytes(input.TemplateConfiguration), 40, work, input.Now)
	if err != nil {
		t.Fatal(err)
	}
	// A retry can occur after the baseline file was committed but before the
	// bootstrap journal step was. Its new clock read must not change identity.
	retry, err := NewCatalogFreshInstallBaseline(input.OperationID, input.Absence,
		digestBytes(input.TemplateConfiguration), 40, work, input.Now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceSHA256 == retry.EvidenceSHA256 || !samePRDReplacementBootstrapBaseline(first, retry) {
		t.Fatal("same-operation baseline retry must match immutable identity while retaining original timestamp")
	}
	conflict := retry
	conflict.OperationID = "abcdefabcdefabcdefabcdefabcdefab"
	if samePRDReplacementBootstrapBaseline(first, conflict) {
		t.Fatal("different operation must not adopt the existing fresh-install baseline")
	}
	conflict = retry
	conflict.ConfigurationSHA256 = strings.Repeat("f", 64)
	if samePRDReplacementBootstrapBaseline(first, conflict) {
		t.Fatal("different configuration must not adopt the existing fresh-install baseline")
	}
}

func TestBootstrapJournalFixtureRootUsesPrivateTempParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "bootstrap-ops")
	if err := ensureBootstrapJournalRootAtRoot(root); err != nil {
		t.Fatalf("create private temp-root journal fixture: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("journal fixture root is not a private directory: info=%v err=%v", info, err)
	}
}

func (b *prdBootstrapFixtureBackend) VerifyComplete(_ context.Context, _ PRDReplacementBootstrapPlan,
	journal *PRDReplacementBootstrapJournal) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.operationLock {
		return errors.New("completion verification ran without the lane lifecycle lock")
	}
	for _, step := range prdReplacementBootstrapSteps {
		if !b.completed[step] {
			return ErrCatalogOperationInvalid
		}
	}
	if journal.DatabaseSchema != 40 || journal.WorkInventorySHA256 != (CatalogWorkInventory{}).SHA256() {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func TestRunPRDReplacementBootstrapResumesInterruptedPublicSteps(t *testing.T) {
	for _, interruptedStep := range prdReplacementBootstrapSteps {
		t.Run(interruptedStep, func(t *testing.T) {
			input := prdReplacementBootstrapFixture(t)
			request := prdReplacementBootstrapRequest(input)
			plan, err := BuildPRDReplacementBootstrapPlan(input)
			if err != nil {
				t.Fatal(err)
			}
			backend := &prdBootstrapFixtureBackend{plan: plan, absence: input.Absence,
				completed: make(map[string]bool), interruptStep: interruptedStep, applyCounts: make(map[string]int)}
			parent := t.TempDir()
			if err := os.Chmod(parent, 0o700); err != nil {
				t.Fatalf("make task-owned temp parent private: %v", err)
			}
			root := filepath.Join(parent, "bootstrap-ops")
			now := input.Now
			if _, err := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, now); err == nil {
				t.Fatal("expected the injected interruption")
			} else if !strings.Contains(err.Error(), "fixture interruption after durable step effect") {
				_, exists, journalErr := readBootstrapJournal(filepath.Join(root, request.OperationID+".json"))
				backend.mu.Lock()
				captures, lockAcquires, applied := backend.captures, backend.lockAcquires, len(backend.completed)
				backend.mu.Unlock()
				t.Fatalf("first attempt failed before the injected post-effect interruption: %v (journal exists=%t read_error=%v captures=%d lock_acquires=%d applied_steps=%d)",
					err, exists, journalErr, captures, lockAcquires, applied)
			}
			if _, exists, err := readBootstrapJournal(filepath.Join(root, request.OperationID+".json")); err != nil || !exists {
				t.Fatalf("post-effect interruption did not preserve the durable journal: exists=%t err=%v", exists, err)
			}
			result, err := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, now.Add(time.Second))
			if err != nil {
				journal, exists, journalErr := readBootstrapJournal(filepath.Join(root, request.OperationID+".json"))
				plan, planErr := backend.BuildPlan(request, journal.Absence, now)
				t.Fatalf("resume exact operation: %v (journal exists=%t read_error=%v completed=%v plan_error=%v plan_matches=%t)",
					err, exists, journalErr, journal.CompletedSteps, planErr, bootstrapJournalMatchesPlan(journal, plan))
			}
			if result.Stage != "linux_namespace_ready" || result.OperationID != request.OperationID || result.JournalSHA256 == "" {
				t.Fatalf("bootstrap did not reach the resumable promotion checkpoint: %+v", result)
			}
			for _, step := range prdReplacementBootstrapSteps {
				if backend.applyCounts[step] != 1 {
					t.Fatalf("step %s side-effect count=%d, want exactly once", step, backend.applyCounts[step])
				}
			}
			if backend.captures != 1 {
				t.Fatalf("namespace absence was recaptured %d times; resume must use journaled evidence", backend.captures)
			}
			if backend.lockAcquires != 2 || backend.operationLock {
				t.Fatalf("lifecycle lock was not held once through resumed mutation and verification: acquires=%d held=%v", backend.lockAcquires, backend.operationLock)
			}
			journal, exists, err := readBootstrapJournal(filepath.Join(root, request.OperationID+".json"))
			if err != nil || !exists || journal.ApprovalSHA256 != request.ApprovalSHA256 ||
				journal.PublisherOwner != request.PublisherOwner || journal.PublisherControl != request.PublisherControl ||
				!journal.PublishersPaused || !journal.NoNewAccepted {
				t.Fatalf("durable journal did not bind approval and publisher pause: exists=%v journal=%+v err=%v", exists, journal, err)
			}
		})
	}
}

func TestRunPRDReplacementBootstrapTimestampsAfterAbsenceCapture(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	request := prdReplacementBootstrapRequest(input)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	backend := &prdBootstrapFixtureBackend{plan: plan, absence: input.Absence,
		completed: make(map[string]bool), applyCounts: make(map[string]int), captureAtCurrentTime: true}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "bootstrap-ops")
	// This is the command-entry time. CaptureAbsence returns an observation
	// made later, which previously caused the first public bootstrap to fail
	// before it could persist its journal.
	startedAt := input.Now
	result, err := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, startedAt)
	if err != nil {
		t.Fatalf("bootstrap rejected its freshly captured absence report: %v", err)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, backend.absence.ObservedAtUTC)
	if err != nil || backend.planTime.Before(observedAt) {
		t.Fatalf("plan time %s precedes absence observation %s (parse error=%v)", backend.planTime, backend.absence.ObservedAtUTC, err)
	}
	journal, exists, err := readBootstrapJournal(filepath.Join(root, request.OperationID+".json"))
	createdAt, parseErr := time.Parse(time.RFC3339Nano, journal.CreatedAtUTC)
	if err != nil || parseErr != nil || !exists || createdAt.Before(observedAt) || result.Stage != "linux_namespace_ready" {
		t.Fatalf("fresh absence was not durably bound to a successful bootstrap: exists=%t stage=%s journal=%+v err=%v parse=%v",
			exists, result.Stage, journal, err, parseErr)
	}
}

type blockingPRDBootstrapBackend struct {
	*prdBootstrapFixtureBackend
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPRDBootstrapBackend) ApplyStep(ctx context.Context, plan PRDReplacementBootstrapPlan, step string,
	journal *PRDReplacementBootstrapJournal) error {
	if step == "directories" {
		close(b.entered)
		<-b.release
	}
	return b.prdBootstrapFixtureBackend.ApplyStep(ctx, plan, step, journal)
}

func TestRunPRDReplacementBootstrapSerializesConcurrentStarts(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	request := prdReplacementBootstrapRequest(input)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	backend := &blockingPRDBootstrapBackend{prdBootstrapFixtureBackend: &prdBootstrapFixtureBackend{plan: plan,
		absence: input.Absence, completed: make(map[string]bool), applyCounts: make(map[string]int)},
		entered: make(chan struct{}), release: make(chan struct{})}
	root := filepath.Join(t.TempDir(), "bootstrap-ops")
	if err := os.Chmod(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	first := make(chan error, 1)
	go func() {
		_, runErr := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, input.Now)
		first <- runErr
	}()
	select {
	case <-backend.entered:
	case err := <-first:
		t.Fatalf("first bootstrap ended before entering the controlled step: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("first bootstrap did not enter the controlled step within 30 seconds")
	}
	if _, err := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, input.Now.Add(time.Second)); err == nil {
		close(backend.release)
		t.Fatal("a concurrent second bootstrap acquired the fixed replacement-host lock")
	}
	close(backend.release)
	if err := <-first; err != nil {
		t.Fatalf("first serialized bootstrap did not finish: %v", err)
	}
}

func TestRunPRDReplacementBootstrapRejectsConflictingRetry(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	request := prdReplacementBootstrapRequest(input)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	backend := &prdBootstrapFixtureBackend{plan: plan, absence: input.Absence,
		completed: make(map[string]bool), interruptStep: "configuration", applyCounts: make(map[string]int)}
	root := filepath.Join(t.TempDir(), "bootstrap-ops")
	if _, err := runPRDReplacementBootstrapAtRoot(context.Background(), request, root, backend, input.Now); err == nil {
		t.Fatal("expected interrupted operation")
	}
	conflict := request
	conflict.MacConfiguration = append([]byte(nil), request.MacConfiguration...)
	conflict.MacConfiguration = append(conflict.MacConfiguration, []byte("# changed\n")...)
	if _, err := runPRDReplacementBootstrapAtRoot(context.Background(), conflict, root, backend, input.Now.Add(time.Second)); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("conflicting retry error=%v, want invalid operation", err)
	}
	if backend.applyCounts["database"] != 0 {
		t.Fatal("conflicting retry advanced the partial operation")
	}
}

func TestBootstrapConfigurationFileInstallResumesExactStageAndRejectsConflicts(t *testing.T) {
	contents := []byte("fixture configuration\n")
	want := digestBytes(contents)
	t.Run("resume complete temporary file before rename", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "settings.yaml")
		temporary := filepath.Join(root, ".runner-prd-bootstrap-settings.yaml.tmp")
		if err := os.WriteFile(temporary, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installBootstrapFile(path, contents, 0o600); err != nil {
			t.Fatalf("finish the exact durable temporary write: %v", err)
		}
		if !exactBootstrapFile(path, want, 0o600) {
			t.Fatal("final configuration did not retain the exact content, owner and mode")
		}
		if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary name remains after installation: %v", err)
		}
		if err := installBootstrapFile(path, contents, 0o600); err != nil {
			t.Fatalf("exact configuration retry failed: %v", err)
		}
	})
	t.Run("reject conflicting staged bytes", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "settings.yaml")
		temporary := filepath.Join(root, ".runner-prd-bootstrap-settings.yaml.tmp")
		if err := os.WriteFile(temporary, []byte("different configuration\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installBootstrapFile(path, contents, 0o600); !errors.Is(err, ErrSimpleArtifactConflict) {
			t.Fatalf("conflicting staged content error=%v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("conflicting retry created the live configuration: %v", err)
		}
	})
	t.Run("reject conflicting live bytes without overwrite", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "settings.yaml")
		before := []byte("different live configuration\n")
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installBootstrapFile(path, contents, 0o600); !errors.Is(err, ErrSimpleArtifactConflict) {
			t.Fatalf("conflicting live file error=%v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("conflicting live configuration changed: after=%q err=%v", after, err)
		}
	})
}

func TestBootstrapAuthorizedKeyAppendPreservesUnrelatedKeysAndRetriesExactly(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "authorized_keys")
	original := []byte("ssh-ed25519 AAAAunrelated-key-one fixture-one\nssh-ed25519 AAAAunrelated-key-two fixture-two\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	plan.Namespace.SSHAuthorityPath = path
	backend := localPRDReplacementBootstrapBackend{}
	journal := &PRDReplacementBootstrapJournal{}
	changed, err := backend.PrepareStep(context.Background(), plan, "authorized_key", journal)
	if err != nil || !changed || journal.AuthorizedKeysBefore != digestBytes(original) ||
		!journal.AuthorizedKeysExisted || journal.AuthorizedKeysMode != 0o600 {
		t.Fatalf("journal exact prior key file: changed=%t journal=%+v err=%v", changed, journal, err)
	}
	if err := backend.ApplyStep(context.Background(), plan, "authorized_key", journal); err != nil {
		t.Fatalf("append PRD authorized key: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(after, original) || bytes.Count(after, []byte(plan.AuthorizedKeyLine)) != 1 {
		t.Fatalf("unrelated keys were not preserved or PRD key was not unique: err=%v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys mode changed: info=%v err=%v", info, err)
	}
	changed, err = backend.PrepareStep(context.Background(), plan, "authorized_key", journal)
	if err != nil || changed {
		t.Fatalf("retry preparation changed immutable journal: changed=%t err=%v", changed, err)
	}
	if err := backend.ApplyStep(context.Background(), plan, "authorized_key", journal); err != nil {
		t.Fatalf("exact post-write retry failed: %v", err)
	}
	if err := os.WriteFile(path, append(after, []byte("ssh-ed25519 AAAAunrelated-new-key fixture-three\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.ApplyStep(context.Background(), plan, "authorized_key", journal); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("retry overwrote a concurrent key change: %v", err)
	}
}

func TestPRDReplacementDatabaseStagesAtomicallyAndRetriesExactInstall(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	staging := filepath.Join(state, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(state, "remote.db")
	stagePath := filepath.Join(state, "remote.db.operation.stage")
	markerPath := filepath.Join(staging, "database-operation.json")
	journal := PRDReplacementBootstrapJournal{}
	if err := preparePRDReplacementDatabaseAt(context.Background(), livePath, stagePath, markerPath,
		"0123456789abcdef0123456789abcdef", digestBytes([]byte("database-input")), &journal); err != nil {
		t.Fatalf("prepare isolated empty schema: %v", err)
	}
	if journal.DatabaseStagingPath != stagePath || !bundleDigestPattern.MatchString(journal.DatabaseSHA256) {
		t.Fatalf("database preparation did not bind exact staged bytes: %+v", journal)
	}
	if _, err := os.Lstat(livePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("database preparation wrote the live namespace before its durable digest was journaled")
	}
	if err := verifyPRDReplacementDatabasePreparedAt(context.Background(), livePath, stagePath, journal.DatabaseSHA256); err != nil {
		t.Fatalf("verify staged database before install: %v", err)
	}
	// Simulate interruption after the no-overwrite link but before removal of
	// the stage name. The retry accepts only the same inode with the recorded
	// digest, then completes the unlink and directory sync.
	if err := os.Link(stagePath, livePath); err != nil {
		t.Fatal(err)
	}
	if err := verifyPRDReplacementDatabasePreparedAt(context.Background(), livePath, stagePath, journal.DatabaseSHA256); err != nil {
		t.Fatalf("recognize the exact linked-database interruption: %v", err)
	}
	if err := installPreparedPRDReplacementDatabaseAt(context.Background(), livePath, stagePath, journal.DatabaseSHA256); err != nil {
		t.Fatalf("resume exact database installation: %v", err)
	}
	if err := installPreparedPRDReplacementDatabaseAt(context.Background(), livePath, stagePath, journal.DatabaseSHA256); err != nil {
		t.Fatalf("idempotent exact database install retry: %v", err)
	}
	if err := verifyPRDReplacementDatabaseFile(livePath, journal.DatabaseSHA256, 1); err != nil {
		t.Fatalf("final live DB identity: %v", err)
	}
}

func TestPRDReplacementDatabaseRefusesConflictingLiveFile(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	staging := filepath.Join(state, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(state, "remote.db")
	stagePath := filepath.Join(state, "remote.db.operation.stage")
	markerPath := filepath.Join(staging, "database-operation.json")
	if err := os.WriteFile(livePath, []byte("unrelated database"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	journal := PRDReplacementBootstrapJournal{}
	if err := preparePRDReplacementDatabaseAt(context.Background(), livePath, stagePath, markerPath,
		"0123456789abcdef0123456789abcdef", digestBytes([]byte("database-input")), &journal); err == nil {
		t.Fatal("bootstrap accepted an unrelated database created after the absence observation")
	}
	after, err := os.ReadFile(livePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("conflicting database changed: before=%q after=%q err=%v", before, after, err)
	}
}

func prdReplacementBootstrapRequest(input PRDReplacementBootstrapInput) PRDReplacementBootstrapRequest {
	return PRDReplacementBootstrapRequest{OperationID: input.OperationID, TemplateHostID: input.TemplateHostID,
		TemplateConfiguration: append([]byte(nil), input.TemplateConfiguration...), MacConfiguration: append([]byte(nil), input.MacConfiguration...),
		ReleaseID: input.ReleaseManifest.ReleaseID, ExpectedReleaseSHA256: input.ExpectedReleaseSHA256,
		ApprovalSHA256: strings.Repeat("a", 64), PublisherOwner: "fixture owner", PublisherControl: "fixture publisher hold",
		PublishersPaused: true, NoNewAccepted: true,
		ServerCertificatePEM: append([]byte(nil), input.ServerCertificatePEM...), ServerCSR256: digestBytes(input.ServerCSRPEM),
		ServerCAPEM: append([]byte(nil), input.ServerCAPEM...), ClientCertificatePEM: append([]byte(nil), input.ClientCertificatePEM...),
		ClientCAPEM: append([]byte(nil), input.ClientCAPEM...), ClientPrincipalMap: append([]byte(nil), input.ClientPrincipalMap...),
		DispatcherPublicKey: input.DispatcherPublicKey, SSHHostPublicKey: input.SSHHostPublicKey}
}

func TestBuildPRDReplacementBootstrapPlanRejectsDriftedOrStaleInputs(t *testing.T) {
	base := prdReplacementBootstrapFixture(t)
	tests := []struct {
		name   string
		mutate func(*PRDReplacementBootstrapInput)
	}{
		{name: "expired absence", mutate: func(input *PRDReplacementBootstrapInput) {
			input.Now = input.Now.Add(prdReplacementBootstrapEvidenceMaxAge + time.Second)
		}},
		{name: "Mac route drift", mutate: func(input *PRDReplacementBootstrapInput) {
			input.MacConfiguration = []byte(strings.Replace(string(input.MacConfiguration), "https://129.151.232.40:8443", "https://79.72.94.60:8443", 1))
		}},
		{name: "wrong V2 principal", mutate: func(input *PRDReplacementBootstrapInput) {
			input.ClientPrincipalMap = []byte(strings.Replace(string(input.ClientPrincipalMap), "tomasz.walczuk", "other-user", 1))
		}},
		{name: "wrong release digest", mutate: func(input *PRDReplacementBootstrapInput) { input.ExpectedReleaseSHA256 = strings.Repeat("0", 64) }},
		{name: "wrong sealed manifest source", mutate: func(input *PRDReplacementBootstrapInput) {
			input.ReleaseManifest.SourceRevision = strings.Repeat("a", 40)
		}},
		{name: "wrong template profile", mutate: func(input *PRDReplacementBootstrapInput) { input.TemplateHostID = "factory-sandbox" }},
		{name: "wrong SSH host key type", mutate: func(input *PRDReplacementBootstrapInput) {
			input.SSHHostPublicKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ== fixture"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := BuildPRDReplacementBootstrapPlan(input); err == nil {
				t.Fatal("unsafe or stale bootstrap input was accepted")
			}
		})
	}
}

func TestPRDReplacementBootstrapFilesRejectPathRedirection(t *testing.T) {
	input := prdReplacementBootstrapFixture(t)
	plan, err := BuildPRDReplacementBootstrapPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	plan.LinuxClientCAPath = "/tmp/replacement-client-ca.pem"
	if _, err := PRDReplacementBootstrapFiles(plan); err == nil {
		t.Fatal("bootstrap file plan accepted an arbitrary destination")
	}
}

func prdReplacementBootstrapFixture(t *testing.T) PRDReplacementBootstrapInput {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Nanosecond)
	macConfiguration := []byte(prdReplacementBootstrapMacConfig)
	linuxTemplate := []byte(prdReplacementBootstrapLinuxTemplate)

	bundles := catalogFixtureBundles(t, "replacement-bootstrap")
	manifest, err := NewCatalogReleaseManifest("rr-bootstrap-fixture", bundles)
	if err != nil {
		t.Fatalf("create fixture release manifest: %v", err)
	}
	manifestBytes, err := CatalogReleaseManifestBytes(manifest)
	if err != nil {
		t.Fatalf("serialize canonical fixture release manifest: %v", err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	if err := ValidateCatalogReleaseManifest(manifest); err != nil {
		t.Fatalf("validate fixture release manifest before publish: %v (%+v)", err, manifest)
	}
	if err := ValidateSimpleBundle(bundles["linux-host"]); err != nil {
		t.Fatalf("validate fixture Linux bundle before publish: %v", err)
	}
	if err := validateCatalogLocalBundle(manifest, "linux-host", bundles["linux-host"]); err != nil {
		t.Fatalf("validate fixture Linux manifest/bundle binding before publish: %v", err)
	}
	catalogRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	if _, err := PublishCatalogRelease(catalogRoot, "linux-host", manifest, bundles["linux-host"]); err != nil {
		t.Fatalf("publish fixture release: %v", err)
	}
	release, err := InspectCatalogRelease(catalogRoot, "linux-host", manifest.ReleaseID)
	if err != nil {
		t.Fatalf("inspect fixture release: %v", err)
	}
	if release.Identity.ReleaseSHA256 != hex.EncodeToString(manifestHash[:]) || release.Identity.BundleSHA256 != bundles["linux-host"].Manifest.BundleSHA256 {
		t.Fatal("fixture catalog inspection does not match the canonical release manifest and bundle")
	}

	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	absence := CatalogNamespaceAbsenceReport{
		FormatVersion: 1, HostID: "linux-host", Lane: config.ReleaseLanePRD, SelectedLane: config.ReleaseLaneQAS,
		State: "absent", ServiceRoot: plan.Namespace.ServiceRoot, ServiceRootState: "absent",
		ConfigurationPath: plan.Namespace.ConfigFile, ConfigurationState: "absent",
		DatabasePath: plan.Namespace.Database, DatabaseState: "absent",
		SelectorJournalRoot: filepath.Join(plan.Namespace.Staging, "catalog-operations"), SelectorJournalsState: "absent",
		CohortReceiptRoot: filepath.Join(plan.Namespace.Staging, "catalog-cohorts"), CohortReceiptsState: "absent",
		BackupRoot: plan.Namespace.Backups, BackupState: "absent",
		LifecycleLock: plan.Namespace.LifecycleLock, LifecycleLockState: "absent",
		ServiceUnit: plan.Namespace.RunnerUnit, UnitLoadState: "not-found", UnitFileState: "not-found",
		UnitActiveState: "inactive", UnitSubState: "dead", DirectListenerPort: plan.Namespace.DirectPort,
		DirectListenerAbsent: true, ProcessScanComplete: true, ObservedAtUTC: now.Format(time.RFC3339Nano),
	}
	absence.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(absence)

	serverCA, serverCAKey := prdReplacementTestCA(t, now)
	serverCSR, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate, _, err := SignPRDReplacementServerCSR(serverCSR.RequestPEM, serverCA, serverCAKey, now)
	if err != nil {
		t.Fatal(err)
	}
	clientCA, clientCAKey := prdReplacementTestCA(t, now)
	clientCertificate := prdReplacementLegacyClientCert(t, now, clientCA, clientCAKey, prdReplacementLegacyDirectURI, x509.ExtKeyUsageClientAuth)
	principalMap := []byte("version: 1\nprincipals:\n  - uri_san: " + prdReplacementLegacyDirectURI + "\n    controller_type: direct_mtls\n    controller_id: tomasz.walczuk\n")
	return PRDReplacementBootstrapInput{
		OperationID: "0123456789abcdef0123456789abcdef", TemplateHostID: "sandbox-host",
		TemplateConfiguration: linuxTemplate, MacConfiguration: macConfiguration,
		Absence: absence, ReleaseManifest: manifest, Bundle: bundles["linux-host"],
		ExpectedReleaseSHA256: hex.EncodeToString(manifestHash[:]), ServerCertificatePEM: serverCertificate,
		ServerPrivateKeyPEM: serverCSR.PrivateKeyPEM, ServerCSRPEM: serverCSR.RequestPEM, ServerCAPEM: serverCA,
		ClientCertificatePEM: clientCertificate, ClientCAPEM: clientCA, ClientPrincipalMap: principalMap,
		DispatcherPublicKey: l202PublicKey("replacement-bootstrap-dispatcher"),
		SSHHostPublicKey:    l202PublicKey("replacement-bootstrap-host"), Now: now,
	}
}

const prdReplacementBootstrapMacConfig = `version: 2
mac:
  account: tomasz.walczuk
  service_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
  api_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock"
  locald_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock"
  sqlite: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db"
  workspaces: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/workspaces"
  script_temp_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/tmp/scripts"
  backups: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups"
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: local_user, id: tomasz.walczuk}]
  linux-dev:
    base_system: Ubuntu 22.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: queued_mac, id: tomasz.walczuk}, {type: direct_mtls, id: tomasz.walczuk}]
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  ubuntu-current:
    environment: linux-dev
    execution_target: {kind: remote, profile: linux-host}
remote_hosts:
  linux-host:
    account: ubuntu
    queued_bridge:
      host: 129.151.232.40
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519"
    direct_endpoint:
      name: linux-poc
      url: https://129.151.232.40:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
`

const prdReplacementBootstrapLinuxTemplate = `version: 2
linux:
  account: ubuntu
  service_root: /home/ubuntu/.local/share/remote-session-runner
  sqlite: /home/ubuntu/.local/share/remote-session-runner/state/remote.db
  private_socket: /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock
  workspaces: /home/ubuntu/.local/share/remote-session-runner/workspaces
  script_temp_root: /home/ubuntu/.local/share/remote-session-runner/tmp/scripts
  backups: /home/ubuntu/.local/share/remote-session-runner/backups
  direct_https_bind: 10.0.0.14:8443
  direct_public_endpoint: https://132.226.205.205:8443
  server_cert: /home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
  client_ca: /home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
  client_principal_map: /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
  tls_min_version: "1.3"
  runtime_adapter: linux-host-process
  remote_target_profile: sandbox-host
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
  linux-prd:
    base_system: Ubuntu 22.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: sandbox-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: queued_mac, id: tomasz.walczuk}, {type: direct_mtls, id: tomasz.walczuk}]
`
