package releaseflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

type fakeBackend struct {
	selected               map[string]bool
	prepareCalls           int
	prepared               bool
	switchCalls            []string
	acceptCalls            int
	releaseCalls           int
	rollbackCalls          []string
	failSwitch             string
	failSwitchAfterEffect  string
	failPrepareAfterEffect bool
	failBranch             bool
	existingHolds          map[string]string
	validatedHolds         int
	supersededPredecessor  string
}

type completedHoldBackend struct {
	*fakeBackend
	holds       map[string]string
	validateErr error
	validated   int
}

func (b *completedHoldBackend) Plan(ctx context.Context, lane string, r Release, approval string) (PromotionPlan, error) {
	plan, err := b.fakeBackend.Plan(ctx, lane, r, approval)
	if err == nil {
		plan.ExistingHolds = b.holds
	}
	return plan, err
}

func (b *completedHoldBackend) ValidateExistingCohortHold(context.Context, PromotionPlan) error {
	b.validated++
	return b.validateErr
}

func (f *fakeBackend) Plan(_ context.Context, lane string, r Release, _ string) (PromotionPlan, error) {
	plan, err := BuildPlan(lane, "all", r)
	plan.ExpectedBranchMarker = r.Source.CapturedTip
	plan.ExpectedBranchMarkerExists = true
	plan.ExistingHolds = f.existingHolds
	return plan, err
}
func (f *fakeBackend) ValidateExistingCohortHold(context.Context, PromotionPlan) error {
	f.validatedHolds++
	return nil
}
func (f *fakeBackend) PrepareCohort(_ context.Context, op Operation) (any, error) {
	f.prepareCalls++
	if op.PreviousCohortOperationID != "" {
		f.supersededPredecessor = op.PreviousCohortOperationID
		f.existingHolds = make(map[string]string, len(HostOrder))
		for _, host := range HostOrder {
			f.existingHolds[host] = op.CohortOperationID
		}
	}
	if f.failPrepareAfterEffect {
		f.prepared = true
		f.failPrepareAfterEffect = false
		return nil, errors.New("injected receipt synchronization interruption after cohort creation")
	}
	f.prepared = true
	return map[string]any{"all_hosts": true, "cohort_operation_id": op.OperationID}, nil
}
func (f *fakeBackend) ObserveHost(_ context.Context, op Operation, host string) (HostObservation, error) {
	selected := f.selected[host]
	obs := HostObservation{HostID: host, Lane: op.Lane, ServiceStopped: true, WorkInventorySHA: digest([]byte("empty-" + host)), WorkInventoryEmpty: true, ConfigurationSHA: digest([]byte("cfg-" + host)), SchemaVersion: 40, BackupVerified: true, CapacityVerified: true, CanSwitch: true,
		PreviousReleaseID: "legacy-" + host, PreviousBundleSHA: digest([]byte("previous-" + host)), PreviousSelectorOperation: "previous-selector-" + host, BackupSHA: digest([]byte("backup-" + host))}
	if selected {
		obs.SelectedReleaseID = op.Release.ID
		obs.SelectedBundleSHA = op.Release.Bundles[host].SHA256
		obs.SelectorOperation = "selector-" + host
		obs.JournalVerified = true
	}
	return obs, nil
}
func (f *fakeBackend) SwitchHost(_ context.Context, op Operation, host string) (HostObservation, error) {
	f.switchCalls = append(f.switchCalls, host)
	if f.failSwitch == host {
		return HostObservation{}, errors.New("injected interruption")
	}
	f.selected[host] = true
	if f.failSwitchAfterEffect == host {
		f.failSwitchAfterEffect = ""
		return HostObservation{}, errors.New("injected interruption after selector effect before coordinator receipt sync")
	}
	return f.ObserveHost(context.Background(), op, host)
}
func (f *fakeBackend) AcceptCohort(_ context.Context, op Operation) (Acceptance, error) {
	f.acceptCalls++
	hashes := map[string]string{}
	for _, h := range HostOrder {
		hashes[h] = op.Release.Bundles[h].SHA256
	}
	return Acceptance{Profile: op.Profile, ReleaseID: op.Release.ID, ReleaseSHA256: op.Release.Digest, BundleHashes: hashes, Passed: true, Gates: passedGates(), EvidenceSHA: digest([]byte("acceptance"))}, nil
}
func (f *fakeBackend) ReleaseCohort(context.Context, Operation) (any, error) {
	f.releaseCalls++
	return map[string]bool{"all_admissions_opened": true}, nil
}
func (f *fakeBackend) RollbackHost(_ context.Context, op Operation, host string) (HostObservation, error) {
	f.rollbackCalls = append(f.rollbackCalls, host)
	f.selected[host] = false
	hp := op.Hosts[hostIndex(host)]
	return HostObservation{HostID: host, Lane: op.Lane, SelectedReleaseID: hp.PreviousReleaseID, SelectedBundleSHA: hp.PreviousBundleSHA256, SelectorOperation: "rollback-" + host, JournalVerified: true}, nil
}
func (f *fakeBackend) UpdateSourceBranch(context.Context, string, string, string) error {
	if f.failBranch {
		f.failBranch = false
		return errors.New("mirror delayed")
	}
	return nil
}

