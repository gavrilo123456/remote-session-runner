package runnerlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

// TestBUG004ServicePrioritizesFreshMailboxWorkOverTerminalArtifactBacklog
// protects the reported production shape: retained terminal receipts from a
// prior stop must not prevent a newly published remote run from reaching a
// terminal result and normal ACK lifecycle. The P162 harness deliberately
// uses the real marker-last mailbox, SQLite authority, local API, dispatcher,
// and service scheduler. Its remote bridge is in-memory only.
func TestBUG004ServicePrioritizesFreshMailboxWorkOverTerminalArtifactBacklog(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	now := time.Now().UTC().Truncate(time.Second)
	caller := newP162RemoteCaller(func() time.Time { return now })
	h := newP162Harness(t, root, databasePath, &now, caller)

	batchLimit := mailbox.DefaultTerminalArtifactRecoveryBatchLimit
	if batchLimit < 1 {
		t.Fatalf("terminal artifact recovery batch limit=%d, want positive", batchLimit)
	}
	// More than two full pages makes the tail assertion independent of any
	// first-page ordering and proves the keyset cursor progresses later.
	backlogIDs := p163SeedTerminalArtifactBacklog(t, h, batchLimit*3+1)
	tailID := backlogIDs[len(backlogIDs)-1]

	client := p162Client(t, h.mailboxRoot)
	const freshID = "req-bug004-fresh-remote-run"
	p162WriteRunRequest(t, client, freshID, "key-bug004-fresh-remote-run", "printf 'P162_COMPLETE_OK\\n'")

	// A single service cycle must first service fresh ingress and the remote
	// dispatch/reconciliation path. Terminal recovery gets only its bounded
	// later slice, so the tail remains absent at this point.
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	fresh := p162ReadResponse(t, client, freshID)
	if fresh.RequestState != string(store.MailboxExchangeComplete) ||
		fresh.CommandState != string(domain.CommandStateSucceeded) || fresh.Stdout != "P162_COMPLETE_OK\\n" ||
		fresh.OutputComplete == nil || !*fresh.OutputComplete || fresh.CommandID == "" || fresh.JobID == "" || fresh.SessionID == "" {
		t.Fatalf("fresh remote mailbox response=%+v, want complete succeeded result", fresh)
	}
	if events, err := client.ReadEventsThroughCursor(fresh); err != nil || len(events) != 4 || events[2].Text != "P162_COMPLETE_OK\\n" {
		t.Fatalf("fresh remote mailbox event projection events=%+v err=%v", events, err)
	}

	recovered := p163RecoveredArtifacts(t, h.mailboxRoot, backlogIDs)
	if recovered != batchLimit {
		t.Fatalf("first terminal recovery repaired %d/%d receipts, want exactly bounded batch %d", recovered, len(backlogIDs), batchLimit)
	}
	p163AssertArtifactAbsent(t, h.mailboxRoot, tailID)

	// Publish the ACK only after reading the terminal response, exactly as a
	// file-only caller does. The following service cycle must consume it even
	// while old terminal recovery remains backlogged.
	if err := client.WriteAcknowledgment(freshID, fresh); err != nil {
		t.Fatalf("write native mailbox ACK: %v", err)
	}
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	freshRecord := p162Exchange(t, h.authority, freshID)
	if freshRecord.AcknowledgedAt == nil || freshRecord.ResponseCleanupAt == nil {
		t.Fatalf("fresh terminal receipt was not ACKed: %+v", freshRecord)
	}
	for _, path := range []string{
		filepath.Join(h.mailboxRoot, "acks", freshID+mailbox.RequestSuffix),
		filepath.Join(h.mailboxRoot, "acks", freshID+mailbox.ReadySuffix),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ACK pair path=%s err=%v, want consumed", path, err)
		}
	}

	freshIntent := p162IntentForRequest(t, h.authority, h.owner, freshID)
	if caller.mutationCount(string(freshIntent.JobID)) != 1 || caller.totalMutations() != 1 {
		t.Fatalf("terminal recovery or ACK replayed fresh remote work: job_mutations=%d total_mutations=%d", caller.mutationCount(string(freshIntent.JobID)), caller.totalMutations())
	}

	// Advance through all remaining pages. This catches a bounded recovery that
	// keeps rescanning the first current page and never reaches a later missing
	// artifact. No target mutation is allowed during recovery.
	for cycle := 0; cycle < 4; cycle++ {
		// The production scheduler intentionally limits background recovery to
		// one turn per second. Reset only this test's in-memory scheduler clock
		// so the page/cursor contract is exercised without a wall-clock wait.
		h.service.terminalArtifactRecoveryMu.Lock()
		h.service.lastTerminalArtifactRecovery = time.Time{}
		h.service.terminalArtifactRecoveryMu.Unlock()
		h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	}
	if recovered := p163RecoveredArtifacts(t, h.mailboxRoot, backlogIDs); recovered != len(backlogIDs) {
		t.Fatalf("terminal recovery reached %d/%d retained artifacts, want every page including tail", recovered, len(backlogIDs))
	}
	if caller.mutationCount(string(freshIntent.JobID)) != 1 || caller.totalMutations() != 1 {
		t.Fatalf("later terminal recovery replayed fresh remote work: job_mutations=%d total_mutations=%d", caller.mutationCount(string(freshIntent.JobID)), caller.totalMutations())
	}
}

