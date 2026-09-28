package runnerd

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP122D09DirectMutationAcceptancesWarnAfterIdempotencyGC(t *testing.T) {
	runtime := &p109Runtime{generation: "p122-expiry-generation"}
	service, authority, database := p112NewService(t, runtime)
	endpoint, controller, _ := p112StartMappedServer(t, service)
	const key = "p122-expired-create-key"
	status, body := p112Do(t, controller, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), key)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var first directSessionAcceptance
	p112Decode(t, body, &first)
	if first.IdempotencyWarning != "" {
		t.Fatalf("first acceptance warning = %q, want none", first.IdempotencyWarning)
	}

	p122ExpireKeyAndCollect(t, database, authority, "create_session", key)

	status, body = p112Do(t, controller, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), key)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var reused directSessionAcceptance
	p112Decode(t, body, &reused)
	if reused.SessionID == first.SessionID || reused.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired-key acceptance = %#v, prior=%#v", reused, first)
	}

	status, body = p112Do(t, controller, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), key)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var replay directSessionAcceptance
	p112Decode(t, body, &replay)
	if replay.SessionID != reused.SessionID || replay.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("warned replay = %#v, first post-expiry result=%#v", replay, reused)
	}

	submitBody := []byte(`{"script":"printf p122-expiry"}`)
	submitPath := endpoint + "/v1/sessions/" + reused.SessionID + "/commands"
	const submitKey = "p122-expired-submit-key"
	status, body = p112Do(t, controller, http.MethodPost, submitPath, submitBody, submitKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var submitted directCommandAcceptance
	p112Decode(t, body, &submitted)
	p108WaitForCommandState(t, authority, domain.CommandID(submitted.CommandID), domain.CommandStateSucceeded)
	p122ExpireKeyAndCollect(t, database, authority, "submit_command", submitKey)
	status, body = p112Do(t, controller, http.MethodPost, submitPath, submitBody, submitKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var resubmitted directCommandAcceptance
	p112Decode(t, body, &resubmitted)
	if resubmitted.CommandID == submitted.CommandID || resubmitted.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired submit-key acceptance = %#v, prior=%#v", resubmitted, submitted)
	}
	p108WaitForCommandState(t, authority, domain.CommandID(resubmitted.CommandID), domain.CommandStateSucceeded)

	cancelCommand := p109AcceptQueuedCommand(t, service, domain.SessionID(reused.SessionID), p107DirectController(t, "tomasz.walczuk"), "p122-cancel-target")
	cancelPath := endpoint + "/v1/commands/" + string(cancelCommand) + "/cancel"
	const cancelKey = "p122-expired-cancel-key"
	status, body = p112Do(t, controller, http.MethodPost, cancelPath, nil, cancelKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	p122ExpireKeyAndCollect(t, database, authority, "cancel_command", cancelKey)
	status, body = p112Do(t, controller, http.MethodPost, cancelPath, nil, cancelKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var cancelled directCommandAcceptance
	p112Decode(t, body, &cancelled)
	if cancelled.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired cancel-key acceptance = %#v", cancelled)
	}

	closePath := endpoint + "/v1/sessions/" + reused.SessionID
	const closeKey = "p122-expired-close-key"
	closeBody := []byte(`{"policy":"graceful"}`)
	status, body = p112Do(t, controller, http.MethodDelete, closePath, closeBody, closeKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	p122ExpireKeyAndCollect(t, database, authority, "close_session", closeKey)
	status, body = p112Do(t, controller, http.MethodDelete, closePath, closeBody, closeKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var closed directSessionAcceptance
	p112Decode(t, body, &closed)
	if closed.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired close-key acceptance = %#v", closed)
	}

	const runKey = "p122-expired-run-key"
	runPath := endpoint + "/v1/jobs"
	runBody := []byte(p110RunBody)
	status, body = p112Do(t, controller, http.MethodPost, runPath, runBody, runKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var firstJob directJobAcceptance
	p112Decode(t, body, &firstJob)
	p122ExpireKeyAndCollect(t, database, authority, "run", runKey)
	status, body = p112Do(t, controller, http.MethodPost, runPath, runBody, runKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var secondJob directJobAcceptance
	p112Decode(t, body, &secondJob)
	if secondJob.JobID == firstJob.JobID || secondJob.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired run-key acceptance = %#v, prior=%#v", secondJob, firstJob)
	}
}

func p122ExpireKeyAndCollect(t *testing.T, database *sql.DB, authority *store.AuthorityStore, operation, key string) {
	t.Helper()
	// Model an expiry that occurred one day ago: GC records the warning until
	// the end of the following 90-day metadata-retention window.
	expiresAt := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(context.Background(), `UPDATE exec_idempotency SET expires_at = ? WHERE controller_type = 'direct_mtls' AND controller_id = 'tomasz.walczuk' AND operation = ? AND idempotency_key = ?`, expiresAt, operation, key); err != nil {
		t.Fatal(err)
	}
	report, err := authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
	if err != nil || report.IdempotencyRecordsDeleted != 1 {
		t.Fatalf("%s idempotency GC report=%+v err=%v", operation, report, err)
	}
}

func TestP122D09AcceptanceMappersCarryWarnings(t *testing.T) {
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	// The JSON wire contract uses the same optional field across all target
	// authority mutation acceptance kinds.
	for name, value := range map[string]any{
		"session": directSessionAcceptanceFromRecord(store.SessionRecord{SessionID: "session-p122", Target: target}, true),
		"command": directCommandAcceptanceFromRecord(store.CommandRecord{CommandID: "command-p122", SessionID: "session-p122"}, target, true),
		"job":     directJobAcceptanceFromRecord(store.JobRecord{JobID: "job-p122", SessionID: "session-p122", CommandID: "command-p122", Target: target}, true),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["idempotency_warning"] != "deduplication_not_guaranteed" {
				t.Fatalf("acceptance JSON = %s", encoded)
			}
		})
	}
}
