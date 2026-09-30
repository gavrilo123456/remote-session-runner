package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

type p102Harness struct {
	mailbox *p095Harness
	db      *sql.DB
	client  *http.Client
}

func newP102Harness(t *testing.T) *p102Harness {
	t.Helper()
	server, authority, db, client := p063Server(t)
	root := filepath.Join(t.TempDir(), "mailbox")
	importer, err := mailbox.NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := mailbox.NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: importer, Authority: authority, Controller: p063Owner(t), Operations: server,
		Outbox: outbox, EventFiles: eventFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &p095Harness{server: server, authority: authority, importer: importer, outbox: outbox, eventFiles: eventFiles, processor: processor}
	return &p102Harness{mailbox: h, db: db, client: client}
}

func p102API(t *testing.T, h *p102Harness, method, path, key, body string) (int, []byte) {
	t.Helper()
	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, "http://local"+path, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", key)
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func p102AssertIntentCount(t *testing.T, h *p102Harness, operation, key string, want int) {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM local_intents WHERE operation = ? AND idempotency_key = ?`, operation, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s intent rows for key %q = %d, want %d", operation, key, count, want)
	}
}

func p102Decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode API response %s: %v", data, err)
	}
	return value
}

func p102AssertAPIConflict(t *testing.T, h *p102Harness, method, path, key, body string) {
	t.Helper()
	status, data := p102API(t, h, method, path, key, body)
	if status != http.StatusConflict {
		t.Fatalf("changed %s request status/body = %d/%s, want 409", method, status, data)
	}
	failure := p102Decode[errorEnvelope](t, data)
	if failure.Code != "idempotency_conflict" {
		t.Fatalf("changed %s request error = %+v, want idempotency_conflict", method, failure)
	}
}

func p102AssertMailboxConflict(t *testing.T, h *p102Harness, requestID string, request map[string]any) {
	t.Helper()
	p101Import(t, h.mailbox, requestID, request)
	response := p101ReadResponse(t, h.mailbox, requestID)
	if response.RequestState != string(store.MailboxExchangeRejected) || response.Error == nil || response.Error.Code != "idempotency_conflict" {
		t.Fatalf("changed mailbox request response=%+v, want idempotency_conflict", response)
	}
}

func p102OneIntent(t *testing.T, h *p102Harness, operation, key string) store.LocalIntentRecord {
	t.Helper()
	intent, err := h.mailbox.authority.GetLocalIntentByIdempotency(context.Background(), operation, key, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p102AssertIntentCount(t, h, operation, key, 1)
	return intent
}

// p102MailboxIntent resolves the trusted execution identity chosen by the
// durable mailbox receipt. The client key stays in the mailbox response and
// receipt, while local intent creation uses this scoped key.
func p102MailboxIntent(t *testing.T, h *p102Harness, operation, requestID string) (store.MailboxExchangeRecord, store.LocalIntentRecord) {
	t.Helper()
	ref, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := h.mailbox.authority.GetMailboxExchangeInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ExecutionIdempotencyKey == "" || receipt.ExecutionIdempotencyKey == receipt.IdempotencyKey {
		t.Fatalf("mailbox receipt did not retain a scoped execution key: %+v", receipt)
	}
	intent, err := h.mailbox.authority.GetLocalIntentByIdempotency(context.Background(), operation, receipt.ExecutionIdempotencyKey, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p102AssertIntentCount(t, h, operation, receipt.ExecutionIdempotencyKey, 1)
	return receipt, intent
}

func TestP102M08D05CreateSessionCrossIngressIdempotencyMatrix(t *testing.T) {
	for _, first := range []string{"unix", "mailbox"} {
		t.Run(first+" first", func(t *testing.T) {
			h := newP102Harness(t)
			key := "key-p102-create-" + first
			requestID := "req-p102-create-" + first
			mailboxCreate := map[string]any{
				"request_id": requestID, "idempotency_key": key, "operation": "create_session",
				"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
				"source": map[string]string{"mode": "empty"},
			}
			apiBody := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`
			var original sessionAcceptance
			if first == "unix" {
				status, data := p102API(t, h, http.MethodPost, "/v1/sessions", key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("create status/body = %d/%s", status, data)
				}
				original = p102Decode[sessionAcceptance](t, data)
				p101Import(t, h.mailbox, requestID, mailboxCreate)
			} else {
				p101Import(t, h.mailbox, requestID, mailboxCreate)
				status, data := p102API(t, h, http.MethodPost, "/v1/sessions", key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("create cross-ingress replay status/body = %d/%s", status, data)
				}
				original = p102Decode[sessionAcceptance](t, data)
			}
			mailboxResponse := p101ReadResponse(t, h.mailbox, requestID)
			if original.SessionID == "" || original.SessionID != original.ResourceID || original.IntentID == "" ||
				mailboxResponse.SessionID == "" || mailboxResponse.SessionID == original.SessionID || mailboxResponse.RequestState != string(store.MailboxExchangeAccepted) {
				t.Fatalf("create execution identities were not isolated: api=%+v mailbox=%+v", original, mailboxResponse)
			}
			_, mailboxIntent := p102MailboxIntent(t, h, "create_session", requestID)
			if string(mailboxIntent.SessionID) != mailboxResponse.SessionID || mailboxIntent.IntentID == domain.IntentID(original.IntentID) {
				t.Fatalf("mailbox create intent was not isolated: api=%+v mailbox=%+v", original, mailboxIntent)
			}

			// Omitted API source and explicit mailbox source canonicalize to the same request.
			apiRetryBody := `{"source":{"mode":"empty"},"execution_target":{"profile":"mac-workstation","kind":"local"},"environment":"mac-dev"}`
			status, retryData := p102API(t, h, http.MethodPost, "/v1/sessions", key, apiRetryBody)
			if status != http.StatusAccepted {
				t.Fatalf("canonical create retry status/body = %d/%s", status, retryData)
			}
			retry := p102Decode[sessionAcceptance](t, retryData)
			if retry.SessionID != original.SessionID || retry.IntentID != original.IntentID {
				t.Fatalf("create retry changed identity: original=%+v retry=%+v", original, retry)
			}
			mailboxRetryID := requestID + "-retry"
			mailboxCreate["request_id"] = mailboxRetryID
			p101Import(t, h.mailbox, mailboxRetryID, mailboxCreate)
			if got := p101ReadResponse(t, h.mailbox, mailboxRetryID); got.SessionID != mailboxResponse.SessionID || got.RequestState != string(store.MailboxExchangeAccepted) {
				t.Fatalf("mailbox create retry=%+v, mailbox session=%s", got, mailboxResponse.SessionID)
			}

			p102AssertAPIConflict(t, h, http.MethodPost, "/v1/sessions", key,
				`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"other-local-profile"},"source":{"mode":"empty"}}`)
			changedMailbox := map[string]any{
				"request_id": requestID + "-changed", "idempotency_key": key, "operation": "create_session",
				"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "other-local-profile"},
				"source": map[string]string{"mode": "empty"},
			}
			p102AssertMailboxConflict(t, h, requestID+"-changed", changedMailbox)
			intent := p102OneIntent(t, h, "create_session", key)
			if string(intent.SessionID) != original.SessionID || intent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("durable create intent=%+v, original=%+v", intent, original)
			}
			var authoritySessions int
			if err := h.db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&authoritySessions); err != nil {
				t.Fatal(err)
			}
			if authoritySessions != 0 {
				t.Fatalf("create replay made %d authoritative sessions", authoritySessions)
			}
		})
	}
}

