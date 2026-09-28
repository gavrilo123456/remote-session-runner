package runnerd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP111I01DirectResourceOperationContract(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p111-contract-generation", stopConfirmed: true}
	service, _ := newP046Service(t, runtimeAdapter)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")

	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p111-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var create directSessionAcceptance
	p107Decode(t, created, &create)
	if create.ResourceID != create.SessionID || create.AcceptanceScope != "target_authority" ||
		create.ExecutionTarget.Kind != string(domain.TargetKindRemote) || create.KnownState.SessionState != string(domain.SessionStateReady) {
		t.Fatalf("create acceptance = %+v", create)
	}
	readSession := p107Do(handler, controller, true, http.MethodGet, "/v1/sessions/"+create.SessionID, nil, "")
	if readSession.Code != http.StatusOK {
		t.Fatalf("read session status=%d body=%s", readSession.Code, readSession.Body.String())
	}
	var sessionSnapshot directSessionReadResponse
	p107Decode(t, readSession, &sessionSnapshot)
	if sessionSnapshot.View != "authority" || sessionSnapshot.Resource.SessionID != create.SessionID ||
		sessionSnapshot.Resource.SessionState != string(domain.SessionStateReady) || sessionSnapshot.Resource.Authority != "remote" {
		t.Fatalf("session snapshot = %+v", sessionSnapshot)
	}

	script := `{"script":"printf p111-contract"}`
	submitted := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+create.SessionID+"/commands", []byte(script), "p111-submit-key")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitted.Code, submitted.Body.String())
	}
	var submit directCommandAcceptance
	p107Decode(t, submitted, &submit)
	if submit.ResourceID != submit.CommandID || submit.SessionID != create.SessionID || submit.AcceptanceScope != "target_authority" ||
		submit.ExecutionTarget.Kind != string(domain.TargetKindRemote) || submit.KnownState.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("submit acceptance = %+v", submit)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		command, err := service.GetCommand(context.Background(), domain.CommandID(submit.CommandID), controller)
		if err != nil {
			t.Fatalf("get submitted command from service: %v", err)
		}
		if command.State == domain.CommandStateSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("submitted command did not complete; latest state=%q", command.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	readCommand := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+submit.CommandID, nil, "")
	if readCommand.Code != http.StatusOK {
		t.Fatalf("read command status=%d body=%s", readCommand.Code, readCommand.Body.String())
	}
	var commandSnapshot directCommandReadResponse
	p107Decode(t, readCommand, &commandSnapshot)
	if commandSnapshot.View != "authority" || commandSnapshot.Resource.CommandID != submit.CommandID ||
		commandSnapshot.Resource.SessionID != create.SessionID || commandSnapshot.Resource.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("command snapshot = %+v", commandSnapshot)
	}

	queuedID := p109AcceptQueuedCommand(t, service, domain.SessionID(create.SessionID), controller, "cmd-p111-cancel")
	cancelled := p107Do(handler, controller, true, http.MethodPost, "/v1/commands/"+string(queuedID)+"/cancel", nil, "p111-cancel-key")
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	var cancel directCommandAcceptance
	p107Decode(t, cancelled, &cancel)
	if cancel.ResourceID != string(queuedID) || cancel.CommandID != string(queuedID) || cancel.SessionID != create.SessionID ||
		cancel.AcceptanceScope != "target_authority" || cancel.KnownState.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("cancel acceptance = %+v", cancel)
	}
	readCancelled := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(queuedID), nil, "")
	var cancelledSnapshot directCommandReadResponse
	if readCancelled.Code != http.StatusOK {
		t.Fatalf("read cancelled command status=%d body=%s", readCancelled.Code, readCancelled.Body.String())
	}
	p107Decode(t, readCancelled, &cancelledSnapshot)
	if cancelledSnapshot.Resource.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("cancelled command snapshot = %+v", cancelledSnapshot)
	}

	closed := p107Do(handler, controller, true, http.MethodDelete, "/v1/sessions/"+create.SessionID, []byte(`{"policy":"graceful"}`), "p111-close-key")
	if closed.Code != http.StatusAccepted {
		t.Fatalf("close status=%d body=%s", closed.Code, closed.Body.String())
	}
	var closeAcceptance directSessionAcceptance
	p107Decode(t, closed, &closeAcceptance)
	if closeAcceptance.ResourceID != create.SessionID || closeAcceptance.SessionID != create.SessionID ||
		closeAcceptance.AcceptanceScope != "target_authority" || closeAcceptance.KnownState.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("close acceptance = %+v", closeAcceptance)
	}

	jobResponse := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(p110RunBody), "p111-run-key")
	if jobResponse.Code != http.StatusAccepted {
		t.Fatalf("run status=%d body=%s", jobResponse.Code, jobResponse.Body.String())
	}
	var jobAcceptance directJobAcceptance
	p107Decode(t, jobResponse, &jobAcceptance)
	if jobAcceptance.ResourceID != jobAcceptance.JobID || jobAcceptance.SessionID == "" || jobAcceptance.CommandID == "" ||
		jobAcceptance.AcceptanceScope != "target_authority" || jobAcceptance.ExecutionTarget.Kind != string(domain.TargetKindRemote) {
		t.Fatalf("run acceptance = %+v", jobAcceptance)
	}
	readJob := p107Do(handler, controller, true, http.MethodGet, "/v1/jobs/"+jobAcceptance.JobID, nil, "")
	if readJob.Code != http.StatusOK {
		t.Fatalf("get-job status=%d body=%s", readJob.Code, readJob.Body.String())
	}
	var jobSnapshot directJobReadResponse
	p107Decode(t, readJob, &jobSnapshot)
	if jobSnapshot.View != "authority" || jobSnapshot.Resource.JobID != jobAcceptance.JobID ||
		jobSnapshot.Resource.SessionID != jobAcceptance.SessionID || jobSnapshot.Resource.CommandID != jobAcceptance.CommandID ||
		jobSnapshot.Resource.CommandState == nil || *jobSnapshot.Resource.CommandState != string(domain.CommandStateSucceeded) ||
		jobSnapshot.Resource.TeardownState != "closed" {
		t.Fatalf("job snapshot = %+v", jobSnapshot)
	}

	localTarget := []byte(`{"environment":"linux-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`)
	localRejected := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", localTarget, "p111-local-target")
	if localRejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("direct local-target status=%d body=%s", localRejected.Code, localRejected.Body.String())
	}
	otherController := p107DirectController(t, "p111-other-controller")
	denied := p107Do(handler, otherController, true, http.MethodGet, "/v1/jobs/"+jobAcceptance.JobID, nil, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-controller job read status=%d body=%s", denied.Code, denied.Body.String())
	}

	if runtimeAdapter.commandCalls != 2 || runtimeAdapter.stopCalls != 2 {
		t.Fatalf("contract operations ran unexpected work: command calls=%d stop calls=%d, want two each", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}
}
