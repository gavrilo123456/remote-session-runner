package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const p138AuthorityHostGate = "RSR_P138_AUTHORITY_HOST"
const p138DatabasePageBudget = 512 // limits each disposable fixture to 2 MiB of extra SQLite pages

type p138Runtime struct {
	p020FakeRuntime
	executeEntered chan struct{}
	releaseExecute chan struct{}
	stopSessionErr error
	stopConfirmed  bool
	stopCalls      int
	startHook      func(string)
	startPath      string
}

type p138StreamingRuntime struct {
	*p138Runtime
	callbackSawCancel bool
}

func (r *p138StreamingRuntime) ExecuteCommandStream(ctx context.Context, _ RuntimeCommandRequest, onOutput func(string, []byte) error) (RuntimeCommandResult, error) {
	r.commandCall++
	if r.executeEntered != nil {
		close(r.executeEntered)
	}
	if r.releaseExecute != nil {
		select {
		case <-r.releaseExecute:
		case <-ctx.Done():
			return RuntimeCommandResult{}, ctx.Err()
		}
	}
	if err := onOutput("stdout", []byte("p138-output\n")); err != nil {
		r.callbackSawCancel = r.cancelCall == 1
		return RuntimeCommandResult{}, err
	}
	return RuntimeCommandResult{}, nil
}

func (r *p138Runtime) ExecuteCommand(ctx context.Context, _ RuntimeCommandRequest) (RuntimeCommandResult, error) {
	r.commandCall++
	if r.executeEntered != nil {
		close(r.executeEntered)
	}
	if r.releaseExecute != nil {
		select {
		case <-r.releaseExecute:
		case <-ctx.Done():
			return RuntimeCommandResult{}, ctx.Err()
		}
	}
	return RuntimeCommandResult{Stdout: []byte("p138-output\n")}, nil
}

func (r *p138Runtime) StopSession(context.Context, store.SessionRecord) (bool, error) {
	r.stopCalls++
	return r.stopConfirmed, r.stopSessionErr
}