func TestP102M08D05SubmitCommandCrossIngressIdempotencyMatrix(t *testing.T) {
	for _, first := range []string{"unix", "mailbox"} {
		t.Run(first+" first", func(t *testing.T) {
			h := newP102Harness(t)
			sessionID := p064CreateSession(t, h.client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p102-submit-session-"+first)
			key := "key-p102-submit-" + first
			requestID := "req-p102-submit-" + first
			path := "/v1/sessions/" + sessionID + "/commands"
			apiBody := `{"script":"printf p102-submit","timeout_seconds":30}`
			mailboxSubmit := map[string]any{
				"request_id": requestID, "idempotency_key": key, "operation": "submit_command",
				"session_id": sessionID, "script": "printf p102-submit", "timeout_seconds": 30,
			}
			var original commandAcceptance
			if first == "unix" {
				status, data := p102API(t, h, http.MethodPost, path, key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("submit status/body = %d/%s", status, data)
				}
				original = p102Decode[commandAcceptance](t, data)
				p101Import(t, h.mailbox, requestID, mailboxSubmit)
			} else {
				p101Import(t, h.mailbox, requestID, mailboxSubmit)
				status, data := p102API(t, h, http.MethodPost, path, key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("submit cross-ingress replay status/body = %d/%s", status, data)
				}
				original = p102Decode[commandAcceptance](t, data)
			}
			mailboxResponse := p101ReadResponse(t, h.mailbox, requestID)
			if original.CommandID == "" || original.CommandID != original.ResourceID || original.SessionID != sessionID || original.IntentID == "" ||
				mailboxResponse.CommandID == "" || mailboxResponse.CommandID == original.CommandID || mailboxResponse.SessionID != sessionID {
				t.Fatalf("submit execution identities were not isolated: api=%+v mailbox=%+v", original, mailboxResponse)
			}
			_, mailboxIntent := p102MailboxIntent(t, h, "submit_command", requestID)
			if string(mailboxIntent.CommandID) != mailboxResponse.CommandID || string(mailboxIntent.SessionID) != sessionID || mailboxIntent.IntentID == domain.IntentID(original.IntentID) {
				t.Fatalf("mailbox submit intent was not isolated: api=%+v mailbox=%+v", original, mailboxIntent)
			}
			status, retryData := p102API(t, h, http.MethodPost, path, key, `{"timeout_seconds":30,"script":"printf p102-submit"}`)
			if status != http.StatusAccepted {
				t.Fatalf("canonical submit retry status/body = %d/%s", status, retryData)
			}
			retry := p102Decode[commandAcceptance](t, retryData)
			if retry.CommandID != original.CommandID || retry.IntentID != original.IntentID {
				t.Fatalf("submit retry changed identity: original=%+v retry=%+v", original, retry)
			}
			mailboxRetryID := requestID + "-retry"
			mailboxSubmit["request_id"] = mailboxRetryID
			p101Import(t, h.mailbox, mailboxRetryID, mailboxSubmit)
			if got := p101ReadResponse(t, h.mailbox, mailboxRetryID); got.CommandID != mailboxResponse.CommandID || got.RequestState != string(store.MailboxExchangeAccepted) {
				t.Fatalf("mailbox submit retry=%+v, mailbox command=%s", got, mailboxResponse.CommandID)
			}

			p102AssertAPIConflict(t, h, http.MethodPost, path, key, `{"script":"printf p102-submit","timeout_seconds":31}`)
			changedMailbox := map[string]any{
				"request_id": requestID + "-changed", "idempotency_key": key, "operation": "submit_command",
				"session_id": sessionID, "script": "printf changed", "timeout_seconds": 30,
			}
			p102AssertMailboxConflict(t, h, requestID+"-changed", changedMailbox)
			intent := p102OneIntent(t, h, "submit_command", key)
			if string(intent.CommandID) != original.CommandID || intent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("durable submit intent=%+v, original=%+v", intent, original)
			}
			var authorityCommands int
			if err := h.db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&authorityCommands); err != nil {
				t.Fatal(err)
			}
			if authorityCommands != 0 {
				t.Fatalf("submit replay created %d authoritative commands", authorityCommands)
			}
		})
	}
}

