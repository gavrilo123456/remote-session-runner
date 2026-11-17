package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

func gpuCloseoutFixture(t *testing.T) (*gpuCloseoutState, gpuCloseoutBackend, *[]string) {
	t.Helper()
	now := time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC)
	expiry := now.Add(-time.Minute)
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, config.MacAccount)
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	raw := []byte(`{"request_id":"` + gpuCloseoutRequest + `","operation":"run","request_state":"accepted","response_revision":2,"job_id":"` + gpuCloseoutJob + `","session_id":"` + gpuCloseoutSession + `","command_id":"` + gpuCloseoutCommand + `","delivery_state":"uncertain"}`)
	digest := sha256.Sum256(raw)
	state := &gpuCloseoutState{
		binding: scopedCatalogBinding{configuration: strings.Repeat("a", 64), database: CatalogRetainedDatabaseIdentity{Path: "/fixture/authority.db", Device: 1, Inode: 2},
			selector: CatalogSelectorStatus{Kind: "catalog", Release: &CatalogReleaseStatus{Identity: CatalogReleaseIdentity{ReleaseID: "rr-fixture", ReleaseSHA256: strings.Repeat("b", 64), BundleSHA256: strings.Repeat("c", 64)}}}},
		exchange: store.MailboxExchangeRecord{MailboxID: "gpu-services", RequestID: gpuCloseoutRequest, Operation: "run", Controller: controller, ExecutionIdempotencyKey: "exact-key", State: store.MailboxExchangeAccepted, ResponseRevision: 2, ResponseBytes: raw, ResponseSHA256: digest[:]},
		intent: store.LocalIntentRecord{LocalIntentCreate: store.LocalIntentCreate{IntentID: gpuCloseoutIntent, Operation: "run", ResourceID: gpuCloseoutJob, JobID: gpuCloseoutJob, SessionID: gpuCloseoutSession, CommandID: gpuCloseoutCommand,
			Target: target, Environment: "sandbox-dev", Controller: controller, IdempotencyKey: "exact-key", PayloadJSON: []byte(`{"script":"printf fixture"}`), DeliveryState: store.LocalIntentUncertain, Reason: "remote_transport_uncertain", LeaseOwner: "fixture-router", LeaseExpiresAt: &expiry, AttemptCount: 1}, UpdatedAt: now.Add(-time.Minute)},
		lifecycle: []store.LocalIntentLifecycleRecord{{IntentID: gpuCloseoutIntent, Sequence: 3, NewState: store.LocalIntentUncertain, Reason: "remote_transport_uncertain", OccurredAt: now.Add(-time.Minute)}},
	}
	calls := []string{}
	b := gpuCloseoutBackend{read: func(context.Context) (gpuCloseoutState, error) { calls = append(calls, "read"); return *state, nil },
		stopped: func(context.Context) error { calls = append(calls, "stopped"); return nil }, lock: func() (func(), error) {
			calls = append(calls, "lock")
			return func() { calls = append(calls, "unlock") }, nil
		},
		backup: func(context.Context, GPUUncertainSnapshot) (string, error) {
			calls = append(calls, "backup")
			return "/fixture/backup", nil
		},
		close: func(context.Context, GPUUncertainSnapshot) (store.MailboxExchangeRecord, error) {
			calls = append(calls, "close")
			state.exchange.State = store.MailboxExchangeIndeterminate
			state.exchange.ResponseRevision = 3
			return state.exchange, nil
		},
		project: func(context.Context, store.MailboxExchangeRecord) error { calls = append(calls, "project"); return nil }, now: func() time.Time { return now }, revision: strings.Repeat("d", 40)}
	return state, b, &calls
}

