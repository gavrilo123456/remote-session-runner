package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

type queueClaimTestSubmission struct {
	sessionID domain.SessionID
	commandID domain.CommandID
	accept    SessionCreateAcceptance
	command   CommandAcceptance
}

func TestQueueClaimDiagnosticsClassifySQLiteBusyAtTransactionBegin(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "queue-claim-busy.db")
	claimDB, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claimDB.Close() })
	claimDB.SetMaxOpenConns(1)
	if _, err := claimDB.ExecContext(context.Background(), "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	claimer, err := NewAuthorityStore(claimDB)
	if err != nil {
		t.Fatal(err)
	}

	blockerDB, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockerDB.Close() })
	blockerDB.SetMaxOpenConns(1)
	blocker, err := blockerDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	if _, err := blocker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = blocker.ExecContext(context.Background(), "ROLLBACK") })

	if _, err := claimer.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit); err == nil {
		t.Fatal("claim succeeded while the independent WAL connection held the writer reservation")
	} else {
		diagnostic := QueueClaimDiagnostics(err)
		if diagnostic.Stage != "begin_transaction" || diagnostic.Category != "sqlite_lock" || !diagnostic.HasSQLiteCode ||
			diagnostic.SQLitePrimaryCode != sqlite3.SQLITE_BUSY || diagnostic.SQLiteExtendedCode != sqlite3.SQLITE_BUSY {
			t.Fatalf("SQLite contention diagnostic=%+v err=%v, want begin_transaction SQLITE_BUSY", diagnostic, err)
		}
	}
	metrics, err := claimer.ReadOperationalMetrics(context.Background())
	if err != nil || metrics.StorageErrorsTotal != 1 {
		t.Fatalf("claim storage error count=%d err=%v, want 1", metrics.StorageErrorsTotal, err)
	}
}

func TestQueueClaimDiagnosticsDistinguishConstraintAndRollbackClaim(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 18, 10, 5, 0, 0, time.UTC)}
	claimer := newP019Store(t, clock)
	sessionID := p019ReadySession(t, claimer, "session-claim-constraint", "key-claim-constraint-session")
	command := p019Command(t, claimer, sessionID, "command-claim-constraint", "key-claim-constraint-command")
	if _, err := claimer.db.ExecContext(context.Background(), `
INSERT INTO exec_command_slots(command_id, host_key, reserved_at) VALUES(?, ?, ?)
`, string(command.CommandID), schedulerHostKey, formatStoredTime(clock.Now())); err != nil {
		t.Fatal(err)
	}

	_, err := claimer.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit)
	if err == nil {
		t.Fatal("claim succeeded despite the duplicate durable command-slot identity")
	}
	diagnostic := QueueClaimDiagnostics(err)
	if diagnostic.Stage != "reserve_command_slot" || diagnostic.Category != "sqlite_constraint" || !diagnostic.HasSQLiteCode ||
		diagnostic.SQLitePrimaryCode != sqlite3.SQLITE_CONSTRAINT || diagnostic.SQLiteExtendedCode&0xff != sqlite3.SQLITE_CONSTRAINT {
		t.Fatalf("constraint diagnostic=%+v err=%v, want reserve_command_slot SQLITE_CONSTRAINT", diagnostic, err)
	}
	stored, err := claimer.GetCommand(context.Background(), command.CommandID)
	if err != nil || stored.State != domain.CommandStateQueued {
		t.Fatalf("failed claim command state=%s err=%v, want queued after rollback", stored.State, err)
	}
	var startedEvents, slots int
	if err := claimer.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM exec_command_events WHERE command_id = ? AND event_type = 'command_started'
`, string(command.CommandID)).Scan(&startedEvents); err != nil {
		t.Fatal(err)
	}
	if err := claimer.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM exec_command_slots WHERE command_id = ?`, string(command.CommandID)).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if startedEvents != 0 || slots != 1 {
		t.Fatalf("failed claim aftermath started_events=%d slots=%d, want 0 and original reservation only", startedEvents, slots)
	}
}