func TestP102M08D05CancelCommandCrossIngressIdempotencyMatrix(t *testing.T) {
	for _, first := range []string{"unix", "mailbox"} {
		t.Run(first+" first", func(t *testing.T) {
			h := newP102Harness(t)
			sessionID := p064CreateSession(t, h.client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p102-cancel-session-"+first)
			commandID := p066SubmitCommand(t, h.client, sessionID, "key-p102-cancel-target-"+first)
			otherCommandID := p066SubmitCommand(t, h.client, sessionID, "key-p102-cancel-other-"+first)
			key := "key-p102-cancel-" + first
			requestID := "req-p102-cancel-" + first
			path := "/v1/commands/" + commandID + "/cancel"
			apiBody := `{"command_id":"` + commandID + `"}`
			mailboxCancel := map[string]any{
				"request_id": requestID, "idempotency_key": key, "operation": "cancel_command", "command_id": commandID,
			}
			var original commandAcceptance
			if first == "unix" {
				status, data := p102API(t, h, http.MethodPost, path, key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("cancel status/body = %d/%s", status, data)
				}
				original = p102Decode[commandAcceptance](t, data)
				p101Import(t, h.mailbox, requestID, mailboxCancel)
			} else {
				p101Import(t, h.mailbox, requestID, mailboxCancel)
				status, data := p102API(t, h, http.MethodPost, path, key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("cancel cross-ingress replay status/body = %d/%s", status, data)
				}
				original = p102Decode[commandAcceptance](t, data)
			}
			mailboxResponse := p101ReadResponse(t, h.mailbox, requestID)
			if original.CommandID != commandID || original.SessionID != sessionID || original.IntentID == "" || mailboxResponse.CommandID != commandID || mailboxResponse.SessionID != sessionID {
				t.Fatalf("cancel Unix/mailbox outcomes differ: api=%+v mailbox=%+v", original, mailboxResponse)
			}
			status, retryData := p102API(t, h, http.MethodPost, path, key, `{}`)
			if status != http.StatusAccepted {
				t.Fatalf("canonical cancel retry status/body = %d/%s", status, retryData)
			}
			retry := p102Decode[commandAcceptance](t, retryData)
			if retry.CommandID != original.CommandID || retry.IntentID != original.IntentID {
				t.Fatalf("cancel retry changed identity: original=%+v retry=%+v", original, retry)
			}
			mailboxRetryID := requestID + "-retry"
			mailboxCancel["request_id"] = mailboxRetryID
			p101Import(t, h.mailbox, mailboxRetryID, mailboxCancel)
			if got := p101ReadResponse(t, h.mailbox, mailboxRetryID); got.CommandID != original.CommandID || got.RequestState != string(store.MailboxExchangeAccepted) {
				t.Fatalf("mailbox cancel retry=%+v, original command=%s", got, original.CommandID)
			}

			p102AssertAPIConflict(t, h, http.MethodPost, path, key, `{"reason":"changed"}`)
			changedPath := "/v1/commands/" + otherCommandID + "/cancel"
			changedMailbox := map[string]any{
				"request_id": requestID + "-changed", "idempotency_key": key, "operation": "cancel_command", "command_id": otherCommandID,
			}
			p102AssertMailboxConflict(t, h, requestID+"-changed", changedMailbox)
			// Changing the resource path also changes the API's canonical command ID.
			p102AssertAPIConflict(t, h, http.MethodPost, changedPath, key, `{}`)
			intent := p102OneIntent(t, h, "cancel_command", key)
			if string(intent.CommandID) != commandID || string(intent.SessionID) != sessionID || intent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("durable cancel intent=%+v, original=%+v", intent, original)
			}
		})
	}
}

