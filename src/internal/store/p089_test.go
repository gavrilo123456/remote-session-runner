package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP089MailboxAckRequiresExactTerminalRevisionAndCursor(t *testing.T) {
	root := testfixture.New(t)
	dbPath := filepath.Join(root.Path(), "state", "ack.db")
	db, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	raw := p082RunPayload(t, "req-p089", "key-p089", "echo p089")
	hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p089", Operation: "run", Controller: controller, IdempotencyKey: "key-p089", RequestHash: hash, CanonicalPayload: canonical, ResourceID: "job-p089"}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AcknowledgeMailboxExchange(context.Background(), MailboxAcknowledgement{RequestID: "req-p089", ResponseRevision: 1}); !errors.Is(err, ErrMailboxAckConflict) {
		t.Fatalf("preterminal ACK error=%v", err)
	}
	cursor := int64(3)
	response := []byte(`{"request_id":"req-p089","request_state":"complete","response_revision":1,"available_event_sequence":3,"output_complete":false,"output_unavailable_reason":"remote_event_gap"}`)
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-p089", MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: response, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}
	ack := MailboxAcknowledgement{RequestID: "req-p089", ResponseRevision: 1, AvailableEventSequence: &cursor}
	recorded, err := authority.AcknowledgeMailboxExchange(context.Background(), ack)
	if err != nil || recorded.AcknowledgedAt == nil {
		t.Fatalf("recorded ACK=%+v err=%v", recorded, err)
	}
	ackTime := *recorded.AcknowledgedAt
	duplicate, err := authority.AcknowledgeMailboxExchange(context.Background(), ack)
	if err != nil || duplicate.AcknowledgedAt == nil || !duplicate.AcknowledgedAt.Equal(ackTime) {
		t.Fatalf("duplicate ACK=%+v err=%v", duplicate, err)
	}
	wrongRevision := ack
	wrongRevision.ResponseRevision = 2
	if _, err := authority.AcknowledgeMailboxExchange(context.Background(), wrongRevision); !errors.Is(err, ErrMailboxAckConflict) {
		t.Fatalf("wrong revision error=%v", err)
	}
	wrongCursor := ack
	wrongCursorValue := int64(2)
	wrongCursor.AvailableEventSequence = &wrongCursorValue
	if _, err := authority.AcknowledgeMailboxExchange(context.Background(), wrongCursor); !errors.Is(err, ErrMailboxAckConflict) {
		t.Fatalf("wrong cursor error=%v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStore(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	reopenedRecord, err := reopened.GetMailboxExchange(context.Background(), "req-p089")
	if err != nil || reopenedRecord.AcknowledgedAt == nil || !reopenedRecord.AcknowledgedAt.Equal(ackTime) {
		t.Fatalf("reopened ACK=%+v err=%v", reopenedRecord, err)
	}
}

func TestP089AckWithoutEventCursorAndInvalidValues(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), filepath.Join(root.Path(), "state", "ack-no-cursor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	raw := p082RunPayload(t, "req-p089-no-cursor", "key-p089-no-cursor", "true")
	hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p089-no-cursor", Operation: "run", Controller: controller, IdempotencyKey: "key-p089-no-cursor", RequestHash: hash, CanonicalPayload: canonical}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-p089-no-cursor", MailboxResponsePublication{State: MailboxExchangeRejected, Bytes: []byte(`{"request_state":"rejected","response_revision":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AcknowledgeMailboxExchange(context.Background(), MailboxAcknowledgement{RequestID: "req-p089-no-cursor", ResponseRevision: 1}); err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	if _, err := authority.AcknowledgeMailboxExchange(context.Background(), MailboxAcknowledgement{RequestID: "req-p089-no-cursor", ResponseRevision: 1, AvailableEventSequence: &negative}); !errors.Is(err, ErrMailboxAckInvalid) {
		t.Fatalf("negative ACK cursor error=%v", err)
	}
}
