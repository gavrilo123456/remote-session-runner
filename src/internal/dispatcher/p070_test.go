package dispatcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

type p070Caller struct {
	frames    []sshbridge.RequestFrame
	responses []sshbridge.ReplyFrame
	errors    []error
}

func (c *p070Caller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	index := len(c.frames) - 1
	if index < len(c.errors) && c.errors[index] != nil {
		return sshbridge.ReplyFrame{}, c.errors[index]
	}
	if index < len(c.responses) {
		return c.responses[index], nil
	}
	return sshbridge.ReplyFrame{}, errors.New("missing P070 fake response")
}

func TestP070ReconcileAcceptedResourceWithoutResubmitting(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p070-accepted", "session-p070-accepted", "command-p070-accepted", domain.TargetKindRemote, "echo accepted")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(intent.ScriptBytes)
	readPayload, _ := json.Marshal(map[string]any{
		"command_id": string(intent.CommandID), "session_id": string(intent.SessionID), "ordinal": *intent.IntentOrdinal,
		"script_sha256": hex.EncodeToString(digest[:]),
	})
	caller := &p070Caller{
		errors:    []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}},
		responses: []sshbridge.ReplyFrame{{}, {ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID) + "/reconcile", ResponseType: "result", Payload: readPayload}},
	}
	driver, err := NewRemoteDriver(authority, caller, "router-p070", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err == nil {
		t.Fatal("uncertain send unexpectedly succeeded")
	}
	current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || current.DeliveryState != store.LocalIntentUncertain {
		t.Fatalf("after send state = %+v, %v", current, err)
	}
	accepted, reply, err := driver.ReconcileIntent(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.DeliveryState != store.LocalIntentAccepted || reply.ResponseType != "result" || len(caller.frames) != 2 {
		t.Fatalf("reconciliation = %+v/%+v calls=%d", accepted, reply, len(caller.frames))
	}
	if caller.frames[1].Operation != sshbridge.OperationGetCommand || caller.frames[0].Operation != sshbridge.OperationSubmitOrResumeCommand {
		t.Fatalf("reconciliation calls = %+v", caller.frames)
	}
}

func TestP070ReconcileNotFoundProvesNoTargetDelivery(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p070-missing", "session-p070-missing", "command-p070-missing", domain.TargetKindRemote, "echo missing")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	errorPayload, _ := json.Marshal(sshbridge.ErrorPayload{Code: "resource_not_found", Message: "unknown command"})
	caller := &p070Caller{
		errors:    []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}},
		responses: []sshbridge.ReplyFrame{{}, {ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID) + "/reconcile", ResponseType: "error", Payload: errorPayload}},
	}
	driver, err := NewRemoteDriver(authority, caller, "router-p070", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = driver.DispatchIntent(context.Background(), intent.IntentID)
	notDelivered, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID)
	if err != nil || notDelivered.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("not-found reconciliation = %+v, %v", notDelivered, err)
	}
}

func TestP070MismatchedTargetResourceRemainsUncertain(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p070-mismatch", "session-p070-mismatch", "command-p070-mismatch", domain.TargetKindRemote, "echo mismatch")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	wrongPayload, _ := json.Marshal(map[string]any{"command_id": "command-other", "session_id": string(intent.SessionID), "ordinal": *intent.IntentOrdinal})
	caller := &p070Caller{
		errors:    []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}},
		responses: []sshbridge.ReplyFrame{{}, {ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID) + "/reconcile", ResponseType: "result", Payload: wrongPayload}},
	}
	driver, err := NewRemoteDriver(authority, caller, "router-p070", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = driver.DispatchIntent(context.Background(), intent.IntentID)
	if _, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID); !errors.Is(err, ErrRemoteNotReconciled) {
		t.Fatalf("mismatch error = %v, want ErrRemoteNotReconciled", err)
	}
	current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != store.LocalIntentUncertain {
		t.Fatalf("mismatch changed state to %s", current.DeliveryState)
	}
}

func TestP070ReconcileBeforeSendStateIsAlreadyProvenNotDelivered(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p070-before", "session-p070-before", "command-p070-before", domain.TargetKindRemote, "echo before")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p070Caller{errors: []error{&sshclient.TransportError{Phase: sshclient.PhaseBeforeSend, Err: errors.New("no connection")}}}
	driver, err := NewRemoteDriver(authority, caller, "router-p070", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = driver.DispatchIntent(context.Background(), intent.IntentID)
	current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || current.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("before-send state = %+v, %v", current, err)
	}
	before := len(caller.frames)
	result, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID)
	if err != nil || result.DeliveryState != store.LocalIntentNotDelivered || len(caller.frames) != before {
		t.Fatalf("reconcile proven non-delivery = %+v, %v calls=%d/%d", result, err, len(caller.frames), before)
	}
}