func TestP102M08D05CloseSessionPolicyCrossIngressIdempotencyMatrix(t *testing.T) {
	for _, first := range []string{"unix", "mailbox"} {
		t.Run(first+" first", func(t *testing.T) {
			h := newP102Harness(t)
			target := p101Target{name: "local", kind: string(domain.TargetKindLocal), profile: "mac-workstation", environment: "mac-dev"}
			sessionID := h.mailbox.createSession(t, domain.TargetKindLocal, target.profile, target.environment, true)
			key := "key-p102-close-" + first
			requestID := "req-p102-close-" + first
			apiPath := "/v1/sessions/" + sessionID + "?mode=cancel"
			mailboxClose := map[string]any{
				"request_id": requestID, "idempotency_key": key, "operation": "close_session", "session_id": sessionID,
			}
			var original closeAcceptance
			if first == "unix" {
				status, data := p102API(t, h, http.MethodDelete, apiPath, key, "")
				if status != http.StatusAccepted {
					t.Fatalf("close status/body = %d/%s", status, data)
				}
				original = p102Decode[closeAcceptance](t, data)
				p101Import(t, h.mailbox, requestID, mailboxClose)
			} else {
				p101Import(t, h.mailbox, requestID, mailboxClose)
				status, data := p102API(t, h, http.MethodDelete, apiPath, key, "")
				if status != http.StatusAccepted {
					t.Fatalf("close cross-ingress replay status/body = %d/%s", status, data)
				}
				original = p102Decode[closeAcceptance](t, data)
			}
			mailboxResponse := p101ReadResponse(t, h.mailbox, requestID)
			if original.SessionID != sessionID || original.IntentID == "" || mailboxResponse.SessionID != sessionID {
				t.Fatalf("close Unix/mailbox outcomes differ: api=%+v mailbox=%+v", original, mailboxResponse)
			}
			status, retryData := p102API(t, h, http.MethodDelete, "/v1/sessions/"+sessionID, key, `{"policy":"cancel"}`)
			if status != http.StatusAccepted {
				t.Fatalf("canonical close retry status/body = %d/%s", status, retryData)
			}
			retry := p102Decode[closeAcceptance](t, retryData)
			if retry.SessionID != original.SessionID || retry.IntentID != original.IntentID {
				t.Fatalf("close retry changed identity: original=%+v retry=%+v", original, retry)
			}
			mailboxRetryID := requestID + "-retry"
			mailboxClose["request_id"] = mailboxRetryID
			p101Import(t, h.mailbox, mailboxRetryID, mailboxClose)
			if got := p101ReadResponse(t, h.mailbox, mailboxRetryID); got.SessionID != original.SessionID || got.RequestState != string(store.MailboxExchangeAccepted) {
				t.Fatalf("mailbox close retry=%+v, original session=%s", got, original.SessionID)
			}

			p102AssertAPIConflict(t, h, http.MethodDelete, "/v1/sessions/"+sessionID+"?mode=drain", key, "")
			changedMailbox := map[string]any{
				"request_id": requestID + "-changed", "idempotency_key": key, "operation": "close_session",
				"session_id": sessionID, "close_policy": map[string]string{"policy": "drain"},
			}
			p102AssertMailboxConflict(t, h, requestID+"-changed", changedMailbox)

			// The one target close is durable; repeated Unix and mailbox acceptance must not add another close transition.
			p101CloseTarget(t, h.mailbox, target, sessionID)
			status, data := p102API(t, h, http.MethodDelete, apiPath, key, "")
			if status != http.StatusAccepted || p102Decode[closeAcceptance](t, data).IntentID != original.IntentID {
				t.Fatalf("post-close API retry status/body = %d/%s", status, data)
			}
			postCloseID := requestID + "-post-close"
			mailboxClose["request_id"] = postCloseID
			p101Import(t, h.mailbox, postCloseID, mailboxClose)
			if got := p101ReadResponse(t, h.mailbox, postCloseID); got.SessionID != sessionID {
				t.Fatalf("post-close mailbox retry=%+v", got)
			}
			lifecycle, err := h.mailbox.authority.ListSessionLifecycle(context.Background(), domain.SessionID(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			closing, closed := 0, 0
			for _, record := range lifecycle {
				if record.NewState == domain.SessionStateClosing {
					closing++
				}
				if record.NewState == domain.SessionStateClosed {
					closed++
				}
			}
			if closing != 1 || closed != 1 {
				t.Fatalf("close side effects recorded closing=%d closed=%d, want one each; lifecycle=%+v", closing, closed, lifecycle)
			}
			intent := p102OneIntent(t, h, "close_session", key)
			if string(intent.SessionID) != sessionID || intent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("durable close intent=%+v, original=%+v", intent, original)
			}
		})
	}
}

func TestP102M08D05RunCrossIngressIdempotencyMatrixAndMailboxScopedExecution(t *testing.T) {
	for _, first := range []string{"unix", "mailbox"} {
		t.Run(first+" first", func(t *testing.T) {
			h := newP102Harness(t)
			key := "key-p102-run-" + first
			requestID := "req-p102-run-" + first
			mailboxRun := p100RunRequest(requestID, key, "local", "mac-workstation", "mac-dev", "printf p102-once")
			apiBody := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf p102-once","timeout_seconds":30}`
			var original jobAcceptance
			if first == "unix" {
				status, data := p102API(t, h, http.MethodPost, "/v1/jobs", key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("run status/body = %d/%s", status, data)
				}
				original = p102Decode[jobAcceptance](t, data)
				p101Import(t, h.mailbox, requestID, mailboxRun)
			} else {
				p101Import(t, h.mailbox, requestID, mailboxRun)
				status, data := p102API(t, h, http.MethodPost, "/v1/jobs", key, apiBody)
				if status != http.StatusAccepted {
					t.Fatalf("run cross-ingress replay status/body = %d/%s", status, data)
				}
				original = p102Decode[jobAcceptance](t, data)
			}
			mailboxResponse := p101ReadResponse(t, h.mailbox, requestID)
			if original.JobID == "" || original.JobID != original.ResourceID || original.SessionID == "" || original.CommandID == "" || original.IntentID == "" ||
				mailboxResponse.JobID == "" || mailboxResponse.SessionID == "" || mailboxResponse.CommandID == "" ||
				mailboxResponse.JobID == original.JobID || mailboxResponse.SessionID == original.SessionID || mailboxResponse.CommandID == original.CommandID {
				t.Fatalf("run execution identities were not isolated: api=%+v mailbox=%+v", original, mailboxResponse)
			}

			apiIntent := p102OneIntent(t, h, "run", key)
			if apiIntent.JobID != domain.JobID(original.JobID) || apiIntent.SessionID != domain.SessionID(original.SessionID) || apiIntent.CommandID != domain.CommandID(original.CommandID) || apiIntent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("Unix run intent=%+v, original=%+v", apiIntent, original)
			}
			_, mailboxIntent := p102MailboxIntent(t, h, "run", requestID)
			if mailboxIntent.JobID != domain.JobID(mailboxResponse.JobID) || mailboxIntent.SessionID != domain.SessionID(mailboxResponse.SessionID) || mailboxIntent.CommandID != domain.CommandID(mailboxResponse.CommandID) || mailboxIntent.IntentID == apiIntent.IntentID {
				t.Fatalf("mailbox run intent was not isolated: api=%+v mailbox=%+v", apiIntent, mailboxIntent)
			}
			p101SetIntentDelivery(t, h.mailbox, mailboxIntent, store.LocalIntentAccepted)
			p101CompleteLocalRun(t, h.mailbox, mailboxIntent, "p102-once\n")
			if err := h.mailbox.processor.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			terminal := p101ReadResponse(t, h.mailbox, requestID)
			if terminal.RequestState != string(store.MailboxExchangeComplete) || terminal.JobID != mailboxResponse.JobID || terminal.SessionID != mailboxResponse.SessionID || terminal.CommandID != mailboxResponse.CommandID || terminal.CommandState != string(domain.CommandStateSucceeded) || terminal.TeardownOutcome != "closed" {
				t.Fatalf("completed mailbox run response=%+v", terminal)
			}

			// Each ingress replays its own acceptance; the mailbox execution is
			// scoped away from the unrelated Unix-socket request with the same key.
			apiRetryBody := `{"source":{"mode":"empty"},"script":"printf p102-once","timeout_seconds":30,"execution_target":{"profile":"mac-workstation","kind":"local"},"environment":"mac-dev"}`
			status, retryData := p102API(t, h, http.MethodPost, "/v1/jobs", key, apiRetryBody)
			if status != http.StatusAccepted {
				t.Fatalf("completed run API retry status/body = %d/%s", status, retryData)
			}
			retry := p102Decode[jobAcceptance](t, retryData)
			if retry.JobID != original.JobID || retry.SessionID != original.SessionID || retry.CommandID != original.CommandID || retry.IntentID != original.IntentID {
				t.Fatalf("completed run retry changed identity: original=%+v retry=%+v", original, retry)
			}
			mailboxRetryID := requestID + "-retry"
			mailboxRun["request_id"] = mailboxRetryID
			p101Import(t, h.mailbox, mailboxRetryID, mailboxRun)
			if err := h.mailbox.processor.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			mailboxTerminal := p101ReadResponse(t, h.mailbox, mailboxRetryID)
			if mailboxTerminal.RequestState != string(store.MailboxExchangeComplete) || mailboxTerminal.JobID != mailboxResponse.JobID || mailboxTerminal.SessionID != mailboxResponse.SessionID || mailboxTerminal.CommandID != mailboxResponse.CommandID || mailboxTerminal.CommandState != terminal.CommandState || mailboxTerminal.TeardownOutcome != terminal.TeardownOutcome {
				t.Fatalf("completed mailbox retry=%+v, mailbox original=%+v", mailboxTerminal, terminal)
			}

			p102AssertAPIConflict(t, h, http.MethodPost, "/v1/jobs", key,
				`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf changed","timeout_seconds":30}`)
			changedMailbox := p100RunRequest(requestID+"-changed", key, "local", "mac-workstation", "mac-dev", "printf changed")
			p102AssertMailboxConflict(t, h, requestID+"-changed", changedMailbox)
			apiIntent = p102OneIntent(t, h, "run", key)
			if apiIntent.JobID != domain.JobID(original.JobID) || apiIntent.IntentID != domain.IntentID(original.IntentID) {
				t.Fatalf("Unix run retry changed durable intent: %+v", apiIntent)
			}
			command, events, err := h.mailbox.authority.GetCommandWithEvents(context.Background(), domain.CommandID(mailboxResponse.CommandID))
			if err != nil || command.State != domain.CommandStateSucceeded {
				t.Fatalf("one-off command=%+v err=%v", command, err)
			}
			starts := 0
			for _, event := range events {
				if event.Type == "command_started" {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("mailbox retries recorded %d command starts, want exactly one", starts)
			}
			if _, err := h.mailbox.authority.GetJob(context.Background(), domain.JobID(mailboxResponse.JobID)); err != nil {
				t.Fatalf("run job after replay: %v", err)
			}
			var jobs, sessions, commands int
			if err := h.db.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&commands); err != nil {
				t.Fatal(err)
			}
			if jobs != 1 || sessions != 1 || commands != 1 {
				t.Fatalf("completed run created duplicate authority resources: jobs=%d sessions=%d commands=%d", jobs, sessions, commands)
			}
		})
	}
}
