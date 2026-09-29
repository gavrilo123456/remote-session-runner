package execution

import (
	"context"
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP141F05SubscriptionUsesConfiguredEnvironmentByteLimit(t *testing.T) {
	ctx := context.Background()
	target := p020Target(t, domain.TargetKindLocal, "mac-workstation")
	controller := p020Controller(t, domain.ControllerTypeLocalUser)
	limits := domain.DefaultServiceLimits()
	limits.SubscriberBufferBytes = 7
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS workstation", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, t.TempDir()+"/state/p141-subscriber.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, &p020FakeRuntime{generation: "p141-subscriber-generation"}, registry, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("session-p141-configured-limit")
	_, err = service.CreateSession(ctx, p020Request(t, string(sessionID), "key-p141-configured-limit", target))
	if err != nil {
		t.Fatal(err)
	}
	commandID := domain.CommandID("command-p141-configured-limit")
	script := "printf p141"
	requestBytes, err := json.Marshal(map[string]string{"session_id": string(sessionID), "script": script})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", requestBytes, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptCommand(ctx, SubmitCommandRequest{
		CommandID: commandID, SessionID: sessionID, Controller: controller,
		IdempotencyKey: "key-p141-configured-command", RequestHash: hash, Script: script,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte("1234"), []byte("5678")} {
		if _, err := authority.AppendCommandEvent(ctx, store.CommandEventAppend{
			CommandID: commandID, Type: "stdout", Payload: payload, ByteCount: int64(len(payload)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	subscription, err := service.SubscribeCommandEvents(ctx, commandID, controller, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if got := subscription.BufferedPayloadBytes(); got != 4 {
		t.Fatalf("configured 7-byte subscriber retained %d bytes, want the first four-byte output only", got)
	}
	if got := subscription.LastSequence(); got != 3 {
		t.Fatalf("configured byte limit was not enforced at the environment value: cursor=%d, want 3", got)
	}
}