func TestQueueClaimDiagnosticsDistinguishLogicalSchedulerState(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}
	claimer := newP019Store(t, clock)
	sessionID := p019ReadySession(t, claimer, "session-claim-diagnostics", "key-claim-diagnostics-session")
	command := p019Command(t, claimer, sessionID, "command-claim-diagnostics", "key-claim-diagnostics-command")
	if _, err := claimer.db.ExecContext(context.Background(), "UPDATE exec_commands SET created_at = ? WHERE command_id = ?", "invalid-time", string(command.CommandID)); err != nil {
		t.Fatal(err)
	}

	_, err := claimer.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit)
	if !errors.Is(err, ErrCommandOrderCorrupt) {
		t.Fatalf("claim error=%v, want ErrCommandOrderCorrupt", err)
	}
	diagnostic := QueueClaimDiagnostics(err)
	if diagnostic.Stage != "validate_scheduler_order_timestamp" || diagnostic.Category != "logical" || diagnostic.HasSQLiteCode || diagnostic.HasOSErrorCode {
		t.Fatalf("logical scheduler diagnostic=%+v, want state classification without storage codes", diagnostic)
	}
}

func TestQueueClaimDiagnosticsIncludeTypedOSErrorCodeOnlyWhenPresent(t *testing.T) {
	diagnostic := QueueClaimDiagnostics(fmt.Errorf("filesystem operation: %w", syscall.EACCES))
	if diagnostic.Category != "os" || !diagnostic.HasOSErrorCode || diagnostic.OSErrorCode != int(syscall.EACCES) || diagnostic.HasSQLiteCode {
		t.Fatalf("typed OS error diagnostic=%+v, want OS category and EACCES code", diagnostic)
	}
	logical := QueueClaimDiagnostics(errors.New("logical validation failure"))
	if logical.HasOSErrorCode || logical.HasSQLiteCode || logical.Category != "logical" {
		t.Fatalf("logical diagnostic=%+v, want no typed error codes", logical)
	}
}

func TestSQLiteFailureCategorySeparatesLockConstraintIOAndOtherStorageCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
		want string
	}{
		{name: "busy", code: sqlite3.SQLITE_BUSY, want: "sqlite_lock"},
		{name: "locked", code: sqlite3.SQLITE_LOCKED, want: "sqlite_lock"},
		{name: "constraint", code: sqlite3.SQLITE_CONSTRAINT, want: "sqlite_constraint"},
		{name: "io", code: sqlite3.SQLITE_IOERR, want: "sqlite_io"},
		{name: "capacity", code: sqlite3.SQLITE_FULL, want: "sqlite_capacity"},
		{name: "other", code: sqlite3.SQLITE_ERROR, want: "sqlite"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sqliteFailureCategory(test.code); got != test.want {
				t.Fatalf("sqliteFailureCategory(%d)=%q, want %q", test.code, got, test.want)
			}
		})
	}
}