func testRelease() Release {
	commit := strings.Repeat("a", 40)
	bundles := map[string]Bundle{}
	list := []Bundle{}
	for _, host := range HostOrder {
		goos, goarch := "linux", "amd64"
		if host == "sandbox-host" {
			goarch = "arm64"
		}
		if host == "mac-control-plane" {
			goos, goarch = "darwin", "arm64"
		}
		b := Bundle{HostID: host, GOOS: goos, GOARCH: goarch, Toolchain: "go1.27.1", SourceRevision: commit, DatabaseSchemaMax: 40, Components: testComponents(host), SHA256: digest([]byte("bundle-" + host)), ManifestSHA: digest([]byte("manifest-" + host)), Bytes: 1000}
		bundles[host] = b
		list = append(list, b)
	}
	releaseDigest, err := CanonicalReleaseDigest("release-abc", commit, "go1.27.1", list)
	if err != nil {
		panic(err)
	}
	release := Release{ID: "release-abc", Digest: releaseDigest, Source: Source{Branch: "dev", CapturedTip: commit, Commit: commit, PrimaryRef: commit, MirrorRef: commit}, Toolchain: "go1.27.1", Bundles: bundles, PublishedHosts: append([]string(nil), HostOrder...), Sealed: true}
	release.ProvenanceSHA = digest(testProvenanceBytes(release))
	return release
}

func testProvenanceBytes(release Release) []byte {
	hosts := make([]Bundle, 0, len(HostOrder))
	settings := make([]HostBuildSettings, 0, len(HostOrder))
	for _, host := range HostOrderByName {
		b := release.Bundles[host]
		hosts = append(hosts, b)
		settings = append(settings, HostBuildSettings{HostID: host, ToolchainBinarySHA: digest([]byte("toolchain:" + host)), GOOS: b.GOOS, GOARCH: b.GOARCH, CGOEnabled: b.GOOS == "darwin", Flags: hostBuildFlags(b.SourceRevision)})
	}
	p := Provenance{FormatVersion: 1, BuildID: strings.Repeat("c", 32), ReleaseID: release.ID, ReleaseSHA256: release.Digest, Source: release.Source,
		Toolchain: release.Toolchain, ControllerRevision: strings.Repeat("d", 40), Hosts: hosts, BuildSettings: settings}
	body, _ := json.MarshalIndent(p, "", "  ")
	return append(body, '\n')
}

