package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneadmission"
	"remote-session-runner/src/internal/testfixture"
)

func TestLanesL110AdmissionPersistsAndGatesMutationEntryPoints(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := root.Path() + "/state/l110-admission.db"
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(database, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	legacySession, _, err := authority.AcceptSessionCreate(ctx, p013Acceptance(t, "session-l110", "key-l110-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, legacySession.SessionID, domain.SessionStateReady, "fixture_ready"); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostLinux); err != nil {
		t.Fatal(err)
	}
	state, configured, err := authority.LaneAdmissionSnapshot(ctx)
	if err != nil || !configured || state.AllowsMutation() || state.HighWaterEpoch != 0 {
		t.Fatalf("new gate state=%+v configured=%v err=%v", state, configured, err)
	}

	// The same store boundary serves direct HTTPS and private/queued adapters.
	// Each attempt fails before it can create an authoritative resource.
	if _, _, err := authority.AcceptSessionCreate(ctx, p013Acceptance(t, "session-l110-closed", "key-l110-closed", "linux-dev")); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("session-create ingress error=%v", err)
	}
	commandInput := CommandAcceptance{
		CommandID: "command-l110-closed", SessionID: legacySession.SessionID,
		IdempotencyKey: "key-l110-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-l110","script":"echo closed"}`),
		Script: "echo closed", Timeout: time.Second,
	}
	if _, _, err := authority.AcceptCommand(ctx, commandInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("direct/private submit ingress error=%v", err)
	}
	jobInput := p024Acceptance(t, "job-l110-closed", "session-l110-job", "command-l110-job", "key-l110-job", "echo closed")
	for _, ingress := range []audit.Ingress{audit.IngressDirectMTLS, audit.IngressSSHBridge} {
		if _, _, err := authority.AcceptJob(audit.WithIngress(ctx, ingress), jobInput); !errors.Is(err, ErrLaneAdmissionClosed) {
			t.Fatalf("%s queued run ingress error=%v", ingress, err)
		}
	}

	first := l110Identity("transition-1", "candidate-1", laneadmission.RoleCandidateAdmission, 1)
	if err := authority.PrepareLaneAdmissionCandidate(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := authority.SwitchLaneAdmissionCandidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, first, laneadmission.PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptCommand(ctx, commandInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("transport_ready submit ingress error=%v", err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, first, laneadmission.PhaseOpen); err != nil {
		t.Fatal(err)
	}
	accepted, duplicate, err := authority.AcceptCommand(ctx, commandInput)
	if err != nil || duplicate || accepted.CommandID != commandInput.CommandID {
		t.Fatalf("open candidate submit=%+v duplicate=%v err=%v", accepted, duplicate, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStore(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	state, configured, err = reopened.LaneAdmissionSnapshot(ctx)
	if err != nil || !configured || !state.AllowsMutation() || state.HighWaterEpoch != 1 {
		t.Fatalf("reopened admission state=%+v configured=%v err=%v", state, configured, err)
	}
	if _, _, err := reopened.AcceptCommand(ctx, commandInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("restart reused open admission without revalidation: %v", err)
	}
	if err := reopened.AdvanceLaneAdmissionCandidate(ctx, first, laneadmission.PhaseOpen); err != nil {
		t.Fatalf("idempotent persisted-open observation: %v", err)
	}
	if _, _, err := reopened.AcceptCommand(ctx, commandInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("idempotent open replay bypassed restart revalidation: %v", err)
	}
	if err := reopened.RevalidateLaneAdmissionCandidate(ctx, first); err != nil {
		t.Fatalf("revalidate exact open candidate: %v", err)
	}
	if _, duplicate, err := reopened.AcceptCommand(ctx, commandInput); err != nil || !duplicate {
		t.Fatalf("revalidated retry duplicate=%v err=%v", duplicate, err)
	}

	closure := l110Identity("transition-2", "candidate-1", laneadmission.RoleActiveRuntimeClosure, 2)
	if err := reopened.BeginLaneAdmissionClosure(ctx, closure); err != nil {
		t.Fatal(err)
	}
	if err := reopened.BeginLaneMigrationFence(ctx, "transition-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.AcceptSessionCreate(ctx, p013Acceptance(t, "session-l110-fenced", "key-l110-fenced", "linux-dev")); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("migration-fenced create ingress error=%v", err)
	}
	if err := reopened.AdvanceLaneMigrationFence(ctx, "transition-2", laneadmission.FenceFenced); err != nil {
		t.Fatal(err)
	}
}

func TestLanesL110SubmitAndClosureSerialize(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, root.Path()+"/state/l110-race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := authority.AcceptSessionCreate(ctx, p013Acceptance(t, "session-l110-race", "key-l110-race-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, session.SessionID, domain.SessionStateReady, "fixture_ready"); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostLinux); err != nil {
		t.Fatal(err)
	}
	active := l110Identity("transition-1", "candidate-race", laneadmission.RoleCandidateAdmission, 1)
	if err := authority.PrepareLaneAdmissionCandidate(ctx, active); err != nil {
		t.Fatal(err)
	}
	if err := authority.SwitchLaneAdmissionCandidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, active, laneadmission.PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, active, laneadmission.PhaseOpen); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	var submitErr, closeErr error
	mutationEntered := make(chan struct{})
	mutationRelease := make(chan struct{})
	mutationDone := make(chan error, 1)
	closureStarted := make(chan struct{})
	closureDone := make(chan struct{})
	go func() {
		mutationDone <- authority.WithLaneIngressMutation(ctx, func() error {
			close(mutationEntered)
			<-mutationRelease
			return nil
		})
	}()
	<-mutationEntered
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		input := CommandAcceptance{
			CommandID: "command-l110-race", SessionID: session.SessionID,
			IdempotencyKey: "key-l110-race-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-l110-race","script":"echo race"}`),
			Script: "echo race", Timeout: time.Second,
		}
		_, _, submitErr = authority.AcceptCommand(ctx, input)
	}()
	go func() {
		defer wait.Done()
		<-start
		close(closureStarted)
		closeErr = authority.BeginLaneAdmissionClosure(ctx, l110Identity("transition-2", "candidate-race", laneadmission.RoleActiveRuntimeClosure, 2))
		close(closureDone)
	}()
	close(start)
	<-closureStarted
	select {
	case <-closureDone:
		t.Fatal("closure completed while an admitted lifecycle mutation was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(mutationRelease)
	if err := <-mutationDone; err != nil {
		t.Fatalf("admitted lifecycle mutation failed: %v", err)
	}
	wait.Wait()
	if closeErr != nil {
		t.Fatalf("concurrent closure failed: %v", closeErr)
	}
	if submitErr != nil && !errors.Is(submitErr, ErrLaneAdmissionClosed) {
		t.Fatalf("concurrent submit error=%v", submitErr)
	}
	if _, _, err := authority.AcceptCommand(ctx, CommandAcceptance{
		CommandID: "command-l110-after-close", SessionID: session.SessionID,
		IdempotencyKey: "key-l110-after-close", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-l110-race","script":"echo after"}`),
		Script: "echo after", Timeout: time.Second,
	}); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("post-closure submit error=%v", err)
	}
	if err := authority.WithLaneIngressMutation(ctx, func() error {
		t.Error("closed admission invoked an external lifecycle mutation")
		return nil
	}); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("post-closure lifecycle mutation error=%v", err)
	}
	state, configured, err := authority.LaneAdmissionSnapshot(ctx)
	if err != nil || !configured || state.Active == nil || state.Active.Identity.Role != laneadmission.RoleActiveRuntimeClosure || state.AllowsMutation() {
		t.Fatalf("post-race closure state=%+v configured=%v err=%v", state, configured, err)
	}
	if submitErr == nil {
		if _, err := authority.GetCommand(ctx, "command-l110-race"); err != nil {
			t.Fatalf("accepted-before-closure command missing: %v", err)
		}
	}
}