func TestIndependentStoresClaimExactlyOnceDuringHealthAndSubmissionWrites(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "queue-claim-concurrent.db")
	apiDB, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiDB.Close() })
	localdDB, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localdDB.Close() })
	api, err := NewAuthorityStore(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	locald, err := NewAuthorityStore(localdDB)
	if err != nil {
		t.Fatal(err)
	}

	const initialCount = 8
	const concurrentSubmissions = 12
	const total = initialCount + concurrentSubmissions
	submissions := make([]queueClaimTestSubmission, total)
	for i := range submissions {
		sessionID := domain.SessionID(fmt.Sprintf("session-multi-project-%02d", i))
		commandID := domain.CommandID(fmt.Sprintf("command-multi-project-%02d", i))
		sessionKey := fmt.Sprintf("key-multi-project-session-%02d", i)
		commandKey := fmt.Sprintf("key-multi-project-command-%02d", i)
		environment := []string{"mac-dev", "linux-dev", "sandbox-dev", "factory-sandbox-dev"}[i%4]
		script := "printf " + string(commandID)
		submissions[i] = queueClaimTestSubmission{
			sessionID: sessionID,
			commandID: commandID,
			accept:    p013Acceptance(t, string(sessionID), sessionKey, environment),
			command: CommandAcceptance{
				CommandID:      commandID,
				SessionID:      sessionID,
				IdempotencyKey: commandKey,
				RequestHash:    p014Hash(t, fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":%q}`, sessionID, script)),
				Script:         script,
				Timeout:        time.Minute,
			},
		}
	}

	for i := 0; i < initialCount; i++ {
		if err := acceptQueueClaimTestSubmission(context.Background(), api, submissions[i]); err != nil {
			t.Fatalf("seed submission %d: %v", i, err)
		}
	}

	start := make(chan struct{})
	producerDone := make(chan struct{})
	producerErrors := make(chan error, 1)
	go func() {
		defer close(producerDone)
		<-start
		for i := initialCount; i < len(submissions); i++ {
			if err := acceptQueueClaimTestSubmission(context.Background(), api, submissions[i]); err != nil {
				producerErrors <- fmt.Errorf("submission %d: %w", i, err)
				return
			}
		}
	}()

	var healthWait sync.WaitGroup
	healthErrors := make(chan error, 2)
	for _, component := range []string{"mac_ingress", "mac_local_executor"} {
		component := component
		healthWait.Add(1)
		go func() {
			defer healthWait.Done()
			<-start
			for i := 0; i < 40; i++ {
				if err := api.CheckWritable(context.Background(), component); err != nil {
					healthErrors <- fmt.Errorf("health write %s: %w", component, err)
					return
				}
			}
		}()
	}

	start <- struct{}{}
	start <- struct{}{}
	start <- struct{}{}

	claimed := make(map[domain.CommandID]struct{}, total)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for len(claimed) < total {
		select {
		case err := <-producerErrors:
			t.Fatal(err)
		default:
		}
		select {
		case err := <-healthErrors:
			t.Fatal(err)
		default:
		}
		select {
		case <-deadline.C:
			t.Fatalf("claim progress timed out after %d/%d commands", len(claimed), total)
		default:
		}
		command, err := locald.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit)
		switch {
		case errors.Is(err, ErrCommandNotEligible):
			time.Sleep(time.Millisecond)
			continue
		case err != nil:
			t.Fatalf("claim %d failed: %v diagnostic=%+v", len(claimed), err, QueueClaimDiagnostics(err))
		}
		if _, duplicate := claimed[command.CommandID]; duplicate {
			t.Fatalf("command %s was claimed twice", command.CommandID)
		}
		claimed[command.CommandID] = struct{}{}
		if _, err := locald.TransitionCommand(context.Background(), CommandTransition{
			CommandID: command.CommandID, NextState: domain.CommandStateSucceeded,
			ExitCode: intPointer(0), OutputComplete: true,
		}); err != nil {
			t.Fatalf("complete command %s: %v", command.CommandID, err)
		}
		if _, err := locald.TransitionSession(context.Background(), command.SessionID, domain.SessionStateReady, "command_complete"); err != nil {
			t.Fatalf("return session %s to ready: %v", command.SessionID, err)
		}
		if err := locald.ConfirmCommandSlotRelease(context.Background(), command.CommandID); err != nil {
			t.Fatalf("release command slot %s: %v", command.CommandID, err)
		}
	}
	<-producerDone
	healthWait.Wait()
	close(producerErrors)
	close(healthErrors)
	for err := range producerErrors {
		t.Fatal(err)
	}
	for err := range healthErrors {
		t.Fatal(err)
	}

	for i, submission := range submissions {
		command, err := locald.GetCommand(context.Background(), submission.commandID)
		if err != nil || command.State != domain.CommandStateSucceeded {
			t.Fatalf("command %d final state=%+v err=%v, want succeeded", i, command, err)
		}
	}
	for i := 0; i < 20; i++ {
		if _, err := locald.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit); !errors.Is(err, ErrCommandNotEligible) {
			t.Fatalf("empty queue claim %d error=%v, want ErrCommandNotEligible", i, err)
		}
	}
	apiMetrics, err := api.ReadOperationalMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	localdMetrics, err := locald.ReadOperationalMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := apiMetrics.StorageErrorsTotal + localdMetrics.StorageErrorsTotal; got != 0 {
		t.Fatalf("normal concurrent workload recorded %d SQLite storage errors, want 0", got)
	}
}

