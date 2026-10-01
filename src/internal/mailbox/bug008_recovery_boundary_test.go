package mailbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// The online retained-capacity repair is an owner-only runnerd command. It
// must remain outside the file mailbox's request schema and must never invoke
// a mailbox handler.
func TestBUG008RecoverRetainedCapacityIsNotAMailboxOperation(t *testing.T) {
	for _, operation := range []string{"recover_retained_capacity", "recover-retained-capacity"} {
		t.Run(operation, func(t *testing.T) {
			root := p081MailboxRoot(t)
			handled := false
			importer, err := NewImporter(root, func(context.Context, Request) error {
				handled = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			requestID := "req-bug008-" + operation
			raw := []byte(fmt.Sprintf(`{"request_id":%q,"idempotency_key":"key-bug008","operation":%q}`, requestID, operation))
			if _, err := importer.validateRequest(requestID, raw); !errors.Is(err, ErrMailboxSchema) {
				t.Fatalf("validate unknown mailbox operation error=%v, want schema rejection", err)
			}
			writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+RequestSuffix), raw, MailboxFileMode)
			writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix), nil, MailboxFileMode)
			results, err := importer.Import(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Status != ResultRejected || handled {
				t.Fatalf("unknown mailbox operation results=%+v handler_called=%t", results, handled)
			}
		})
	}
}
