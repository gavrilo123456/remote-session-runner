package laneinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// This fixture executes the real operator composition with a schema-40 DB,
// nine mixed retained owners, native no-signal exit observation, original
// archives, target-only transactions and filesystem finalization. Only the
// fixed installed-host/catalog/backup observations are substituted.
func scopedCompositionFixture(t *testing.T) (PRDUpgradePlan, CatalogPRDScopedTeardownRequest, scopedTeardownGuards) {
	t.Helper()
	root := catalogTestTempDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := PRDUpgradePlan{HostID: "mac-control-plane", Namespace: config.LaneNamespace{
		Database: filepath.Join(root, "authority.db"), Workspaces: filepath.Join(root, "workspaces"), Backups: filepath.Join(root, "backups"),
		LifecycleLock: filepath.Join(root, "lifecycle.lock"),
	}}
	for _, path := range []string{plan.Namespace.Workspaces, plan.Namespace.Backups, filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(plan.Namespace.LifecycleLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	var target store.CompletedTeardownRecoveryPair
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 9; index++ {
		pair := seedScopedCompositionPair(t, db, authority, index, index == 8)
		if index == 0 {
			target = pair
		}
		session, err := authority.GetSession(context.Background(), pair.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		workspace := filepath.Join(plan.Namespace.Workspaces, fmt.Sprintf("w%d", index))
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		owner := hostruntime.RuntimeOwnershipRecord{Version: 1, HostOS: runtime.GOOS, SessionID: string(pair.SessionID), Generation: session.RuntimeGeneration,
			Workspace: workspace, OwnedWorkspace: true, PID: 999990000 + index, ProcessGroupID: 999990000 + index, UID: os.Getuid(), Username: account.Username, Command: "/bin/bash", ProcessStartIdentity: "1700000000000001"}
		raw, _ := json.Marshal(owner)
		if err := os.WriteFile(filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership", hashScopedBytes([]byte(owner.SessionID))+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO mailbox_exchanges
(exchange_id,mailbox_id,client_request_id,operation,controller_type,controller_id,canonical_hash_version,canonical_hash,canonical_payload,resource_id,request_state,response_revision,terminal_response_bytes,terminal_response_sha256,available_event_sequence,created_at,updated_at,response_bytes,response_sha256)
VALUES ('scoped-exchange','scoped-mailbox','req-scoped-original','run','local_user','tomasz.walczuk',1,zeroblob(32),X'7b7d',?,'complete',4,X'707265736572766564',zeroblob(32),8,'now','now',X'707265736572766564',zeroblob(32))`, fmt.Sprintf("job-%032x", 1)); err != nil {
		t.Fatal(err)
	}
	_, inventory, _, err := readScopedStorePlan(context.Background(), db, target)
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := hostruntime.ReadMacScopedTeardownOwnership(context.Background(), plan.Namespace.Workspaces, string(target.SessionID), scopedRuntimeSessions(inventory))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	request := validScopedTeardownRequest()
	request.TeardownPair = string(target.SessionID) + ":" + string(target.CommandID)
	request.BackupDirectory = filepath.Join(plan.Namespace.Backups, "initial")
	if err := os.Mkdir(request.BackupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	request.ExpectedPreservedInventorySHA256 = inventory.PreservedInventorySHA256
	request.ExpectedPreservedOwnershipSHA256 = ownership.PreservedSHA256
	request.ExpectedTargetOwnershipSHA256 = ownership.TargetIdentitySHA256
	identity := CatalogReleaseIdentity{ReleaseID: request.ReleaseID, ReleaseSHA256: request.ExpectedReleaseSHA256, BundleSHA256: request.ExpectedBundleSHA256, HostID: plan.HostID}
	guards := scopedTeardownGuards{attested: true, readBinding: func(context.Context, PRDUpgradePlan, PRDServiceProbe, CatalogPRDScopedTeardownRequest) (scopedCatalogBinding, error) {
		return scopedCatalogBinding{root: root, configuration: request.ExpectedConfigurationSHA256, selector: CatalogSelectorStatus{Kind: "catalog", Release: &CatalogReleaseStatus{Identity: identity}}}, nil
	}, requireStopped: func(context.Context, PRDUpgradePlan) error { return nil }, validateBackup: func(context.Context, PRDUpgradePlan, CatalogPRDScopedTeardownRequest) error { return nil }}
	return plan, request, guards
}

func seedScopedCompositionPair(t *testing.T, db *sql.DB, authority *store.AuthorityStore, index int, lost bool) store.CompletedTeardownRecoveryPair {
	t.Helper()
	ctx := context.Background()
	suffix := index + 1
	pair := store.CompletedTeardownRecoveryPair{SessionID: domain.SessionID(fmt.Sprintf("sess-%032x", suffix)), CommandID: domain.CommandID(fmt.Sprintf("cmd-%032x", suffix))}
	jobID := domain.JobID(fmt.Sprintf("job-%032x", suffix))
	target, _ := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	hash, err := domain.HashMutationRequestJSON("create_session", []byte(`{"operation":"create_session","environment":"mac-dev"}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := store.SessionCreateAcceptance{SessionCreate: store.SessionCreate{SessionID: pair.SessionID, Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{CommandTimeout: time.Minute, IdleTimeout: time.Minute, SessionMaxLifetime: time.Hour, OutputBytesPerCommand: 1 << 20}}, IdempotencyKey: fmt.Sprintf("session-%d", suffix), RequestHash: hash}
	raw := []byte(`{"operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":"printf harmless"}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	jobHash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptJob(ctx, store.JobAcceptance{JobID: jobID, SessionID: pair.SessionID, CommandID: pair.CommandID, Controller: controller, IdempotencyKey: fmt.Sprintf("job-%d", suffix), RequestHash: jobHash, Environment: "mac-dev", Target: target, Source: domain.NewEmptySource(), Script: "printf harmless", CanonicalPayload: canonical, IdempotencyRetention: store.DefaultSessionIdempotencyRetention}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptSessionCreate(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteSessionCreation(ctx, pair.SessionID, domain.SessionStateReady, fmt.Sprintf("generation-%d", suffix), "", "runtime_ready"); err != nil {
		t.Fatal(err)
	}
	commandHash, err := domain.HashMutationRequestJSON("submit_command", []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":"printf harmless"}`, pair.SessionID)), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptCommand(ctx, store.CommandAcceptance{CommandID: pair.CommandID, SessionID: pair.SessionID, IdempotencyKey: fmt.Sprintf("command-%d", suffix), RequestHash: commandHash, Script: "printf harmless", Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	exit := 1
	command, err := authority.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: pair.CommandID, NextState: domain.CommandStateFailed, ExitCode: &exit, OutputComplete: true}, domain.SessionStateReady, "command_completed", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, pair.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, pair.SessionID, domain.SessionStateLost, "runtime_cleanup_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	teardown := store.JobTeardownLost
	if _, err := authority.CheckpointJob(ctx, jobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseLost, Command: &command, TeardownState: &teardown, TeardownReason: "runtime_cleanup_unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	if lost {
		for _, statement := range []string{
			`UPDATE exec_commands SET state='lost',exit_code=NULL,output_complete=0,output_unavailable_reason='capture_boundary_unconfirmed' WHERE command_id=?`,
			`UPDATE exec_jobs SET command_state='lost',exit_code=NULL,output_complete=0,output_unavailable_reason='capture_boundary_unconfirmed' WHERE command_id=?`,
			`UPDATE exec_command_events SET event_type='command_lost',payload=X'' WHERE command_id=? AND sequence=(SELECT final_event_sequence FROM exec_commands WHERE command_id=?)`,
			`UPDATE exec_command_slots SET stop_confirmed_at=NULL,released_at=NULL WHERE command_id=?`,
		} {
			args := []any{string(pair.CommandID)}
			if strings.Contains(statement, "SELECT final_event_sequence") {
				args = append(args, string(pair.CommandID))
			}
			if _, err := db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	return pair
}

func TestScopedTeardownCompositionPreservesMixedPeersAcrossInterruptions(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Mac no-signal process observation; store/archive fixtures run on every platform")
	}
	for _, checkpoint := range []string{"", "after_proof", "after_release", "workspace_removed", "after_runtime_finalization"} {
		t.Run(checkpoint, func(t *testing.T) {
			plan, request, guards := scopedCompositionFixture(t)
			if checkpoint != "" {
				guards.checkpoint = func(stage string) error {
					if stage == checkpoint || checkpoint == "workspace_removed" && stage == "after_release" {
						if checkpoint == "workspace_removed" {
							binding, err := readScopedTargetBinding(scopedTeardownArchiveDirectory(request, strings.Split(request.TeardownPair, ":")[0]), request.ExpectedTargetOwnershipSHA256)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.Remove(binding.Workspace.Path); err != nil {
								t.Fatal(err)
							}
						}
						return errors.New("fixture interruption")
					}
					return nil
				}
			}
			report, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards)
			if checkpoint != "" {
				if err == nil {
					t.Fatal("interruption ignored")
				}
				archive := scopedTeardownArchiveDirectory(request, strings.Split(request.TeardownPair, ":")[0])
				raw, readErr := readScopedPrivateFile(filepath.Join(archive, "manifest.json"), 2<<20)
				if readErr != nil {
					t.Fatal(readErr)
				}
				request.RuntimeArchiveDirectory = archive
				request.ExpectedRuntimeArchiveManifestSHA256 = hashScopedBytes(raw)
				request.BackupDirectory = filepath.Join(plan.Namespace.Backups, "fresh-retry")
				if err := os.Mkdir(request.BackupDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
				guards.checkpoint = nil
				report, err = recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards)
			}
			if err != nil {
				t.Fatalf("composition/retry failed: %v", err)
			}
			if !report.Applied || !report.TargetReservationReleased || report.FinalizationPending || report.WorkAfter.ActiveSessionReservations != 8 || report.WorkAfter.ActiveCommandSlots != 1 || report.SafeToResume != "not_established" || report.SelectedExecutorInvoked {
				t.Fatalf("wrong scoped result: %+v", report)
			}
			if _, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards); err != nil {
				t.Fatalf("fully finalized retry: %v", err)
			}
		})
	}
}

func TestScopedTeardownCompositionRefusesBeforeTargetRelease(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Mac no-signal observation")
	}
	for _, kind := range []string{"unsupported_tree", "workspace_replaced", "backup_refused", "services_loaded", "peer_changed"} {
		t.Run(kind, func(t *testing.T) {
			plan, request, guards := scopedCompositionFixture(t)
			targetID := strings.Split(request.TeardownPair, ":")[0]
			targetMarker := filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership", hashScopedBytes([]byte(targetID))+".json")
			original, err := os.ReadFile(targetMarker)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unsupported_tree":
				if err := os.Symlink("outside", filepath.Join(plan.Namespace.Workspaces, "w0", "unapproved-link")); err != nil {
					t.Fatal(err)
				}
			case "workspace_replaced":
				path := filepath.Join(plan.Namespace.Workspaces, "w0")
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "backup_refused":
				guards.validateBackup = func(context.Context, PRDUpgradePlan, CatalogPRDScopedTeardownRequest) error {
					return errors.New("fixture refusal")
				}
			case "services_loaded":
				guards.requireStopped = func(context.Context, PRDUpgradePlan) error { return errors.New("fixture refusal") }
			case "peer_changed":
				path := filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership", hashScopedBytes([]byte(fmt.Sprintf("sess-%032x", 2)))+".json")
				raw, _ := os.ReadFile(path)
				var owner hostruntime.RuntimeOwnershipRecord
				if json.Unmarshal(raw, &owner) != nil {
					t.Fatal("peer decode")
				}
				owner.Generation = "unexpected-generation"
				raw, _ = json.Marshal(owner)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards); !errors.Is(err, ErrCatalogPRDScopedTeardown) || !strings.Contains(err.Error(), "failure_stage=") {
				t.Fatalf("unsafe composition not refused with bounded stage: %v", err)
			}
			after, err := os.ReadFile(targetMarker)
			if err != nil || string(after) != string(original) {
				t.Fatal("refusal stamped or removed target marker")
			}
			db, err := store.OpenExistingCurrent(context.Background(), plan.Namespace.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			pair, _ := scopedTeardownPair(request.TeardownPair)
			_, inventory, work, err := readScopedStorePlan(context.Background(), db, pair)
			if err != nil || inventory.Recovery.AlreadyRecovered || work.ActiveSessionReservations != 9 || work.ActiveCommandSlots != 1 {
				t.Fatalf("refusal changed reservations: %+v %v", work, err)
			}
		})
	}
}

func TestScopedTeardownCompositionRefusesInconsistentOrCorruptRetry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Mac no-signal observation")
	}
	for _, kind := range []string{"marker_missing_workspace_present", "workspace_replaced", "archive_corrupt"} {
		t.Run(kind, func(t *testing.T) {
			plan, request, guards := scopedCompositionFixture(t)
			guards.checkpoint = func(stage string) error {
				if stage == "after_release" {
					return errors.New("fixture interruption")
				}
				return nil
			}
			if _, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards); err == nil {
				t.Fatal("interruption ignored")
			}
			targetID := strings.Split(request.TeardownPair, ":")[0]
			archive := scopedTeardownArchiveDirectory(request, targetID)
			raw, err := readScopedPrivateFile(filepath.Join(archive, "manifest.json"), 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			request.RuntimeArchiveDirectory = archive
			request.ExpectedRuntimeArchiveManifestSHA256 = hashScopedBytes(raw)
			request.BackupDirectory = filepath.Join(plan.Namespace.Backups, "fresh-retry")
			if err := os.Mkdir(request.BackupDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "marker_missing_workspace_present":
				if err := os.Remove(filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership", hashScopedBytes([]byte(targetID))+".json")); err != nil {
					t.Fatal(err)
				}
			case "workspace_replaced":
				path := filepath.Join(plan.Namespace.Workspaces, "w0")
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "archive_corrupt":
				if err := os.WriteFile(filepath.Join(archive, "workspace.tar"), []byte("not-original-archive"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			guards.checkpoint = nil
			if _, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards); !errors.Is(err, ErrCatalogPRDScopedTeardown) {
				t.Fatalf("unsafe retry accepted: %v", err)
			}
			db, err := store.OpenExistingCurrent(context.Background(), plan.Namespace.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			pair, _ := scopedTeardownPair(request.TeardownPair)
			_, inventory, work, err := readScopedStorePlan(context.Background(), db, pair)
			if err != nil || !inventory.Recovery.AlreadyRecovered || !inventory.Recovery.FinalizationPending || work.ActiveSessionReservations != 8 || work.ActiveCommandSlots != 1 {
				t.Fatalf("retry cleared pending evidence or old holds: %+v %v", inventory, err)
			}
		})
	}
}

func TestScopedTeardownCompositionRefusesBytesChangedAcrossExitProof(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Mac no-signal observation")
	}
	plan, request, guards := scopedCompositionFixture(t)
	path := filepath.Join(plan.Namespace.Workspaces, "w0", "result.txt")
	if err := os.WriteFile(path, []byte("original-before-exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	guards.checkpoint = func(stage string) error {
		if stage == "after_proof" {
			return os.WriteFile(path, []byte("changed-at-exit-boundary"), 0o600)
		}
		return nil
	}
	if _, err := recoverCatalogPRDScopedTeardownWith(context.Background(), plan, nil, request, guards); !errors.Is(err, ErrCatalogPRDScopedTeardown) || !strings.Contains(err.Error(), "failure_stage=archive") {
		t.Fatalf("stale workspace archive accepted: %v", err)
	}
	archive := scopedTeardownArchiveDirectory(request, strings.Split(request.TeardownPair, ":")[0])
	raw, err := readScopedPrivateFile(filepath.Join(archive, "workspace.tar"), scopedTeardownArchiveMaxBytes+(4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "original-before-exit") || strings.Contains(string(raw), "changed-at-exit-boundary") {
		t.Fatal("refusal replaced the original reviewed archive")
	}
	db, err := store.OpenExistingCurrent(context.Background(), plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pair, _ := scopedTeardownPair(request.TeardownPair)
	_, inventory, work, err := readScopedStorePlan(context.Background(), db, pair)
	if err != nil || inventory.Recovery.AlreadyRecovered || inventory.Recovery.FinalizationPending || work.ActiveSessionReservations != 9 || work.ActiveCommandSlots != 1 {
		t.Fatalf("archive mismatch released capacity: %+v %v", inventory, err)
	}
}