func TestQueueClaimConcurrentAPIProcessHelper(t *testing.T) {
	databasePath := os.Getenv("RSR_QUEUE_CLAIM_HELPER_DATABASE")
	if databasePath == "" {
		return
	}
	apiDB, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiDB.Close() })
	api, err := NewAuthorityStore(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("RSR_QUEUE_CLAIM_HELPER_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitQueueClaimHelperMarker(t, os.Getenv("RSR_QUEUE_CLAIM_HELPER_START"))

	const total = 20
	for i := 0; i < total; i++ {
		if err := acceptQueueClaimTestSubmission(t.Context(), api, newQueueClaimTestSubmission(t, i)); err != nil {
			t.Fatalf("API process submission %d: %v", i, err)
		}
		if err := api.CheckWritable(t.Context(), "mac_ingress"); err != nil {
			t.Fatalf("API process health write %d: %v", i, err)
		}
	}

	finishPath := os.Getenv("RSR_QUEUE_CLAIM_HELPER_FINISH")
	for {
		if _, err := os.Stat(finishPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect API process finish marker: %v", err)
		}
		if err := api.CheckWritable(t.Context(), "mac_ingress"); err != nil {
			t.Fatalf("API process concurrent health write: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	metrics, err := api.ReadOperationalMetrics(t.Context())
	if err != nil || metrics.StorageErrorsTotal != 0 {
		t.Fatalf("API process storage error count=%d err=%v, want 0", metrics.StorageErrorsTotal, err)
	}
}

func TestIndependentProcessesClaimExactlyOnceDuringMultiProjectWrites(t *testing.T) {
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "queue-claim-processes.db")
	initialDB, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialDB.Close(); err != nil {
		t.Fatal(err)
	}
	localdDB, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localdDB.Close() })
	locald, err := NewAuthorityStore(localdDB)
	if err != nil {
		t.Fatal(err)
	}

	readyPath := filepath.Join(root.Path(), "api.ready")
	startPath := filepath.Join(root.Path(), "api.start")
	finishPath := filepath.Join(root.Path(), "api.finish")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestQueueClaimConcurrentAPIProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		"RSR_QUEUE_CLAIM_HELPER_DATABASE="+databasePath,
		"RSR_QUEUE_CLAIM_HELPER_READY="+readyPath,
		"RSR_QUEUE_CLAIM_HELPER_START="+startPath,
		"RSR_QUEUE_CLAIM_HELPER_FINISH="+finishPath,
	)
	var processOutput bytes.Buffer
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	var processErr error
	go func() {
		processErr = command.Wait()
		close(processDone)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(finishPath, []byte("finish"), 0o600)
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-processDone
		}
	})
	waitForQueueClaimHelperMarker(t, readyPath, processDone, func() error { return processErr }, &processOutput)

	const total = 20
	var healthWait sync.WaitGroup
	healthErrors := make(chan error, 1)
	healthWait.Add(1)
	go func() {
		defer healthWait.Done()
		for i := 0; i < 80; i++ {
			if err := locald.CheckWritable(t.Context(), "mac_local_executor"); err != nil {
				healthErrors <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	writeQueueClaimHelperMarker(t, startPath)

	claimed := make(map[domain.CommandID]struct{}, total)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for len(claimed) < total {
		select {
		case err := <-healthErrors:
			t.Fatalf("locald process health write: %v", err)
		case <-processDone:
			t.Fatalf("API process exited before the locald process drained its queue: %v; output=%q", processErr, processOutput.String())
		case <-deadline.C:
			t.Fatalf("locald process claimed %d/%d commands before deadline", len(claimed), total)
		default:
		}
		command, err := locald.StartNextDispatchableCommand(t.Context(), DefaultRunningCommandLimit)
		switch {
		case errors.Is(err, ErrCommandNotEligible):
			time.Sleep(time.Millisecond)
			continue
		case err != nil:
			t.Fatalf("locald process claim %d failed: %v diagnostic=%+v", len(claimed), err, QueueClaimDiagnostics(err))
		}
		if _, duplicate := claimed[command.CommandID]; duplicate {
			t.Fatalf("locald process claimed command %s twice", command.CommandID)
		}
		claimed[command.CommandID] = struct{}{}
		if _, err := locald.TransitionCommand(t.Context(), CommandTransition{
			CommandID: command.CommandID, NextState: domain.CommandStateSucceeded,
			ExitCode: intPointer(0), OutputComplete: true,
		}); err != nil {
			t.Fatalf("complete process-shared command %s: %v", command.CommandID, err)
		}
		if _, err := locald.TransitionSession(t.Context(), command.SessionID, domain.SessionStateReady, "command_complete"); err != nil {
			t.Fatalf("return process-shared session %s to ready: %v", command.SessionID, err)
		}
		if err := locald.ConfirmCommandSlotRelease(t.Context(), command.CommandID); err != nil {
			t.Fatalf("release process-shared command slot %s: %v", command.CommandID, err)
		}
	}
	for i := 0; i < 20; i++ {
		if _, err := locald.StartNextDispatchableCommand(t.Context(), DefaultRunningCommandLimit); !errors.Is(err, ErrCommandNotEligible) {
			t.Fatalf("locald empty queue claim %d error=%v, want ErrCommandNotEligible", i, err)
		}
	}
	writeQueueClaimHelperMarker(t, finishPath)
	select {
	case <-processDone:
	case <-time.After(15 * time.Second):
		t.Fatal("API process did not exit after the finish marker")
	}
	if processErr != nil {
		t.Fatalf("API process failed: %v; output=%q", processErr, processOutput.String())
	}
	healthWait.Wait()
	select {
	case err := <-healthErrors:
		t.Fatalf("locald process health write: %v", err)
	default:
	}

	for i := 0; i < total; i++ {
		command, err := locald.GetCommand(t.Context(), domain.CommandID(fmt.Sprintf("command-multi-project-%02d", i)))
		if err != nil || command.State != domain.CommandStateSucceeded {
			t.Fatalf("process-shared command %d final state=%+v err=%v, want succeeded", i, command, err)
		}
	}
	metrics, err := locald.ReadOperationalMetrics(t.Context())
	if err != nil || metrics.StorageErrorsTotal != 0 {
		t.Fatalf("locald process storage error count=%d err=%v, want 0", metrics.StorageErrorsTotal, err)
	}
}

func waitQueueClaimHelperMarker(t *testing.T, markerPath string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(markerPath); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect process helper marker: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for process helper marker %s", filepath.Base(markerPath))
}

func waitForQueueClaimHelperMarker(t *testing.T, markerPath string, processDone <-chan struct{}, processError func() error, output *bytes.Buffer) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(markerPath); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect API process ready marker: %v", err)
		}
		select {
		case <-processDone:
			t.Fatalf("API process exited before signaling ready: %v; output=%q", processError(), output.String())
		case <-deadline.C:
			t.Fatal("timed out waiting for API process ready marker")
		case <-time.After(time.Millisecond):
		}
	}
}