func TestP138CleanupFailureIsDurableInLifecycleHealthAndAudit(t *testing.T) {
	runtimeAdapter := &p020FakeRuntime{
		generation: "generation-p138-cleanup-failure",
		startErr:   errors.New("injected agent start failure"),
		cleanupErr: errors.New("injected runtime cleanup failure"),
	}
	service, authority, _ := newP020Service(t, runtimeAdapter)
	request := p020Request(t, "session-p138-cleanup-failure", "key-p138-cleanup-failure", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	result, err := service.CreateSession(context.Background(), request)
	if !errors.Is(err, ErrRuntimeUnavailable) || result.Session.State != domain.SessionStateLost {
		t.Fatalf("create result=%+v err=%v, want lost/unavailable", result.Session, err)
	}
	if runtimeAdapter.cleanupCall != 1 {
		t.Fatalf("runtime cleanup calls=%d, want 1", runtimeAdapter.cleanupCall)
	}
	lifecycle, err := authority.ListSessionLifecycle(context.Background(), request.SessionID)
	if err != nil || len(lifecycle) != 2 || lifecycle[1].NewState != domain.SessionStateLost || lifecycle[1].Reason != "runtime_cleanup_unconfirmed" {
		t.Fatalf("cleanup-failure lifecycle=%+v err=%v", lifecycle, err)
	}
	if count, err := authority.CountLiveSessionReservations(context.Background()); err != nil || count != 1 {
		t.Fatalf("live reservations=%d err=%v, want retained 1", count, err)
	}
	metrics, err := authority.ReadOperationalMetrics(context.Background())
	if err != nil || metrics.CleanupFailuresTotal != 1 {
		t.Fatalf("cleanup health metrics=%+v err=%v, want one cleanup failure", metrics, err)
	}
	records, err := authority.ListAuditRecords(context.Background(), 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("audit records=%+v err=%v, want create and cleanup failure", records, err)
	}
	cleanup := records[1]
	if cleanup.Action != audit.ActionRuntimeCleanup || cleanup.Outcome != audit.OutcomeFailed || cleanup.ReasonCode != audit.ReasonRuntimeCleanupUnconfirmed || cleanup.SessionID != request.SessionID {
		t.Fatalf("cleanup audit record=%+v", cleanup)
	}
}

func TestP138AuthorityFailureInjectionHost(t *testing.T) {
	gate := os.Getenv(p138AuthorityHostGate)
	if gate == "" {
		t.Skipf("set %s=macos or linux to run the actual-authority P138 gate", p138AuthorityHostGate)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	wantOS, wantAccount := "darwin", "tomasz.walczuk"
	if gate == "linux" {
		wantOS, wantAccount = "linux", "ubuntu"
	} else if gate != "macos" {
		t.Fatalf("unexpected %s value %q", p138AuthorityHostGate, gate)
	}
	if runtime.GOOS != wantOS || account.Username != wantAccount {
		t.Fatalf("P138 host mismatch: gate=%s OS=%s account=%s, want OS=%s account=%s", gate, runtime.GOOS, account.Username, wantOS, wantAccount)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("P138 authority host=%s OS=%s account=%s", hostname, runtime.GOOS, account.Username)

	t.Run("sqlite full rejects create before runtime", func(t *testing.T) {
		runtimeAdapter := &p138Runtime{}
		service, authority, db := p138NewFullService(t, gate, runtimeAdapter)
		p138FillDatabase(t, db)
		p138FillSessionTable(t, db, gate)
		request := p138CreateRequest(t, gate, "session-p138-full", "key-p138-full")
		if _, err := service.CreateSession(context.Background(), request); !p138IsSQLiteCode(err, sqlite3.SQLITE_FULL) {
			t.Fatalf("create under SQLITE_FULL error=%v, want SQLITE_FULL", err)
		}
		if runtimeAdapter.prepareCall != 0 || runtimeAdapter.startCall != 0 || runtimeAdapter.commandCall != 0 || runtimeAdapter.cleanupCall != 0 {
			t.Fatalf("runtime calls before durable create acceptance: prepare=%d start=%d command=%d cleanup=%d", runtimeAdapter.prepareCall, runtimeAdapter.startCall, runtimeAdapter.commandCall, runtimeAdapter.cleanupCall)
		}
		if count, err := authority.CountLiveSessionReservations(context.Background()); err != nil || count != 0 {
			t.Fatalf("reservations after full create=%d err=%v, want 0", count, err)
		}
		metrics, err := authority.ReadOperationalMetrics(context.Background())
		if err != nil || metrics.StorageErrorsTotal == 0 {
			t.Fatalf("storage health metrics=%+v err=%v, want storage error", metrics, err)
		}
	})

	t.Run("sqlite lock prevents command execution before start commit", func(t *testing.T) {
		runtimeAdapter := &p138Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-p138-busy-start"}}
		service, authority, _, path := p138NewService(t, gate, runtimeAdapter, true)
		session := p138CreateReadySession(t, service, gate, "session-p138-busy-start")
		command := p138QueueCommand(t, service, session, "command-p138-busy-start")
		unlock := p138BeginWriteLock(t, path)
		_, err := service.ResumeCommand(context.Background(), command.CommandID, session.Controller)
		unlock()
		if err == nil || !store.IsSQLiteError(err) {
			t.Fatalf("resume under SQLite lock error=%v, want SQLite busy", err)
		}
		persisted, err := authority.GetCommand(context.Background(), command.CommandID)
		if err != nil || persisted.State != domain.CommandStateQueued {
			t.Fatalf("command after failed start=%+v err=%v, want queued", persisted, err)
		}
		updated, err := authority.GetSession(context.Background(), session.SessionID)
		if err != nil || updated.State != domain.SessionStateReady {
			t.Fatalf("session after failed start=%+v err=%v, want ready", updated, err)
		}
		if count, err := authority.CountLiveCommandSlots(context.Background()); err != nil || count != 0 {
			t.Fatalf("slots after failed start=%d err=%v, want 0", count, err)
		}
		if runtimeAdapter.commandCall != 0 {
			t.Fatalf("runtime command calls=%d, want 0 before committed start", runtimeAdapter.commandCall)
		}
		metrics, err := authority.ReadOperationalMetrics(context.Background())
		if err != nil || metrics.StorageErrorsTotal == 0 {
			t.Fatalf("storage health metrics=%+v err=%v, want storage error", metrics, err)
		}
	})

	t.Run("post-start output failure stops runtime and retains uncertain state", func(t *testing.T) {
		runtimeCore := &p138Runtime{
			p020FakeRuntime: p020FakeRuntime{generation: "generation-p138-busy-output"},
			executeEntered:  make(chan struct{}), releaseExecute: make(chan struct{}), stopConfirmed: true,
		}
		runtimeAdapter := &p138StreamingRuntime{p138Runtime: runtimeCore}
		service, authority, _, path := p138NewService(t, gate, runtimeAdapter, true)
		session := p138CreateReadySession(t, service, gate, "session-p138-busy-output")
		command := p138QueueCommand(t, service, session, "command-p138-busy-output")
		finished := make(chan struct {
			result SubmitCommandResult
			err    error
		}, 1)
		go func() {
			result, err := service.ResumeCommand(context.Background(), command.CommandID, session.Controller)
			finished <- struct {
				result SubmitCommandResult
				err    error
			}{result, err}
		}()
		select {
		case <-runtimeCore.executeEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("runtime did not observe a durably started command")
		}
		unlock := p138BeginWriteLock(t, path)
		close(runtimeCore.releaseExecute)
		var outcome struct {
			result SubmitCommandResult
			err    error
		}
		select {
		case outcome = <-finished:
		case <-time.After(5 * time.Second):
			unlock()
			t.Fatal("resume did not return after bounded SQLite busy waits")
		}
		unlock()
		if outcome.err == nil || !store.IsSQLiteError(outcome.err) {
			t.Fatalf("resume after output persistence failure error=%v, want SQLite error", outcome.err)
		}
		if runtimeCore.stopCalls != 1 || runtimeCore.cancelCall != 1 || !runtimeAdapter.callbackSawCancel {
			t.Fatalf("runtime callback saw cancel=%v cancel calls=%d cleanup calls=%d, want immediate command cancel and session cleanup", runtimeAdapter.callbackSawCancel, runtimeCore.cancelCall, runtimeCore.stopCalls)
		}
		persisted, err := authority.GetCommand(context.Background(), command.CommandID)
		if err != nil || persisted.State != domain.CommandStateRunning {
			t.Fatalf("command after uncommittable failure=%+v err=%v, want conservative running state", persisted, err)
		}
		updated, err := authority.GetSession(context.Background(), session.SessionID)
		if err != nil || updated.State != domain.SessionStateBusy {
			t.Fatalf("session after uncommittable failure=%+v err=%v, want busy", updated, err)
		}
		if count, err := authority.CountLiveCommandSlots(context.Background()); err != nil || count != 1 {
			t.Fatalf("slots after uncommittable failure=%d err=%v, want retained 1", count, err)
		}
		metrics, err := authority.ReadOperationalMetrics(context.Background())
		if err != nil || metrics.StorageErrorsTotal == 0 {
			t.Fatalf("storage health metrics=%+v err=%v, want storage error", metrics, err)
		}
	})

	t.Run("failed runtime cleanup is recorded on host authority", func(t *testing.T) {
		runtimeAdapter := &p020FakeRuntime{
			generation: "generation-p138-host-cleanup",
			startErr:   errors.New("injected start failure"),
			cleanupErr: errors.New("injected cleanup failure"),
		}
		service, authority, _, _ := p138NewService(t, gate, runtimeAdapter, false)
		request := p138CreateRequest(t, gate, "session-p138-host-cleanup", "key-p138-host-cleanup")
		result, err := service.CreateSession(context.Background(), request)
		if !errors.Is(err, ErrRuntimeUnavailable) || result.Session.State != domain.SessionStateLost {
			t.Fatalf("create with failed cleanup=%+v err=%v, want lost", result.Session, err)
		}
		lifecycle, err := authority.ListSessionLifecycle(context.Background(), request.SessionID)
		if err != nil || len(lifecycle) != 2 || lifecycle[1].Reason != "runtime_cleanup_unconfirmed" {
			t.Fatalf("cleanup lifecycle=%+v err=%v", lifecycle, err)
		}
		metrics, err := authority.ReadOperationalMetrics(context.Background())
		if err != nil || metrics.CleanupFailuresTotal != 1 {
			t.Fatalf("cleanup metrics=%+v err=%v", metrics, err)
		}
		records, err := authority.ListAuditRecords(context.Background(), 10)
		if err != nil || len(records) != 2 || records[1].Action != audit.ActionRuntimeCleanup || records[1].Outcome != audit.OutcomeFailed || records[1].SessionID != request.SessionID {
			t.Fatalf("cleanup audit=%+v err=%v", records, err)
		}
	})

	t.Run("ready commit failure cleans started runtime and pins creating session", func(t *testing.T) {
		var unlock func()
		runtimeAdapter := &p138Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-p138-ready-commit"}}
		runtimeAdapter.startHook = func(path string) { unlock = p138BeginWriteLock(t, path) }
		service, authority, _, path := p138NewService(t, gate, runtimeAdapter, true)
		runtimeAdapter.startPath = path
		request := p138CreateRequest(t, gate, "session-p138-ready-commit", "key-p138-ready-commit")
		result, err := service.CreateSession(context.Background(), request)
		if unlock != nil {
			unlock()
		}
		if err == nil || !store.IsSQLiteError(err) {
			t.Fatalf("create with locked ready commit result=%+v err=%v, want SQLite error", result.Session, err)
		}
		if runtimeAdapter.cleanupCall != 1 {
			t.Fatalf("runtime cleanup calls=%d, want 1 after ready commit failure", runtimeAdapter.cleanupCall)
		}
		persisted, err := authority.GetSession(context.Background(), request.SessionID)
		if err != nil || persisted.State != domain.SessionStateCreating {
			t.Fatalf("persisted session=%+v err=%v, want conservative creating state", persisted, err)
		}
		if count, err := authority.CountLiveSessionReservations(context.Background()); err != nil || count != 1 {
			t.Fatalf("session reservations=%d err=%v, want retained 1", count, err)
		}
	})
}

func (r *p138Runtime) StartAgent(_ context.Context, _ RuntimeStartRequest) (RuntimeStarted, error) {
	r.startCall++
	if r.startHook != nil {
		r.startHook(r.startPath)
	}
	if r.startErr != nil {
		return RuntimeStarted{}, r.startErr
	}
	return RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (r *p138Runtime) setP138StartPath(path string) {
	r.startPath = path
}

func p138NewService(t *testing.T, host string, runtimeAdapter SessionRuntime, shortBusyTimeout bool) (*Service, *store.AuthorityStore, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "authority.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if shortBusyTimeout {
		connection, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), "PRAGMA busy_timeout = 100"); err != nil {
			_ = connection.Close()
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	service, authority := p138BuildService(t, host, runtimeAdapter, db, path)
	return service, authority, db, path
}

func p138NewFullService(t *testing.T, host string, runtimeAdapter SessionRuntime) (*Service, *store.AuthorityStore, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "authority.db")
	base, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var pageCount int
	if err := base.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}

	uri := url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("_busy_timeout", strconv.Itoa(int(store.BusyTimeout.Milliseconds())))
	query.Set("_foreign_keys", "on")
	query.Set("_synchronous", "FULL")
	query.Add("_pragma", "max_page_count="+strconv.Itoa(pageCount+p138DatabasePageBudget))
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, authority := p138BuildService(t, host, runtimeAdapter, db, path)
	return service, authority, db
}

func p138BuildService(t *testing.T, host string, runtimeAdapter SessionRuntime, db *sql.DB, path string) (*Service, *store.AuthorityStore) {
	t.Helper()
	if concrete, ok := runtimeAdapter.(interface{ setP138StartPath(string) }); ok {
		concrete.setP138StartPath(path)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	name, class, effectiveAccount, kind, profile, controllerType := p138HostPolicy(t, host)
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(controllerType, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: name, HostClass: class, EffectiveAccount: effectiveAccount,
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtimeAdapter, registry, RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, authority
}

func p138HostPolicy(t *testing.T, host string) (string, string, string, domain.TargetKind, string, domain.ControllerType) {
	t.Helper()
	if host == "macos" {
		return "mac-dev", "macOS workstation", "tomasz.walczuk", domain.TargetKindLocal, "mac-workstation", domain.ControllerTypeLocalUser
	}
	return "linux-dev", "Ubuntu host-process worker", "ubuntu", domain.TargetKindRemote, "linux-host", domain.ControllerTypeDirectMTLS
}

func p138CreateRequest(t *testing.T, host, sessionID, key string) CreateSessionRequest {
	t.Helper()
	environment, _, _, kind, profile, controllerType := p138HostPolicy(t, host)
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(controllerType, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf(`{"operation":"create_session","environment":%q,"session_id":%q}`, environment, sessionID))
	hash, err := domain.HashMutationRequestJSON("create_session", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return CreateSessionRequest{
		SessionID: domain.SessionID(sessionID), IdempotencyKey: key, RequestHash: hash,
		Environment: environment, Target: target, Controller: controller,
		Source: domain.NewEmptySource(),
	}
}

func p138CreateReadySession(t *testing.T, service *Service, host, sessionID string) store.SessionRecord {
	t.Helper()
	request := p138CreateRequest(t, host, sessionID, "key-"+sessionID)
	result, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result.Session
}

func p138QueueCommand(t *testing.T, service *Service, session store.SessionRecord, commandID string) store.CommandRecord {
	t.Helper()
	raw := []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":"printf p138"}`, session.SessionID))
	hash, err := domain.HashMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptCommand(context.Background(), SubmitCommandRequest{
		CommandID: domain.CommandID(commandID), SessionID: session.SessionID, Controller: session.Controller,
		IdempotencyKey: "key-" + commandID, RequestHash: hash, Script: "printf p138",
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted.Command
}

func p138FillDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("CREATE TABLE p138_disk_fill(payload BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	var pageCount, maxPageCount int
	if err := db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA max_page_count").Scan(&maxPageCount); err != nil {
		t.Fatal(err)
	}
	if maxPageCount <= pageCount || maxPageCount > pageCount+p138DatabasePageBudget {
		t.Fatalf("SQLite max page count=%d current=%d, want bounded headroom <=%d pages", maxPageCount, pageCount, p138DatabasePageBudget)
	}
	for index := 0; index < 10000; index++ {
		_, err := db.Exec("INSERT INTO p138_disk_fill(payload) VALUES(zeroblob(3000))")
		if err == nil {
			continue
		}
		if !p138IsSQLiteCode(err, sqlite3.SQLITE_FULL) {
			t.Fatalf("bounded SQLite fill error=%v, want SQLITE_FULL", err)
		}
		var filledPages, freePages int
		if err := db.QueryRow("PRAGMA page_count").Scan(&filledPages); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("PRAGMA freelist_count").Scan(&freePages); err != nil {
			t.Fatal(err)
		}
		if filledPages != maxPageCount || freePages != 0 {
			t.Fatalf("SQLite full fixture pages=%d/%d freelist=%d; require exact cap with no spare pages", filledPages, maxPageCount, freePages)
		}
		return
	}
	t.Fatal("bounded SQLite fixture did not reach SQLITE_FULL within its 2 MiB page budget")
}

func p138FillSessionTable(t *testing.T, db *sql.DB, host string) {
	t.Helper()
	_, _, _, kind, profile, controllerType := p138HostPolicy(t, host)
	controller := string(controllerType)
	const created = "2026-09-29T00:00:00Z"
	for index := 0; index < 10000; index++ {
		sessionID := fmt.Sprintf("p138-fill-session-%05d", index)
		_, err := db.Exec(`INSERT INTO exec_sessions (
			session_id, target_kind, target_profile, environment, controller_type, controller_id,
			source_mode, source_repository_alias, source_requested_revision, source_path,
			source_resolved_revision, runtime_generation, state, command_timeout_ns, idle_timeout_ns,
			session_max_lifetime_ns, output_bytes_per_command, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, 'empty', '', '', '', '', '', 'creating', 1000000000, 1000000000, 1000000000, 1, ?, ?, ?)`,
			sessionID, string(kind), profile, "p138-full-fixture", controller, "tomasz.walczuk", created, created, created)
		if err == nil {
			continue
		}
		if !p138IsSQLiteCode(err, sqlite3.SQLITE_FULL) {
			t.Fatalf("bounded session-table fill error=%v, want SQLITE_FULL", err)
		}
		var freePages int
		if err := db.QueryRow("PRAGMA freelist_count").Scan(&freePages); err != nil {
			t.Fatal(err)
		}
		if freePages != 0 {
			t.Fatalf("session-table SQLITE_FULL left %d spare database pages", freePages)
		}
		return
	}
	t.Fatal("bounded session-table fixture did not reach SQLITE_FULL")
}

func p138IsSQLiteCode(err error, code int) bool {
	var sqliteError *sqlitedriver.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == code
}

func p138BeginWriteLock(t *testing.T, path string) func() {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("_busy_timeout", "100")
	query.Set("_foreign_keys", "on")
	query.Set("_synchronous", "FULL")
	uri.RawQuery = query.Encode()
	lockDB, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	lockDB.SetMaxOpenConns(1)
	connection, err := lockDB.Conn(context.Background())
	if err != nil {
		_ = lockDB.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		_ = connection.Close()
		_ = lockDB.Close()
		t.Fatalf("acquire SQLite fixture writer lock: %v", err)
	}
	return func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		_ = connection.Close()
		_ = lockDB.Close()
	}
}
