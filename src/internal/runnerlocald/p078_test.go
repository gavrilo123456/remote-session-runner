package runnerlocald

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP078AcceptIntentRunsQueuedJobWithStableCheckpoints(t *testing.T) {
	authority, service := newP060Service(t)
	intent := p078LocalRunIntent(t, authority)
	server, serveErr := p060StartServer(t, authority, service, p060SocketPath(t))
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()
	client := p060UnixClient(server.SocketPath())
	body := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, intent.IntentID, intent.RequestHash.String()))
	first := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first run status = %d body=%s", first.StatusCode, p060ReadBody(t, first))
	}
	var accepted intentAcceptanceResponse
	p060DecodeJSON(t, first, &accepted)
	if accepted.JobPhase != string(store.JobPhaseComplete) || accepted.SessionState != string(domain.SessionStateClosed) || accepted.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("first run acceptance = %+v", accepted)
	}
	job, err := authority.GetJob(context.Background(), intent.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.JobID != intent.JobID || job.SessionID != intent.SessionID || job.CommandID != intent.CommandID || job.Phase != store.JobPhaseComplete || string(job.ScriptBytes) != string(intent.ScriptBytes) {
		t.Fatalf("job checkpoint = %+v", job)
	}
	command, err := authority.GetCommand(context.Background(), intent.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if string(command.ScriptBytes) != string(intent.ScriptBytes) || command.State != domain.CommandStateSucceeded {
		t.Fatalf("command = %+v", command)
	}
	second := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("replayed run status = %d body=%s", second.StatusCode, p060ReadBody(t, second))
	}
	var replay intentAcceptanceResponse
	p060DecodeJSON(t, second, &replay)
	if replay.JobPhase != string(store.JobPhaseComplete) || replay.IntentID != string(intent.IntentID) {
		t.Fatalf("replayed run acceptance = %+v", replay)
	}
}

func p078LocalRunIntent(t *testing.T, authority *store.AuthorityStore) store.LocalIntentRecord {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"operation": "run", "environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "job_id": "job-p078-local", "session_id": "session-p078-local", "command_id": "command-p078-local", "script": "echo p078", "source": map[string]string{"mode": "empty"}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return mustCreateRunIntent(t, authority, store.LocalIntentCreate{IntentID: "intent-p078-local", Operation: "run", ResourceID: "job-p078-local", SessionID: "session-p078-local", CommandID: "command-p078-local", JobID: "job-p078-local", Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-p078-local", PayloadJSON: canonical, ScriptBytes: []byte("echo p078")})
}

func mustCreateRunIntent(t *testing.T, authority *store.AuthorityStore, input store.LocalIntentCreate) store.LocalIntentRecord {
	t.Helper()
	record, err := authority.CreateLocalIntent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