func TestBUG004TerminalArtifactRecoveryScheduleIsBoundedAndRotatesMailboxes(t *testing.T) {
	s := &Service{}
	runtimes := []mailboxRuntime{{id: "alpha"}, {id: "beta"}}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	first, ok := s.nextTerminalArtifactRecoveryRuntime(runtimes, now)
	if !ok || first.id != "alpha" {
		t.Fatalf("first recovery turn=%+v ok=%t, want alpha", first, ok)
	}
	if _, ok := s.nextTerminalArtifactRecoveryRuntime(runtimes, now.Add(terminalArtifactRecoveryInterval-time.Nanosecond)); ok {
		t.Fatal("recovery turn ran before its interval elapsed")
	}
	second, ok := s.nextTerminalArtifactRecoveryRuntime(runtimes, now.Add(terminalArtifactRecoveryInterval))
	if !ok || second.id != "beta" {
		t.Fatalf("second recovery turn=%+v ok=%t, want beta", second, ok)
	}
	third, ok := s.nextTerminalArtifactRecoveryRuntime(runtimes, now.Add(2*terminalArtifactRecoveryInterval))
	if !ok || third.id != "alpha" {
		t.Fatalf("third recovery turn=%+v ok=%t, want alpha after rotation", third, ok)
	}
}

// p163SeedTerminalArtifactBacklog creates durable, valid terminal receipts
// without their derived outbox files. They model the exact crash seam that
// terminal artifact recovery owns. The generic terminal responses intentionally
// carry no command event cursor, so no remote proof or event fixture is needed.
func p163SeedTerminalArtifactBacklog(t *testing.T, h *p162Harness, count int) []string {
	t.Helper()
	if h == nil || h.authority == nil || count < 1 {
		t.Fatal("BUG-004 terminal artifact backlog harness is incomplete")
	}
	ctx := context.Background()
	requestIDs := make([]string, 0, count)
	for index := 0; index < count; index++ {
		requestID := fmt.Sprintf("req-bug004-terminal-%04d", index)
		idempotencyKey := fmt.Sprintf("key-bug004-terminal-%04d", index)
		raw, err := json.Marshal(map[string]any{
			"request_id": requestID, "idempotency_key": idempotencyKey,
			"operation": "run", "script": "printf retained-terminal-receipt",
		})
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
		if err != nil {
			t.Fatalf("canonicalize terminal receipt %s: %v", requestID, err)
		}
		hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
		if err != nil {
			t.Fatalf("hash terminal receipt %s: %v", requestID, err)
		}
		ref, err := store.NewMailboxExchangeRef(p162MailboxID, requestID)
		if err != nil {
			t.Fatal(err)
		}
		record, duplicate, err := h.authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{
			MailboxID: p162MailboxID, RequestID: requestID, Operation: "run", Controller: h.owner,
			IdempotencyKey: idempotencyKey, RequestHash: hash, CanonicalPayload: canonical,
		})
		if err != nil || duplicate || record.State != store.MailboxExchangeAccepted {
			t.Fatalf("accept terminal backlog receipt %s: record=%+v duplicate=%t err=%v", requestID, record, duplicate, err)
		}
		response, err := json.Marshal(map[string]any{
			"request_id": requestID, "operation": "run", "request_state": string(store.MailboxExchangeComplete), "response_revision": 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		record, err = h.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{
			State: store.MailboxExchangeComplete, Bytes: response,
		})
		if err != nil || record.State != store.MailboxExchangeComplete || record.ResponseRevision != 1 {
			t.Fatalf("publish terminal backlog receipt %s: record=%+v err=%v", requestID, record, err)
		}
		p163AssertArtifactAbsent(t, h.mailboxRoot, requestID)
		requestIDs = append(requestIDs, requestID)
	}
	return requestIDs
}

func p163RecoveredArtifacts(t *testing.T, mailboxRoot string, requestIDs []string) int {
	t.Helper()
	count := 0
	for _, requestID := range requestIDs {
		if _, err := os.Stat(filepath.Join(mailboxRoot, "outbox", requestID+mailbox.RequestSuffix)); err == nil {
			count++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect recovered terminal artifact %s: %v", requestID, err)
		}
	}
	return count
}

func p163AssertArtifactAbsent(t *testing.T, mailboxRoot, requestID string) {
	t.Helper()
	path := filepath.Join(mailboxRoot, "outbox", requestID+mailbox.RequestSuffix)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal artifact path=%s err=%v, want absent", path, err)
	}
}
