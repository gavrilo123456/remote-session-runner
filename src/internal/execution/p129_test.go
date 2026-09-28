package execution

import (
	"context"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP129CleanupFailureCounterTracksUnconfirmedStartupCleanup(t *testing.T) {
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-p129-cleanup"}, reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-p129-cleanup",
		CleanupConfirmed:  false,
	}}
	service, authority := newP027Service(t, runtime)
	request := p020Request(t, "session-p129-cleanup", "key-p129-cleanup", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	accepted := p027AcceptCreating(t, authority, request)
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	metrics, err := authority.ReadOperationalMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CleanupFailuresTotal != 1 {
		t.Fatalf("cleanup failure count=%d, want 1", metrics.CleanupFailuresTotal)
	}
	settled, err := authority.GetSession(context.Background(), accepted.SessionID)
	if err != nil || settled.State != domain.SessionStateLost {
		t.Fatalf("session after uncertain cleanup=%+v err=%v", settled, err)
	}
}
