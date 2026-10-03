package runnerlocald

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG011RestartPreflightReadsOnlyAndRequiresNoResumableExecution(t *testing.T) {
	for _, fixture := range []struct {
		name               string
		activeSession      bool
		activeCommand      bool
		queuedCommand      bool
		resumablePhase     store.JobPhase
		wantActiveSessions int64
		wantActiveCommands int64
		wantQueuedCommands int64
		wantResumableJobs  int64
		wantNotQuiescent   bool
	}{
		{name: "empty authority"},
		{name: "active session", activeSession: true, wantActiveSessions: 1, wantNotQuiescent: true},
		{name: "active command", activeCommand: true, wantActiveCommands: 1, wantNotQuiescent: true},
		{name: "queued command", queuedCommand: true, wantQueuedCommands: 1, wantNotQuiescent: true},
		{name: "creating one-off job", resumablePhase: store.JobPhaseCreatingSession, wantResumableJobs: 1, wantNotQuiescent: true},
		{name: "accepting one-off job", resumablePhase: store.JobPhaseAcceptingCommand, wantResumableJobs: 1, wantNotQuiescent: true},
		{name: "awaiting one-off job", resumablePhase: store.JobPhaseAwaitingCommand, wantResumableJobs: 1, wantNotQuiescent: true},
		{name: "closing one-off job", resumablePhase: store.JobPhaseClosingSession, wantResumableJobs: 1, wantNotQuiescent: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path, writable := bug011PreflightAuthority(t)
			bug011SeedPreflightCounts(t, writable, fixture.activeSession, fixture.activeCommand, fixture.queuedCommand, fixture.resumablePhase)
			if err := writable.Close(); err != nil {
				t.Fatal(err)
			}

			metrics, err := preflightMacLocalExecution(context.Background(), path)
			if fixture.wantNotQuiescent {
				if !errors.Is(err, errRestartPreflightNotQuiescent) {
					t.Fatalf("preflight error=%v, want %v", err, errRestartPreflightNotQuiescent)
				}
			} else if err != nil {
				t.Fatalf("preflight error=%v", err)
			}
			if metrics.ActiveSessionSlots != fixture.wantActiveSessions || metrics.ActiveCommandSlots != fixture.wantActiveCommands || metrics.QueuedCommands != fixture.wantQueuedCommands || metrics.ResumableJobs != fixture.wantResumableJobs {
				t.Fatalf("preflight metrics=%+v, want sessions=%d commands=%d queued=%d resumable_jobs=%d", metrics, fixture.wantActiveSessions, fixture.wantActiveCommands, fixture.wantQueuedCommands, fixture.wantResumableJobs)
			}
		})
	}
}

func TestBUG011RestartPreflightCommandUsesActiveSettingsAndRefuses(t *testing.T) {
	for _, fixture := range []struct {
		name             string
		queuedCommand    bool
		resumableJob     bool
		wantCode         int
		wantOutput       string
		wantErrorSnippet string
	}{
		{name: "quiescent", wantCode: 0, wantOutput: "runner-locald preflight-restart: local execution is quiescent\n"},
		{name: "queued", queuedCommand: true, wantCode: 1, wantErrorSnippet: "queued_commands=1 resumable_jobs=0"},
		{name: "pre-session job", resumableJob: true, wantCode: 1, wantErrorSnippet: "queued_commands=0 resumable_jobs=1"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			databasePath, writable := bug011PreflightAuthority(t)
			resumablePhase := store.JobPhase("")
			if fixture.resumableJob {
				resumablePhase = store.JobPhaseCreatingSession
			}
			bug011SeedPreflightCounts(t, writable, false, false, fixture.queuedCommand, resumablePhase)
			if err := writable.Close(); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			selected := ""
			code := runRestartPreflightWithSettings([]string{"--config", "/private/tmp/bug011-active-mac.yaml"}, &stdout, &stderr, func(path string) (restartPreflightSettings, error) {
				selected = path
				return restartPreflightSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: databasePath}, nil
			})
			// The closure receives the active config path and resolves it to this
			// test-owned authority. Keeping the database outside that path proves
			// the handler follows the resolved setting rather than guessing a path.
			if selected != "/private/tmp/bug011-active-mac.yaml" {
				t.Fatalf("preflight selected config=%q", selected)
			}
			if code != fixture.wantCode {
				t.Fatalf("preflight exit=%d stdout=%q stderr=%q, want %d", code, stdout.String(), stderr.String(), fixture.wantCode)
			}
			if stdout.String() != fixture.wantOutput {
				t.Fatalf("preflight stdout=%q, want %q", stdout.String(), fixture.wantOutput)
			}
			if fixture.wantErrorSnippet == "" {
				if stderr.Len() != 0 {
					t.Fatalf("quiescent preflight stderr=%q", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), fixture.wantErrorSnippet) || !strings.Contains(stderr.String(), "local execution is not quiescent") {
				t.Fatalf("preflight stderr=%q, want safe refusal with %q", stderr.String(), fixture.wantErrorSnippet)
			}
		})
	}
}

