package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP151RemoteDriverRoutesEachImmutableTargetWithoutFallback(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	createA := p151RemoteRunIntent(t, "route-a", "host-a")
	createB := p151RemoteRunIntent(t, "route-b", "host-b")
	missing := p151RemoteRunIntent(t, "route-missing", "host-missing")
	for _, create := range []store.LocalIntentCreate{createA, createB, missing} {
		if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
			t.Fatal(err)
		}
	}

	storedA, err := authority.GetLocalIntent(ctx, createA.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	storedB, err := authority.GetLocalIntent(ctx, createB.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	callerA := &p149RecoveryCaller{intent: storedA}
	callerB := &p149RecoveryCaller{intent: storedB}
	configured := map[string]RemoteCaller{"host-b": callerB, "host-a": callerA}
	resolver, err := NewRemoteCallerResolver(configured)
	if err != nil {
		t.Fatal(err)
	}
	configured["host-a"] = nil // The resolver must retain its defensive copy.
	if got, want := resolver.RemoteCallerProfiles(), []string{"host-a", "host-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolver profiles=%v, want %v", got, want)
	}
	driver, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-routes", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	for _, create := range []store.LocalIntentCreate{createA, createB} {
		record, _, err := driver.DispatchIntent(ctx, create.IntentID)
		if err != nil || record.DeliveryState != store.LocalIntentAccepted {
			t.Fatalf("dispatch %s record=%+v err=%v", create.IntentID, record, err)
		}
	}
	if callerA.runCalls != 1 || callerB.runCalls != 1 || len(callerA.frames) != 1 || len(callerB.frames) != 1 ||
		callerA.frames[0].ResourceID != string(createA.JobID) || callerB.frames[0].ResourceID != string(createB.JobID) {
		t.Fatalf("cross-profile dispatch: A=%+v B=%+v", callerA.frames, callerB.frames)
	}

	if _, _, err := driver.DispatchIntent(ctx, missing.IntentID); !errors.Is(err, ErrRemoteRouteUnavailable) {
		t.Fatalf("missing route error=%v, want ErrRemoteRouteUnavailable", err)
	}
	unchanged, err := authority.GetLocalIntent(ctx, missing.IntentID)
	if err != nil || unchanged.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("missing-route intent=%+v err=%v; route resolution must precede claim", unchanged, err)
	}
	if callerA.runCalls != 1 || callerB.runCalls != 1 {
		t.Fatalf("missing profile fell back to another caller: A=%d B=%d", callerA.runCalls, callerB.runCalls)
	}
}

func TestP151DispatchNextSkipsUnavailableProfileWithoutStarvingAnotherProfile(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	missing := p151RemoteRunIntent(t, "dispatch-missing", "host-missing")
	available := p151RemoteRunIntent(t, "dispatch-available", "host-b")
	for _, create := range []store.LocalIntentCreate{missing, available} {
		if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := authority.ListEligibleLocalIntents(ctx, 10)
	if err != nil || len(candidates) != 2 || candidates[0].IntentID != missing.IntentID {
		t.Fatalf("eligible candidates=%+v err=%v; unavailable route must be first in this fixture", candidates, err)
	}
	storedAvailable, err := authority.GetLocalIntent(ctx, available.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	callerB := &p149RecoveryCaller{intent: storedAvailable}
	resolver, err := NewRemoteCallerResolver(map[string]RemoteCaller{"host-b": callerB})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-dispatch-next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _, err := driver.DispatchNext(ctx)
	if err != nil || dispatched.IntentID != available.IntentID || dispatched.DeliveryState != store.LocalIntentAccepted || callerB.runCalls != 1 {
		t.Fatalf("dispatch-next result=%+v host-b calls=%d err=%v", dispatched, callerB.runCalls, err)
	}
	stillRecorded, err := authority.GetLocalIntent(ctx, missing.IntentID)
	if err != nil || stillRecorded.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("unavailable route intent=%+v err=%v; it must remain retryable without blocking host-b", stillRecorded, err)
	}
}

func TestP151AcceptedRecoveryUsesPersistedProfileAfterDriverRecreation(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p151RemoteRunIntent(t, "recover-b", "host-b")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	callerA := &p151ProbeCaller{}
	callerB := &p149RecoveryCaller{intent: stored}
	resolver, err := NewRemoteCallerResolver(map[string]RemoteCaller{"host-a": callerA, "host-b": callerB})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := first.DispatchIntent(ctx, create.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted || callerB.runCalls != 1 {
		t.Fatalf("initial host-b acceptance=%+v run=%d err=%v", accepted, callerB.runCalls, err)
	}

	restarted, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-restarted", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReconcileAcceptedRemoteRuns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	reconciled, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil || !store.HasRemoteTerminalProof(reconciled) || callerB.runCalls != 1 || callerB.getJobCalls != 2 || callerB.getCommandCalls != 1 || callerB.streamCalls != 1 {
		t.Fatalf("restarted host-b recovery=%+v calls run=%d job=%d command=%d stream=%d err=%v", reconciled, callerB.runCalls, callerB.getJobCalls, callerB.getCommandCalls, callerB.streamCalls, err)
	}
	if callerA.callCount() != 0 {
		t.Fatalf("host-b recovery read through host-a caller: calls=%d", callerA.callCount())
	}
}

func TestP151WrongProfileReadCannotPersistRemoteProjection(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p151RemoteRunIntent(t, "wrong-profile", "host-b")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, create.IntentID, store.LocalIntentDispatching, "p151-test-dispatching"); err != nil {
		t.Fatal(err)
	}
	accepted, err := authority.TransitionLocalIntent(ctx, create.IntentID, store.LocalIntentAccepted, "p151-test-accepted")
	if err != nil {
		t.Fatal(err)
	}
	caller := &p149RecoveryCaller{intent: accepted, mutateJob: func(fields map[string]any) {
		fields["execution_target"] = map[string]string{"kind": "remote", "profile": "host-a"}
	}}
	resolver, err := NewRemoteCallerResolver(map[string]RemoteCaller{"host-b": caller})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-target-check", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileAcceptedRun(ctx, create.IntentID); err == nil {
		t.Fatal("wrong-profile target reply unexpectedly became a trusted projection")
	}
	if _, err := authority.GetRemoteJobProjection(ctx, create.JobID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("wrong-profile job projection error=%v, want no projection", err)
	}
}

