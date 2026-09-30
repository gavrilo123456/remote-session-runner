package runnerd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

// TestP149RunMutationAcknowledgesDurableWorkWhenCapabilityReadFails verifies
// the response boundary that caused the post-restart mailbox problem. The
// job itself must remain durable when optional capability enrichment fails;
// only a later strict read can turn that acknowledgement into a projection.
func TestP149RunMutationAcknowledgesDurableWorkWhenCapabilityReadFails(t *testing.T) {
	resolver := &p149ToggleResolver{delegate: mustP047Registry(t), successesBeforeFailure: 1}
	runtimeAdapter := &p048FakeRuntime{generation: "p149-response-generation", stopConfirmed: true}
	service, authority := newP046ServiceWithResolver(t, runtimeAdapter, resolver)
	server, serveErr := p048StartServer(t, service)
	client := p046UnixClient(server.SocketPath())
	defer p048CloseServer(t, server, serveErr)

	body := []byte(`{"job_id":"p149-job","session_id":"p149-session","command_id":"p149-command","idempotency_key":"p149-run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"},"script":"printf p149"}`)
	accepted := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/jobs", body)
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("run mutation status=%d body=%s", accepted.StatusCode, p046ReadBody(t, accepted))
	}
	var envelope map[string]json.RawMessage
	p046DecodeJSON(t, accepted, &envelope)
	p149AssertIdentityOnlyJobEnvelope(t, envelope)

	durable, err := authority.GetJob(context.Background(), domain.JobID("p149-job"))
	if err != nil {
		t.Fatal(err)
	}
	if durable.Phase != store.JobPhaseComplete || durable.CommandState == nil || *durable.CommandState != domain.CommandStateSucceeded || durable.TeardownState != store.JobTeardownClosed {
		t.Fatalf("durable job=%+v", durable)
	}
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 1 {
		t.Fatalf("mutation runtime calls command=%d stop=%d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}

	readURL := "http://runnerd/internal/v1/jobs/p149-job?controller_type=queued_mac&controller_id=tomasz.walczuk"
	unavailable := p046DoJSON(t, client, http.MethodGet, readURL, nil)
	if unavailable.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("strict job read status=%d body=%s", unavailable.StatusCode, p046ReadBody(t, unavailable))
	}
	_ = p046ReadBody(t, unavailable)

	resolver.SetAvailable(true)
	read := p046DoJSON(t, client, http.MethodGet, readURL, nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("recovered job read status=%d body=%s", read.StatusCode, p046ReadBody(t, read))
	}
	var projection jobResponse
	p046DecodeJSON(t, read, &projection)
	if projection.JobID != "p149-job" || projection.JobPhase != string(store.JobPhaseComplete) || projection.CommandState == nil || *projection.CommandState != string(domain.CommandStateSucceeded) || projection.Capabilities.EffectiveAccount != "ubuntu" {
		t.Fatalf("strict job projection=%+v", projection)
	}
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 1 {
		t.Fatalf("reads re-executed durable job: command=%d stop=%d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}
}

func p149AssertIdentityOnlyJobEnvelope(t *testing.T, envelope map[string]json.RawMessage) {
	t.Helper()
	for _, key := range []string{"job_id", "session_id", "command_id"} {
		if _, ok := envelope[key]; !ok {
			t.Fatalf("acceptance envelope lacks %q: %s", key, p149JSON(t, envelope))
		}
	}
	for _, forbidden := range []string{
		"job_phase", "command_state", "exit_code", "final_event_sequence",
		"output_complete", "output_truncated", "teardown_state", "execution_target",
		"authority", "controller", "observed_at", "environment", "source", "capabilities",
	} {
		if _, ok := envelope[forbidden]; ok {
			t.Fatalf("acceptance envelope misleadingly contains %q: %s", forbidden, p149JSON(t, envelope))
		}
	}
}

func p149JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type p149ToggleResolver struct {
	delegate               execution.EnvironmentResolver
	mu                     sync.Mutex
	successesBeforeFailure int
	available              bool
}

func (r *p149ToggleResolver) ResolveEnvironment(ctx context.Context, name string) (domain.Environment, error) {
	r.mu.Lock()
	if r.available {
		r.mu.Unlock()
		return r.delegate.ResolveEnvironment(ctx, name)
	}
	if r.successesBeforeFailure > 0 {
		r.successesBeforeFailure--
		r.mu.Unlock()
		return r.delegate.ResolveEnvironment(ctx, name)
	}
	r.mu.Unlock()
	return domain.Environment{}, errors.New("P149 simulated capability resolver outage")
}

func (r *p149ToggleResolver) SetAvailable(available bool) {
	r.mu.Lock()
	r.available = available
	r.mu.Unlock()
}