func saveTestRelease(t *testing.T, store *Store, release Release) {
	t.Helper()
	if err := store.SaveReleaseProvenance(release.ID, testProvenanceBytes(release)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRelease(release); err != nil {
		t.Fatal(err)
	}
}

func passedGates() map[string]string {
	out := make(map[string]string, len(RequiredAcceptanceGates))
	for _, gate := range RequiredAcceptanceGates {
		out[gate] = "passed"
	}
	return out
}

func saveTestAcceptance(t *testing.T, store *Store, lane string, release Release, now time.Time) {
	t.Helper()
	hashes := map[string]string{}
	for _, host := range HostOrder {
		hashes[host] = release.Bundles[host].SHA256
	}
	if err := store.SaveAcceptance(AcceptanceRecord{FormatVersion: FormatVersion, Lane: lane, OperationID: strings.Repeat("9", 32), Profile: "profile-v1", ReleaseID: release.ID,
		ReleaseSHA256: release.Digest, BundleHashes: hashes, EvidenceSHA: digest([]byte("prior acceptance")), Passed: true, Gates: passedGates(), AcceptedUTC: now.UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
}

func testComponents(host string) []laneinstall.SimpleBundleComponent {
	names := []string{"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"}
	if host == "mac-control-plane" {
		names = []string{"runner", "runner-local", "runner-locald", "runner-log-capture"}
	}
	components := make([]laneinstall.SimpleBundleComponent, 0, len(names))
	for _, name := range names {
		components = append(components, laneinstall.SimpleBundleComponent{Name: name, SHA256: digest([]byte(host + ":" + name)), Bytes: 100})
	}
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	return components
}

func TestPromotePersistsAndSupersedesOneValidatedCompletedHold(t *testing.T) {
	store := testStore(t)
	release := testRelease()
	saveTestRelease(t, store, release)
	predecessor := strings.Repeat("c", 32)
	backend := &completedHoldBackend{fakeBackend: &fakeBackend{selected: map[string]bool{}}, holds: map[string]string{}}
	for _, host := range HostOrder {
		backend.holds[host] = predecessor
	}
	operationID := strings.Repeat("d", 32)
	result, err := Promote(context.Background(), store, backend, PromoteRequest{Lane: "dev", Target: "all", ReleaseID: release.ID,
		OperationID: operationID, Profile: "profile-v1"}, time.Now)
	if err != nil {
		t.Fatalf("promotion with validated completed predecessor failed: %v", err)
	}
	if backend.validated != 1 || backend.prepareCalls != 1 || result.Operation.PreviousCohortOperationID != predecessor ||
		result.Operation.Stage != StageComplete {
		t.Fatalf("completed predecessor handover was not durable: validations=%d prepared=%d previous=%s stage=%s",
			backend.validated, backend.prepareCalls, result.Operation.PreviousCohortOperationID, result.Operation.Stage)
	}
	loaded, err := store.LoadOperation("dev", operationID)
	if err != nil || loaded.PreviousCohortOperationID != predecessor {
		t.Fatalf("predecessor identity did not survive operation reload: previous=%s err=%v", loaded.PreviousCohortOperationID, err)
	}
}

func TestPromoteRejectsPartialAmbiguousOrUnverifiedExistingHoldsBeforeEffects(t *testing.T) {
	for name, tc := range map[string]struct {
		holds       map[string]string
		validateErr error
	}{
		"partial host set": {holds: map[string]string{"linux-host": strings.Repeat("c", 32)}},
		"different operations": {holds: map[string]string{
			"linux-host": strings.Repeat("c", 32), "sandbox-host": strings.Repeat("c", 32),
			"factory-sandbox": strings.Repeat("e", 32), "mac-control-plane": strings.Repeat("c", 32),
		}},
		"failed predecessor validation": {holds: func() map[string]string {
			holds := map[string]string{}
			for _, host := range HostOrder {
				holds[host] = strings.Repeat("c", 32)
			}
			return holds
		}(), validateErr: ErrOperationCorrupted},
	} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			release := testRelease()
			saveTestRelease(t, store, release)
			backend := &completedHoldBackend{fakeBackend: &fakeBackend{selected: map[string]bool{}}, holds: tc.holds, validateErr: tc.validateErr}
			operationID := strings.Repeat("d", 32)
			_, err := Promote(context.Background(), store, backend, PromoteRequest{Lane: "dev", Target: "all", ReleaseID: release.ID,
				OperationID: operationID, Profile: "profile-v1"}, time.Now)
			if !errors.Is(err, ErrAdmissionHeld) && !errors.Is(err, ErrOperationCorrupted) {
				t.Fatalf("unsafe existing hold was not rejected: %v", err)
			}
			if backend.prepareCalls != 0 {
				t.Fatalf("preparation ran before predecessor proof: %d", backend.prepareCalls)
			}
			if _, loadErr := store.LoadOperation("dev", operationID); loadErr == nil {
				t.Fatal("blocked predecessor validation created a promotion operation")
			}
		})
	}
}

