package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

func TestP127LocalUnixAndMailboxActionsRecordTrustedIngress(t *testing.T) {
	h := newP095Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	importRequest := func(requestID string, request map[string]any) p095Response {
		t.Helper()
		writeP094Request(t, h.importer, requestID, request)
		results, err := h.processor.Import(context.Background())
		if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
			t.Fatalf("mailbox import %s results=%+v err=%v", requestID, results, err)
		}
		return readP095Response(t, h.outbox, requestID)
	}
	createdMailbox := importRequest("req-p127-mailbox-create", map[string]any{
		"request_id": "req-p127-mailbox-create", "idempotency_key": "key-p127-mailbox-create", "operation": "create_session",
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source": map[string]string{"mode": "empty"},
	})
	if createdMailbox.RequestState != "accepted" || createdMailbox.SessionID == "" {
		t.Fatalf("mailbox create acknowledgement=%+v", createdMailbox)
	}

	const script = "printf 'mailbox-raw-script-marker'"
	submitted := importRequest("req-p127-mailbox-submit", map[string]any{
		"request_id": "req-p127-mailbox-submit", "idempotency_key": "key-p127-mailbox-submit",
		"operation": "submit_command", "session_id": sessionID, "script": script, "timeout_seconds": 30,
	})
	if submitted.RequestState != "accepted" || submitted.CommandID == "" {
		t.Fatalf("mailbox submit acknowledgement=%+v", submitted)
	}
	cancelled := importRequest("req-p127-mailbox-cancel", map[string]any{
		"request_id": "req-p127-mailbox-cancel", "idempotency_key": "key-p127-mailbox-cancel",
		"operation": "cancel_command", "command_id": submitted.CommandID,
	})
	if cancelled.RequestState != "accepted" {
		t.Fatalf("mailbox cancel acknowledgement=%+v", cancelled)
	}
	closed := importRequest("req-p127-mailbox-close", map[string]any{
		"request_id": "req-p127-mailbox-close", "idempotency_key": "key-p127-mailbox-close",
		"operation": "close_session", "session_id": sessionID,
	})
	if closed.RequestState != "accepted" {
		t.Fatalf("mailbox close acknowledgement=%+v", closed)
	}

	localCreate := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(localCreate))
	request.Header.Set("Idempotency-Key", "key-p127-local-unix-create")
	response := httptest.NewRecorder()
	h.server.serveHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("local Unix create status=%d body=%s", response.Code, response.Body.String())
	}

	records, err := h.authority.ListAuditRecords(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	want := map[audit.Action]audit.Ingress{
		audit.ActionCreate: audit.IngressMailbox,
		audit.ActionSubmit: audit.IngressMailbox,
		audit.ActionCancel: audit.IngressMailbox,
		audit.ActionClose:  audit.IngressMailbox,
	}
	seenMailbox := make(map[audit.Action]bool)
	seenUnixCreate := false
	for _, record := range records {
		if record.Ingress == audit.IngressLocalUnix && record.Action == audit.ActionCreate && record.Outcome == audit.OutcomeAllowed {
			seenUnixCreate = true
		}
		if ingress, ok := want[record.Action]; ok && record.Ingress == ingress && record.Outcome == audit.OutcomeAllowed {
			seenMailbox[record.Action] = true
		}
		if record.Principal.ID() != h.server.owner.ID() {
			t.Errorf("audit principal = %s, want local owner", record.Principal.ID())
		}
	}
	if !seenUnixCreate {
		t.Fatalf("audit rows omitted allowed local Unix create: %+v", records)
	}
	for action := range want {
		if !seenMailbox[action] {
			t.Errorf("audit rows omitted allowed mailbox %s: %+v", action, records)
		}
	}
	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "mailbox-raw-script-marker") {
		t.Fatal("audit rows included the raw submitted script")
	}
}
