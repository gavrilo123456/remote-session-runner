package mailbox

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/store"
)

func TestP164FrozenIngressDiagnosticMatchesV1Schema(t *testing.T) {
	ctx := context.Background()
	root := p081MailboxRoot(t)
	database, err := store.Open(ctx, filepath.Join(root, "state", "p164-diagnostic-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	observedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	authority, err := store.NewAuthorityStoreWithClock(database, func() time.Time { return observedAt })
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewMailboxIngressDiagnosticRef("slidestud-io", "req-p164-schema")
	if err != nil {
		t.Fatal(err)
	}
	record, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(
		ctx,
		ref,
		sha256.Sum256([]byte("bounded-invalid-request-fingerprint")),
		store.MailboxIngressDiagnosticInvalidRequestSchema,
	)
	if err != nil || disposition != store.MailboxIngressDiagnosticCreated {
		t.Fatalf("record diagnostic disposition=%q err=%v", disposition, err)
	}
	value, err := p004MailboxDecode(record.DiagnosticBytes)
	if err != nil {
		t.Fatalf("decode frozen diagnostic: %v", err)
	}
	if err := p004MailboxSchemas(t)["diagnostic"].Validate(value); err != nil {
		t.Fatalf("frozen diagnostic does not match v1 schema: %v", err)
	}
}