func TestP151RemoteSessionCacheKeysByProfileAndProbeKeepsFailuresVisible(t *testing.T) {
	authority := p068Authority(t)
	callerA := &p151ProbeCaller{}
	callerB := &p151ProbeCaller{err: errors.New("unavailable")}
	resolver, err := NewRemoteCallerResolver(map[string]RemoteCaller{"host-b": callerB, "host-a": callerA})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRemoteDriverWithResolver(authority, resolver, "router-p151-health", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	targetA, err := domain.NewExecutionTarget(domain.TargetKindRemote, "host-a")
	if err != nil {
		t.Fatal(err)
	}
	targetB, err := domain.NewExecutionTarget(domain.TargetKindRemote, "host-b")
	if err != nil {
		t.Fatal(err)
	}
	sharedSession := domain.SessionID("session-p151-shared")
	driver.rememberRemoteSessionState(targetA, sharedSession, domain.SessionStateReady)
	driver.rememberRemoteSessionState(targetB, sharedSession, domain.SessionStateFailed)
	if state, ok := driver.cachedRemoteSessionState(targetA, sharedSession); !ok || state != domain.SessionStateReady {
		t.Fatalf("host-a session cache state=%q found=%v", state, ok)
	}
	if state, ok := driver.cachedRemoteSessionState(targetB, sharedSession); !ok || state != domain.SessionStateFailed {
		t.Fatalf("host-b session cache state=%q found=%v", state, ok)
	}

	if err := driver.ProbeProfile(context.Background(), "host-a"); err != nil || callerA.callCount() != 1 || callerB.callCount() != 0 {
		t.Fatalf("host-a probe err=%v calls A=%d B=%d", err, callerA.callCount(), callerB.callCount())
	}
	results := driver.ProbeProfiles(context.Background())
	if results["host-a"] != nil || results["host-b"] == nil {
		t.Fatalf("per-profile probe results=%v", results)
	}
	if err := driver.Probe(context.Background()); err == nil {
		t.Fatal("aggregate probe hid host-b failure")
	}
}

func p151RemoteRunIntent(t *testing.T, suffix, profile string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, profile)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	jobID := domain.JobID("job-p151-" + suffix)
	sessionID := domain.SessionID("session-p151-" + suffix)
	commandID := domain.CommandID("command-p151-" + suffix)
	script := "printf p151-" + suffix
	payload, err := json.Marshal(map[string]any{
		"operation": "run", "environment": "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": profile},
		"source":           map[string]string{"mode": "empty"},
		"script":           script,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{
		IntentID: domain.IntentID("intent-p151-" + suffix), Operation: "run", ResourceID: string(jobID),
		JobID: jobID, SessionID: sessionID, CommandID: commandID, Target: target, Environment: "linux-dev",
		Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash,
		IdempotencyKey: "key-p151-" + suffix, PayloadJSON: canonical, ScriptBytes: []byte(script), IdempotencyRetention: time.Hour,
	}
}

type p151ProbeCaller struct {
	mu     sync.Mutex
	frames []sshbridge.RequestFrame
	err    error
}

func (c *p151ProbeCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.mu.Lock()
	c.frames = append(c.frames, frame)
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	if frame.Operation != sshbridge.OperationPing {
		return sshbridge.ReplyFrame{}, errors.New("unexpected P151 route operation")
	}
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: []byte(`{}`)}, nil
}

func (c *p151ProbeCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}