func writeQueueClaimHelperMarker(t *testing.T, markerPath string) {
	t.Helper()
	if err := os.WriteFile(markerPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write process helper marker: %v", err)
	}
}

func newQueueClaimTestSubmission(t *testing.T, index int) queueClaimTestSubmission {
	t.Helper()
	sessionID := domain.SessionID(fmt.Sprintf("session-multi-project-%02d", index))
	commandID := domain.CommandID(fmt.Sprintf("command-multi-project-%02d", index))
	sessionKey := fmt.Sprintf("key-multi-project-session-%02d", index)
	commandKey := fmt.Sprintf("key-multi-project-command-%02d", index)
	environment := []string{"mac-dev", "linux-dev", "sandbox-dev", "factory-sandbox-dev"}[index%4]
	script := "printf " + string(commandID)
	return queueClaimTestSubmission{
		sessionID: sessionID,
		commandID: commandID,
		accept:    p013Acceptance(t, string(sessionID), sessionKey, environment),
		command: CommandAcceptance{
			CommandID:      commandID,
			SessionID:      sessionID,
			IdempotencyKey: commandKey,
			RequestHash:    p014Hash(t, fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":%q}`, sessionID, script)),
			Script:         script,
			Timeout:        time.Minute,
		},
	}
}

func acceptQueueClaimTestSubmission(ctx context.Context, authority *AuthorityStore, submission queueClaimTestSubmission) error {
	if _, _, err := authority.AcceptSessionCreate(ctx, submission.accept); err != nil {
		return fmt.Errorf("accept session: %w", err)
	}
	if _, err := authority.TransitionSession(ctx, submission.sessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		return fmt.Errorf("make session ready: %w", err)
	}
	if _, duplicate, err := authority.AcceptCommand(ctx, submission.command); err != nil {
		return fmt.Errorf("accept command: %w", err)
	} else if duplicate {
		return errors.New("accept command: unexpected duplicate")
	}
	return nil
}
