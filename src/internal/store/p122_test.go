package store

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP122D09ExpiredIdempotencyWarningSurvivesGCAndExpires(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("p122-controller"))
	if err != nil {
		t.Fatal(err)
	}
	hash := p014Hash(t, `{"operation":"submit_command","session_id":"s","script":"x"}`)
	first, duplicate, err := authority.EnsureIdempotency(context.Background(), controller, "p122_test", "p122-expired-key", hash, "resource-before-expiry", time.Minute)
	if err != nil || duplicate || first.DeduplicationWarning {
		t.Fatalf("first key = %+v duplicate=%v err=%v", first, duplicate, err)
	}

	clock.Advance(2 * time.Minute)
	if report, err := authority.CollectGarbage(context.Background(), GarbageCollectionOptions{}); err != nil || report.IdempotencyRecordsDeleted != 1 {
		t.Fatalf("first GC report=%+v err=%v, want one expired mapping removed", report, err)
	}
	var fingerprint []byte
	if err := authority.db.QueryRowContext(context.Background(), `
SELECT key_fingerprint FROM exec_idempotency_expiry_warnings
WHERE controller_type = ? AND controller_id = ? AND operation = ?
`, string(controller.Type()), string(controller.ID()), "p122_test").Scan(&fingerprint); err != nil {
		t.Fatalf("read expired key fingerprint: %v", err)
	}
	if len(fingerprint) != sha256.Size {
		t.Fatalf("expired key fingerprint length = %d, want %d", len(fingerprint), sha256.Size)
	}

	reused, duplicate, err := authority.EnsureIdempotency(context.Background(), controller, "p122_test", "p122-expired-key", hash, "resource-after-expiry", time.Minute)
	if err != nil || duplicate || reused.ResourceID != "resource-after-expiry" || !reused.DeduplicationWarning {
		t.Fatalf("expired-key reuse = %+v duplicate=%v err=%v", reused, duplicate, err)
	}
	replay, duplicate, err := authority.EnsureIdempotency(context.Background(), controller, "p122_test", "p122-expired-key", hash, "ignored-resource", time.Minute)
	if err != nil || !duplicate || replay.ResourceID != reused.ResourceID || !replay.DeduplicationWarning {
		t.Fatalf("warned active replay = %+v duplicate=%v err=%v", replay, duplicate, err)
	}

	cleanupKey := "p122-warning-window-cleanup"
	if _, _, err := authority.EnsureIdempotency(context.Background(), controller, "p122_test", cleanupKey, hash, "cleanup-resource", time.Minute); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := authority.CollectGarbage(context.Background(), GarbageCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(DefaultMetadataRetention + time.Second)
	if _, err := authority.CollectGarbage(context.Background(), GarbageCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	cleaned, duplicate, err := authority.EnsureIdempotency(context.Background(), controller, "p122_test", cleanupKey, hash, "cleanup-resource-after-window", time.Minute)
	if err != nil || duplicate || cleaned.DeduplicationWarning {
		t.Fatalf("reuse after warning retention = %+v duplicate=%v err=%v, want no stale warning", cleaned, duplicate, err)
	}
}
