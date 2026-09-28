package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

func TestP127ActionsAndAuthorizationDenialsAreAudited(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p127-audit", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	ctx := audit.WithIngress(context.Background(), audit.IngressLocalWorker)
	target := p020Target(t, domain.TargetKindLocal, "mac-workstation")
	request := p020Request(t, "session-p127-actions", "key-p127-create", target)
	created, err := service.CreateSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	commandRequest := p021SubmitRequest(t, created.Session.SessionID, "command-p127-actions", "key-p127-submit", "printf 'raw-script-marker'")
	queued, err := service.AcceptCommand(ctx, commandRequest)
	if err != nil {
		t.Fatal(err)
	}
	other, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("p127-other-user"))
	if err != nil {
		t.Fatal(err)
	}
	deniedSubmit := commandRequest
	deniedSubmit.Controller = other
	if _, err := service.AcceptCommand(ctx, deniedSubmit); !errors.Is(err, ErrSessionController) {
		t.Fatalf("cross-controller submit error = %v, want controller denial", err)
	}
	deniedCancel := p022CancelRequest(t, queued.Command.CommandID, "key-p127-denied-cancel", "denied cancel")
	deniedCancel.Controller = other
	if _, err := service.CancelCommand(ctx, deniedCancel); !errors.Is(err, ErrSessionController) {
		t.Fatalf("cross-controller cancel error = %v, want controller denial", err)
	}
	deniedClose := p022CloseRequest(t, created.Session.SessionID, "key-p127-denied-close", "graceful")
	deniedClose.Controller = other
	if _, err := service.CloseSession(ctx, deniedClose); !errors.Is(err, ErrSessionController) {
		t.Fatalf("cross-controller close error = %v, want controller denial", err)
	}
	deniedEnvironment := request
	deniedEnvironment.SessionID = "session-p127-denied-environment"
	deniedEnvironment.IdempotencyKey = "key-p127-denied-environment"
	deniedEnvironment.Environment = "not-configured"
	if _, err := service.CreateSession(ctx, deniedEnvironment); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("unknown environment error = %v, want environment denial", err)
	}

	allowedCancel := p022CancelRequest(t, queued.Command.CommandID, "key-p127-allowed-cancel", "allowed cancel")
	if _, err := service.CancelCommand(ctx, allowedCancel); err != nil {
		t.Fatal(err)
	}
	allowedClose := p022CloseRequest(t, created.Session.SessionID, "key-p127-allowed-close", "graceful")
	if _, err := service.CloseSession(ctx, allowedClose); err != nil {
		t.Fatal(err)
	}

	records, err := authority.ListAuditRecords(ctx, 32)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[audit.Action]map[audit.Outcome]int)
	for _, record := range records {
		if counts[record.Action] == nil {
			counts[record.Action] = make(map[audit.Outcome]int)
		}
		counts[record.Action][record.Outcome]++
		if record.Ingress != audit.IngressLocalWorker {
			t.Errorf("action %s ingress = %q, want local_executor", record.Action, record.Ingress)
		}
		if record.SessionID == created.Session.SessionID && record.Outcome == audit.OutcomeAllowed && record.Principal.ID() != created.Session.Controller.ID() {
			t.Errorf("allowed record principal = %s, want session owner", record.Principal.ID())
		}
		if record.Outcome == audit.OutcomeDenied && record.ReasonCode == audit.ReasonControllerDenied && record.Principal.ID() != other.ID() {
			t.Errorf("denial record principal = %s, want denied controller", record.Principal.ID())
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
				t.Errorf("%s %s audit rows = %d, want %d; all rows=%+v", action, outcome, got, count, records)
			}
		}
	}
	for _, record := range records {
		if record.OccurredAt.IsZero() || record.OccurredAt.After(time.Now().Add(time.Minute)) {
			t.Errorf("invalid audit timestamp: %+v", record)
		}
	}
}