func gpuApproval(t *testing.T, b gpuCloseoutBackend) GPUUncertainApproval {
	t.Helper()
	plan, err := planGPUCloseout(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return GPUUncertainApproval{FormatVersion: 1, Operation: "gpu-f07-unknown-outcome-closeout", Snapshot: plan.Snapshot, SnapshotSHA256: plan.SnapshotSHA256, OperatorRevision: b.revision, AuthorizeUnknownOutcome: true, ExpiresAt: b.now().Add(time.Hour)}
}

func TestGPUCloseoutOnlinePlanHasNoEffectsEvenWithActiveLease(t *testing.T) {
	s, b, calls := gpuCloseoutFixture(t)
	s.binding.active = true
	future := b.now().Add(time.Minute)
	s.intent.LeaseExpiresAt = &future
	plan, err := planGPUCloseout(context.Background(), b)
	if err != nil || !plan.ServiceActive || plan.Applied || len(*calls) != 1 || (*calls)[0] != "read" || plan.SafeToResume != "not_established" {
		t.Fatalf("plan=%+v calls=%v err=%v", plan, *calls, err)
	}
	activeDigest := plan.SnapshotSHA256
	s.binding.active = false
	again, err := planGPUCloseout(context.Background(), b)
	if err != nil || activeDigest != again.SnapshotSHA256 {
		t.Fatal("service stop changed the approved metadata snapshot", err)
	}
}

func TestGPUCloseoutApplyOrdersGuardsBackupCommitAndProjection(t *testing.T) {
	_, b, calls := gpuCloseoutFixture(t)
	a := gpuApproval(t, b)
	*calls = nil
	r, err := applyGPUCloseout(context.Background(), b, a)
	if err != nil || !r.Applied || r.ResponseRevision != 3 || r.SafeToResume != "not_established" || r.Disposition != "owner_abandoned_execution_outcome_unknown_capacity_unchanged" {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	if got := strings.Join(*calls, ","); got != "lock,stopped,read,backup,read,stopped,close,project,unlock" {
		t.Fatal(got)
	}
	encoded, _ := json.Marshal(r)
	for _, forbidden := range []string{"exit_code", "command_state", "teardown_outcome", "available_event_sequence", "acknowledged"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("invented target result or ACK: %s", encoded)
		}
	}
}

func TestGPUCloseoutRejectsUnapprovedDriftLiveLeaseAndFailedGuards(t *testing.T) {
	for _, name := range []string{"no authorization", "wrong operator", "expired", "too long", "snapshot digest", "changed response", "changed catalog", "lease active", "service active", "stop refused", "lock held", "backup failed", "drift after backup", "expiry after backup"} {
		t.Run(name, func(t *testing.T) {
			s, b, calls := gpuCloseoutFixture(t)
			a := gpuApproval(t, b)
			*calls = nil
			switch name {
			case "no authorization":
				a.AuthorizeUnknownOutcome = false
			case "wrong operator":
				a.OperatorRevision = strings.Repeat("e", 40)
			case "expired":
				a.ExpiresAt = b.now()
			case "too long":
				a.ExpiresAt = b.now().Add(5 * time.Hour)
			case "snapshot digest":
				a.SnapshotSHA256 = strings.Repeat("0", 64)
			case "changed response":
				s.exchange.ResponseBytes = []byte(`{}`)
			case "changed catalog":
				s.binding.configuration = strings.Repeat("f", 64)
			case "lease active":
				future := b.now().Add(time.Minute)
				s.intent.LeaseExpiresAt = &future
			case "service active":
				s.binding.active = true
			case "stop refused":
				b.stopped = func(context.Context) error { return errors.New("running") }
			case "lock held":
				b.lock = func() (func(), error) { return nil, errors.New("locked") }
			case "backup failed":
				b.backup = func(context.Context, GPUUncertainSnapshot) (string, error) { return "", errors.New("full") }
			case "drift after backup":
				b.backup = func(context.Context, GPUUncertainSnapshot) (string, error) {
					s.intent.AttemptCount++
					return "/fixture/backup", nil
				}
			case "expiry after backup":
				expired := false
				previous := b.now
				b.now = func() time.Time {
					if expired {
						return a.ExpiresAt
					}
					return previous()
				}
				b.backup = func(context.Context, GPUUncertainSnapshot) (string, error) {
					expired = true
					return "/fixture/backup", nil
				}
			}
			if r, err := applyGPUCloseout(context.Background(), b, a); err == nil || r.Applied {
				t.Fatalf("unsafe apply report=%+v err=%v", r, err)
			}
			for _, call := range *calls {
				if call == "close" || call == "project" {
					t.Fatalf("unsafe effect after %s: %v", name, *calls)
				}
			}
		})
	}
}

func TestGPUCloseoutExactIdentityAndEvidenceGuards(t *testing.T) {
	for _, name := range []string{"mailbox", "request", "target", "environment", "job", "proof", "terminal", "ACK", "missing lease", "lifecycle"} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := gpuCloseoutFixture(t)
			switch name {
			case "mailbox":
				s.exchange.MailboxID = "slidestud-io"
			case "request":
				s.exchange.RequestID = "req-unrelated"
			case "target":
				s.intent.Target, _ = domain.NewExecutionTarget(domain.TargetKindRemote, "sandbox-host")
			case "environment":
				s.intent.Environment = "mac-dev"
			case "job":
				s.intent.JobID = "job-unrelated"
			case "proof":
				s.intent.RemoteTerminalProofVersion = store.RemoteTerminalProofP149
			case "terminal":
				s.exchange.State = store.MailboxExchangeComplete
			case "ACK":
				now := time.Now()
				s.exchange.AcknowledgedAt = &now
			case "missing lease":
				s.intent.LeaseExpiresAt = nil
			case "lifecycle":
				s.lifecycle[0].NewState = store.LocalIntentAccepted
			}
			if _, err := gpuSnapshot(*s); err == nil {
				t.Fatal("accepted", name)
			}
		})
	}
}

