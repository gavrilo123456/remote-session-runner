package runnerd

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP127DirectHTTPSAuditsActionsAndDenials(t *testing.T) {
	owner := p107DirectController(t, "tomasz.walczuk")
	other := p107DirectController(t, "p127-other-controller")
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p127-direct-audit"})
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}

	badEnvironment := []byte(`{"environment":"not-configured","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"}}`)
	deniedCreate := p107Do(handler, owner, true, http.MethodPost, "/v1/sessions", badEnvironment, "key-p127-bad-environment")
	if deniedCreate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown environment status=%d body=%s", deniedCreate.Code, deniedCreate.Body.String())
	}
	created := p107Do(handler, owner, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "key-p127-direct-create")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil || session.SessionID == "" {
		t.Fatalf("decode create acceptance=%+v err=%v", session, err)
	}
	submitBody := []byte(`{"script":"printf 'direct-script-marker'"}`)
	submitted := p107Do(handler, owner, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", submitBody, "key-p127-direct-submit")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitted.Code, submitted.Body.String())
	}
	var command directCommandAcceptance
	if err := json.Unmarshal(submitted.Body.Bytes(), &command); err != nil || command.CommandID == "" {
		t.Fatalf("decode submit acceptance=%+v err=%v", command, err)
	}
	p127WaitForCommandTerminal(t, authority, domain.CommandID(command.CommandID))

	deniedSubmit := p107Do(handler, other, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", []byte(`{"script":"denied"}`), "key-p127-denied-submit")
	if deniedSubmit.Code != http.StatusForbidden {
		t.Fatalf("cross-controller submit status=%d body=%s", deniedSubmit.Code, deniedSubmit.Body.String())
	}
	cancel := p107Do(handler, owner, true, http.MethodPost, "/v1/commands/"+command.CommandID+"/cancel", nil, "key-p127-direct-cancel")
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	deniedCancel := p107Do(handler, other, true, http.MethodPost, "/v1/commands/"+command.CommandID+"/cancel", nil, "key-p127-denied-cancel")
	if deniedCancel.Code != http.StatusForbidden {
		t.Fatalf("cross-controller cancel status=%d body=%s", deniedCancel.Code, deniedCancel.Body.String())
	}
	deniedClose := p107Do(handler, other, true, http.MethodDelete, "/v1/sessions/"+session.SessionID, nil, "key-p127-denied-close")
	if deniedClose.Code != http.StatusForbidden {
		t.Fatalf("cross-controller close status=%d body=%s", deniedClose.Code, deniedClose.Body.String())
	}
	closed := p107Do(handler, owner, true, http.MethodDelete, "/v1/sessions/"+session.SessionID, nil, "key-p127-direct-close")
	if closed.Code != http.StatusAccepted {
		t.Fatalf("close status=%d body=%s", closed.Code, closed.Body.String())
	}

	records, err := authority.ListAuditRecords(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[audit.Action]map[audit.Outcome]int)
	for _, record := range records {
		if counts[record.Action] == nil {
			counts[record.Action] = make(map[audit.Outcome]int)
		}
		counts[record.Action][record.Outcome]++
		if record.Ingress != audit.IngressDirectMTLS {
			t.Errorf("direct audit ingress=%q, want direct_mtls", record.Ingress)
		}
	}
	want := map[audit.Action]map[audit.Outcome]int{
		audit.ActionCreate: {audit.OutcomeAllowed: 1, audit.OutcomeDenied: 1},
		audit.ActionSubmit: {audit.OutcomeAllowed: 1, audit.OutcomeDenied: 1},
		audit.ActionCancel: {audit.OutcomeAllowed: 1, audit.OutcomeDenied: 1},
		audit.ActionClose:  {audit.OutcomeAllowed: 1, audit.OutcomeDenied: 1},
	}
	for action, outcomes := range want {
		for outcome, count := range outcomes {
			if got := counts[action][outcome]; got != count {
				t.Errorf("%s %s rows=%d, want %d; records=%+v", action, outcome, got, count, records)
			}
		}
	}
	for _, record := range records {
		if record.Outcome == audit.OutcomeDenied && record.ReasonCode == audit.ReasonControllerDenied && record.Principal.ID() != other.ID() {
			t.Errorf("controller denial principal=%q, want %q", record.Principal.ID(), other.ID())
		}
		if string(record.SessionID) == session.SessionID && record.Outcome == audit.OutcomeAllowed && record.Principal.Type() != domain.ControllerTypeDirectMTLS {
			t.Errorf("allowed direct row principal type=%q", record.Principal.Type())
		}
	}
}

func p127WaitForCommandTerminal(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		command, err := authority.GetCommand(context.Background(), commandID)
		if err != nil {
			t.Fatal(err)
		}
		if command.State.IsTerminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command %s did not reach terminal state before audit verification", commandID)
}