func TestBUG011RestartPreflightMissingAuthorityIsDistinguishableAndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "authority.db")
	if _, err := preflightMacLocalExecution(context.Background(), path); !errors.Is(err, store.ErrDatabaseMissing) {
		t.Fatalf("preflight missing authority error=%v, want %v", err, store.ErrDatabaseMissing)
	}

	var stdout, stderr bytes.Buffer
	code := runRestartPreflightWithSettings([]string{"--config", "/private/tmp/bug011-active-mac.yaml"}, &stdout, &stderr, func(string) (restartPreflightSettings, error) {
		return restartPreflightSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: path}, nil
	})
	if code != restartPreflightAuthorityMissingExit || stdout.Len() != 0 || stderr.String() != "runner-locald preflight-restart: active local authority database is unavailable\n" {
		t.Fatalf("missing authority command exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := preflightMacLocalExecution(context.Background(), path); !errors.Is(err, store.ErrDatabaseMissing) {
		t.Fatalf("preflight changed missing authority error=%v, want %v", err, store.ErrDatabaseMissing)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database parent was changed: stat error=%v, want not exist", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database file was created: stat error=%v, want not exist", err)
	}
}

func TestBUG011RunDispatchesRestartPreflight(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"preflight-restart", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, &stdout, &stderr); code != 1 {
		t.Fatalf("Run(preflight-restart) exit=%d stdout=%q stderr=%q, want 1", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.String() != "runner-locald preflight-restart: could not prove local execution is quiescent\n" {
		t.Fatalf("Run(preflight-restart) stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func bug011PreflightAuthority(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "authority.db")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return path, database
}

func bug011SeedPreflightCounts(t *testing.T, database *sql.DB, activeSession, activeCommand, queuedCommand bool, resumablePhase store.JobPhase) {
	t.Helper()
	if !activeSession && !activeCommand && !queuedCommand && resumablePhase == "" {
		return
	}
	ctx := context.Background()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if resumablePhase != "" {
		intent := p078LocalRunIntent(t, authority)
		job, duplicate, err := authority.AcceptJob(ctx, store.JobAcceptance{
			JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID,
			Controller: intent.Controller, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash,
			Environment: intent.Environment, Target: intent.Target, Source: intent.Source, Script: string(intent.ScriptBytes),
			CanonicalPayload: intent.PayloadJSON, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		})
		if err != nil || duplicate || job.Phase != store.JobPhaseCreatingSession {
			t.Fatalf("seed resumable one-off=%+v duplicate=%v err=%v", job, duplicate, err)
		}
		if resumablePhase != store.JobPhaseCreatingSession {
			job, err = authority.CheckpointJob(ctx, job.JobID, store.JobCheckpoint{
				ExpectedPhase: store.JobPhaseCreatingSession,
				NextPhase:     resumablePhase,
			})
			if err != nil || job.Phase != resumablePhase {
				t.Fatalf("seed resumable one-off phase=%q job=%+v err=%v", resumablePhase, job, err)
			}
		}
	}
	if !activeSession && !activeCommand && !queuedCommand {
		return
	}
	if activeCommand && queuedCommand {
		t.Fatal("preflight fixture cannot represent both command states with one command")
	}
	sessionID := domain.SessionID("session-bug011-preflight")
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(),
		Limits: domain.EffectiveSessionLimits{CommandTimeout: time.Minute, IdleTimeout: time.Minute, SessionMaxLifetime: time.Hour, OutputBytesPerCommand: 1024},
	}); err != nil {
		t.Fatal(err)
	}
	if !activeSession {
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := database.ExecContext(ctx, `UPDATE exec_capacity_reservations SET cleanup_confirmed_at = ?, released_at = ? WHERE session_id = ?`, stamp, stamp, string(sessionID)); err != nil {
			t.Fatal(err)
		}
	}
	if !activeCommand && !queuedCommand {
		return
	}
	state := "running"
	if queuedCommand {
		state = "queued"
	}
	commandID := "command-bug011-preflight"
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	zeroHash := make([]byte, 32)
	if _, err := database.ExecContext(ctx, `
INSERT INTO exec_commands (
    command_id, session_id, ordinal, request_hash_version, request_hash,
    script_bytes, script_sha256, state, timeout_ns, created_at, updated_at
) VALUES (?, ?, 1, 1, ?, X'', ?, ?, ?, ?, ?)`,
		commandID, string(sessionID), zeroHash, zeroHash, state, int64(time.Minute), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if activeCommand {
		if _, err := database.ExecContext(ctx, `INSERT INTO exec_command_slots(command_id, host_key, reserved_at) VALUES (?, 'authority', ?)`, commandID, stamp); err != nil {
			t.Fatal(err)
		}
	}
}