func TestGPUCloseoutProjectionInterruptionKeepsCommittedOutcomeAndCannotReclose(t *testing.T) {
	_, b, calls := gpuCloseoutFixture(t)
	a := gpuApproval(t, b)
	*calls = nil
	b.project = func(context.Context, store.MailboxExchangeRecord) error { return errors.New("publication interrupted") }
	r, err := applyGPUCloseout(context.Background(), b, a)
	if err == nil || !r.Applied || r.ResponseRevision != 3 || !strings.Contains(err.Error(), "terminal committed") {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	if _, err = applyGPUCloseout(context.Background(), b, a); err == nil {
		t.Fatal("reclosed terminal outcome")
	}
	count := 0
	for _, call := range *calls {
		if call == "close" {
			count++
		}
	}
	if count != 1 {
		t.Fatal(*calls)
	}
}

func TestGPUCloseoutPreservesConcurrentTerminalWinner(t *testing.T) {
	s, b, _ := gpuCloseoutFixture(t)
	a := gpuApproval(t, b)
	b.close = func(context.Context, GPUUncertainSnapshot) (store.MailboxExchangeRecord, error) {
		r := s.exchange
		r.State = store.MailboxExchangeComplete
		r.ResponseRevision = 5
		return r, nil
	}
	r, err := applyGPUCloseout(context.Background(), b, a)
	if err != nil || r.ResponseRevision != 5 || r.Disposition != "existing_terminal_outcome_preserved_capacity_unchanged" {
		t.Fatalf("report=%+v err=%v", r, err)
	}
}

func TestGPUCloseoutPrivateApprovalReadRejectsSymlinkAndPublicFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "approval.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gpuReadPrivate(path, 32); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := gpuReadPrivate(link, 32); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gpuReadPrivate(path, 32); err == nil {
		t.Fatal("public approval accepted")
	}
}