func TestPromoteNamedHostsCommonPreparationAndResume(t *testing.T) {
	store := testStore(t)
	release := testRelease()
	saveTestRelease(t, store, release)
	backend := &fakeBackend{selected: map[string]bool{}, failSwitch: "linux-host"}
	now := func() time.Time { return time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC) }
	req := PromoteRequest{Lane: "dev", Target: "linux-host", ReleaseID: release.ID, OperationID: strings.Repeat("1", 32), Profile: "profile-v1"}
	if _, err := Promote(context.Background(), store, backend, req, now); err == nil {
		t.Fatal("injected interruption was swallowed")
	}
	op, err := store.LoadOperation("dev", req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Stage != StageBlocked || backend.prepareCalls != 1 {
		t.Fatalf("stage=%s prepares=%d", op.Stage, backend.prepareCalls)
	}
	backend.failSwitch = ""
	result, err := Promote(context.Background(), store, backend, PromoteRequest{Lane: "dev", ResumeID: req.OperationID, Target: "linux-host"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "cohort_pending" || backend.prepareCalls != 1 || len(backend.switchCalls) != 2 || backend.switchCalls[0] != "linux-host" || backend.switchCalls[1] != "linux-host" {
		t.Fatalf("result=%+v backend=%+v", result, backend)
	}
	if result.Operation.Hosts[0].ObservedBundleSHA != release.Bundles["linux-host"].SHA256 {
		t.Fatal("resume did not verify the installed bundle")
	}
	result, err = Promote(context.Background(), store, backend, PromoteRequest{Lane: "dev", ResumeID: req.OperationID, Target: "all"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "completed" || len(backend.switchCalls) != 5 || backend.acceptCalls != 1 || backend.releaseCalls != 1 {
		t.Fatalf("result=%+v backend=%+v", result, backend)
	}
}

func TestPromoteRejectsOutOfOrderNamedHost(t *testing.T) {
	store := testStore(t)
	r := testRelease()
	saveTestRelease(t, store, r)
	b := &fakeBackend{selected: map[string]bool{}}
	_, err := Promote(context.Background(), store, b, PromoteRequest{Lane: "dev", Target: "sandbox-host", ReleaseID: r.ID, OperationID: strings.Repeat("2", 32), Profile: "p1"}, time.Now)
	if !errors.Is(err, ErrHostOutOfOrder) {
		t.Fatalf("got %v", err)
	}
	if b.prepareCalls != 1 || len(b.switchCalls) != 0 {
		t.Fatalf("unexpected effects: %+v", b)
	}
}

func TestPromoteQASRequiresSameReleaseAcceptanceAndBranchFailureIsSeparate(t *testing.T) {
	store := testStore(t)
	r := testRelease()
	saveTestRelease(t, store, r)
	saveTestAcceptance(t, store, "dev", r, time.Now())
	b := &fakeBackend{selected: map[string]bool{}, failBranch: true}
	res, err := Promote(context.Background(), store, b, PromoteRequest{Lane: "qas", Target: "all", ReleaseID: r.ID, OperationID: strings.Repeat("3", 32), Profile: "profile-v1"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "runtime_accepted_branch_sync_pending" || res.Operation.BranchSync != "pending" || b.releaseCalls != 1 {
		t.Fatalf("%+v", res)
	}
	b.failBranch = false
	res, err = Promote(context.Background(), store, b, PromoteRequest{Lane: "qas", ResumeID: res.Operation.OperationID}, time.Now)
	if err != nil || res.Outcome != "completed" || b.switchCalls != nil && len(b.switchCalls) != 4 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestPRDApprovalAndRollbackAreBounded(t *testing.T) {
	r := testRelease()
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	predecessors := map[string]string{}
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("previous:" + host))
	}
	a := Approval{FormatVersion: 1, ReleaseID: r.ID, ReleaseSHA256: r.Digest, HostSet: append([]string(nil), HostOrder...), WindowStart: now.Add(-time.Minute).Format(time.RFC3339), WindowEnd: now.Add(time.Hour).Format(time.RFC3339), Owner: "owner", Scope: "exact release migration", PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: digest([]byte("backup-recovery-package"))}
	if err := ValidateApproval(a, r, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidateApproval(a, r, now.Add(2*time.Hour)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expired approval accepted: %v", err)
	}
	a.ReleaseSHA256 = digest([]byte("wrong"))
	if err := ValidateApproval(a, r, now); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong digest accepted: %v", err)
	}
}

func TestSupportedRollbackUsesReverseSwitchOrder(t *testing.T) {
	store := testStore(t)
	r := testRelease()
	saveTestRelease(t, store, r)
	b := &fakeBackend{selected: map[string]bool{}}
	now := time.Now
	res, err := Promote(context.Background(), store, b, PromoteRequest{Lane: "dev", Target: "all", ReleaseID: r.ID, OperationID: strings.Repeat("5", 32), Profile: "p1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed" {
		t.Fatalf("%+v", res)
	}
	op, err := Rollback(context.Background(), store, b, "dev", res.Operation.OperationID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mac-control-plane", "factory-sandbox", "sandbox-host", "linux-host"}
	if !reflect.DeepEqual(b.rollbackCalls, want) {
		t.Fatalf("rollback order %v, want %v", b.rollbackCalls, want)
	}
	if op.Stage != StageRollbackComplete || b.releaseCalls != 2 {
		t.Fatalf("stage=%s releases=%d", op.Stage, b.releaseCalls)
	}
}

func TestConcurrentLaneOperationLockRefusesSecondCoordinator(t *testing.T) {
	store := testStore(t)
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- store.WithLaneLock("qas", func() error { close(locked); <-release; return nil }) }()
	<-locked
	if err := store.WithLaneLock("qas", func() error { return nil }); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("second coordinator error=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEventTamperingRejected(t *testing.T) {
	op, err := NewOperation(strings.Repeat("4", 32), "dev", "all", testRelease(), "p1", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(&op, "plan", "", false, "done", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	op.Events[0].Outcome = "forged"
	if err := ValidateOperation(op); !errors.Is(err, ErrOperationCorrupted) {
		t.Fatalf("tampering accepted: %v", err)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "operations")
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreRejectsSymlinkedOperationDirectory(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "operations")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); err == nil {
		t.Fatal("symlinked workflow root accepted")
	}
}

func TestDigestFixtureIsStable(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	if digest([]byte("x")) != hex.EncodeToString(sum[:]) {
		t.Fatal("digest mismatch")
	}
}