func l110Identity(transition, candidate string, role laneadmission.Role, epoch uint64) laneadmission.Identity {
	return laneadmission.Identity{
		Lane: laneadmission.LaneDev, HostID: laneadmission.HostLinux,
		TransitionID: transition, CandidateManifestID: candidate,
		ConfigurationDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Role:                role, Epoch: epoch,
	}
}

func TestLanesL110AdmissionCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, root.Path()+"/state/l110-corrupt.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneQAS, laneadmission.HostSandbox); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE lane_ingress_admission SET state_json = ? WHERE singleton = 1`, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptSessionCreate(ctx, p013Acceptance(t, "session-l110-corrupt", "key-l110-corrupt", "linux-dev")); !errors.Is(err, ErrLaneAdmissionCorrupt) {
		t.Fatalf("corrupt admission state allowed ingress: %v", err)
	}
}

func TestLanesL110MigrationFromSchema37(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, root.Path()+"/state/l110-migrate.db")
	if err != nil {
		t.Fatal(err)
	}
	dropBUG025CleanupSchemaForMigrationFixture(t, database)
	defer database.Close()
	if _, err := database.ExecContext(ctx, `DROP TABLE lane_capacity_reservations; DROP TABLE lane_capacity_budgets; DROP TABLE lane_ingress_admission; DELETE FROM runner_schema_migrations WHERE version > 37; PRAGMA user_version = 37`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, root.Path()+"/state/l110-migrate.db")
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	var count int
	if err := migrated.QueryRowContext(ctx, `SELECT count(*) FROM lane_ingress_admission`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy authority admission rows=%d err=%v", count, err)
	}
}

func TestLanesL110SameEpochIdentitySwitchIsRejectedAtStoreBoundary(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, root.Path()+"/state/l110-epoch.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostLinux); err != nil {
		t.Fatal(err)
	}
	first := l110Identity("transition-1", "candidate-1", laneadmission.RoleCandidateAdmission, 1)
	if err := authority.PrepareLaneAdmissionCandidate(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.ConfigurationDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err := authority.PrepareLaneAdmissionCandidate(ctx, changed); !errors.Is(err, laneadmission.ErrEpochIdentitySwitch) {
		t.Fatalf("same-epoch configuration switch error=%v", err)
	}
	state, configured, err := authority.LaneAdmissionSnapshot(ctx)
	if err != nil || !configured || state.Pending == nil || state.Pending.Identity != first {
		t.Fatalf("identity switch changed persisted state=%+v configured=%v err=%v", state, configured, err)
	}
}