func TestGPUCloseoutRealStoreDistinctHashesAndInterruptedProjectionResume(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC)
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dbDir, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, b, calls := gpuCloseoutFixture(t)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", []byte(`{"operation":"run","environment":"sandbox-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf fixture","timeout_seconds":30}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	executionHash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clientPayload := []byte(`{"operation":"run","script":"printf fixture","timeout_seconds":30}`)
	sum := sha256.Sum256(clientPayload)
	clientHash, _ := domain.NewCanonicalHash(1, sum[:])
	ref, _ := store.NewMailboxExchangeRef("gpu-services", gpuCloseoutRequest)
	exchange, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{RequestID: gpuCloseoutRequest, Operation: "run", Controller: s.intent.Controller, IdempotencyKey: "client-key", RequestHash: clientHash, CanonicalPayload: clientPayload,
		Selection: &store.MailboxExecutionSelection{ContextName: "ubuntu-current", Environment: "sandbox-dev", Target: s.intent.Target, Source: store.MailboxExecutionSelectionInboxDefault}, SelectionState: store.MailboxExecutionSelectionResolved})
	if err != nil {
		t.Fatal(err)
	}
	i := s.intent.LocalIntentCreate
	i.Source = domain.NewEmptySource()
	i.RequestHash = executionHash
	i.IdempotencyKey = exchange.ExecutionIdempotencyKey
	i.PayloadJSON = canonical
	i.ScriptBytes = []byte("printf fixture")
	i.DeliveryState = ""
	i.Reason = ""
	i.LeaseOwner = ""
	i.LeaseExpiresAt = nil
	i.AttemptCount = 0
	if _, err = authority.CreateLocalIntent(ctx, i); err != nil {
		t.Fatal(err)
	}
	initial := strings.ReplaceAll(strings.ReplaceAll(string(s.exchange.ResponseBytes), `"response_revision":2`, `"response_revision":1`), `"delivery_state":"uncertain"`, `"delivery_state":"recorded"`)
	if _, err = authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{State: store.MailboxExchangeAccepted, Bytes: []byte(initial)}); err != nil {
		t.Fatal(err)
	}
	if _, err = authority.ClaimLocalIntent(ctx, i.IntentID, "fixture-router", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = authority.TransitionLocalIntent(ctx, i.IntentID, store.LocalIntentUncertain, "remote_transport_uncertain"); err != nil {
		t.Fatal(err)
	}
	if _, err = authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{State: store.MailboxExchangeAccepted, Bytes: s.exchange.ResponseBytes}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	b.now = func() time.Time { return now }
	b.read = func(context.Context) (gpuCloseoutState, error) {
		e, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		intent, err := authority.GetLocalIntent(ctx, i.IntentID)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		life, err := authority.ListLocalIntentLifecycle(ctx, i.IntentID)
		return gpuCloseoutState{binding: s.binding, exchange: e, intent: intent, lifecycle: life}, err
	}
	b.close = func(context.Context, GPUUncertainSnapshot) (store.MailboxExchangeRecord, error) {
		*calls = append(*calls, "real-close")
		r, _, err := authority.CloseUncertainRemoteRunAsIndeterminateInMailbox(ctx, ref, i.IntentID, i.JobID, i.SessionID, i.CommandID)
		return r, err
	}
	b.validateBackup = func(context.Context, GPUUncertainSnapshot, string) error { return nil }
	approval := gpuApproval(t, b)
	if approval.Snapshot.MailboxRequestHash == approval.Snapshot.ExecutionRequestHash {
		t.Fatal("fixture did not exercise independent request hashes")
	}
	b.project = func(context.Context, store.MailboxExchangeRecord) error {
		return errors.New("simulated crash before outbox projection")
	}
	first, err := applyGPUCloseout(ctx, b, approval)
	if err == nil || !first.Applied {
		t.Fatalf("report=%+v err=%v", first, err)
	}
	closed, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err != nil || closed.State != store.MailboxExchangeIndeterminate || closed.AcknowledgedAt != nil || closed.AvailableEventSequence != nil {
		t.Fatalf("closed=%+v err=%v", closed, err)
	}
	b.project = func(context.Context, store.MailboxExchangeRecord) error { return nil }
	resumed, err := projectGPUCloseout(ctx, b, approval, "/fixture/backup")
	if err != nil || !resumed.Applied || resumed.Mode != "project-only" || resumed.ResponseRevision != 3 {
		t.Fatalf("report=%+v err=%v", resumed, err)
	}
	count := 0
	for _, call := range *calls {
		if call == "real-close" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("projection retried closeout: %v", *calls)
	}
	eligible, err := authority.ListEligibleLocalIntents(ctx, 10)
	if err != nil || len(eligible) != 0 {
		t.Fatalf("closed intent may dispatch: %+v %v", eligible, err)
	}
	changed := approval
	changed.Snapshot.BundleSHA256 = strings.Repeat("f", 64)
	changed.SnapshotSHA256 = gpuDigest(changed.Snapshot)
	if _, err = projectGPUCloseout(ctx, b, changed, "/fixture/backup"); err == nil {
		t.Fatal("changed original binding was accepted")
	}
}
