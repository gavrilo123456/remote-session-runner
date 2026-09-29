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

func TestP140RemoteAuthorityOrdinalMayFollowCancelledMacIntentGap(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	predecessor := p068SubmitIntent(t, "intent-p140-gap-1", "session-p140-gap", "command-p140-gap-1", domain.TargetKindRemote, "echo cancelled")
	if _, err := authority.CreateLocalIntent(ctx, predecessor); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, predecessor.IntentID, store.LocalIntentNotDelivered, "cancelled_before_dispatch"); err != nil {
		t.Fatal(err)
	}

	intent := p068SubmitIntent(t, "intent-p140-gap-2", "session-p140-gap", "command-p140-gap-2", domain.TargetKindRemote, "printf once")
	*intent.IntentOrdinal = 2
	if _, err := authority.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	caller := &p140OrdinalCaller{intent: stored, authorityOrdinal: 1}
	driver, err := NewRemoteDriver(authority, caller, "router-p140-gap", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(ctx, intent.IntentID)
	if err != nil {
		t.Fatalf("dispatch Mac intent ordinal 2 to Linux ordinal 1: %v", err)
	}
	if accepted.DeliveryState != store.LocalIntentAccepted || len(caller.frames) != 1 {
		t.Fatalf("dispatch state=%s bridge calls=%d", accepted.DeliveryState, len(caller.frames))
	}
	var request struct {
		IntentOrdinal int64 `json:"intent_ordinal"`
	}
	if err := json.Unmarshal(caller.frames[0].Payload, &request); err != nil || request.IntentOrdinal != 2 {
		t.Fatalf("bridge request intent ordinal=%d err=%v, want Mac provenance 2", request.IntentOrdinal, err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(caller.mutationReply.Payload, &response); err != nil {
		t.Fatal(err)
	}
	if ordinal, ok := positiveRemoteAuthorityOrdinal(response); !ok || ordinal != 1 {
		t.Fatalf("Linux authority ordinal=%d present=%v, want 1", ordinal, ok)
	}
	projection, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
	if err != nil || projection.Ordinal != 1 {
		t.Fatalf("stored Linux projection ordinal=%d err=%v, want 1", projection.Ordinal, err)
	}
}

func TestP140ReconciliationKeepsStableIDsAndIndependentLinuxOrdinal(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	predecessor := p068SubmitIntent(t, "intent-p140-reconcile-gap-1", "session-p140-reconcile", "command-p140-reconcile-gap-1", domain.TargetKindRemote, "cancelled")
	if _, err := authority.CreateLocalIntent(ctx, predecessor); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, predecessor.IntentID, store.LocalIntentNotDelivered, "cancelled_before_dispatch"); err != nil {
		t.Fatal(err)
	}
	intent := p068SubmitIntent(t, "intent-p140-reconcile-gap-2", "session-p140-reconcile", "command-p140-reconcile-gap-2", domain.TargetKindRemote, "printf once")
	*intent.IntentOrdinal = 2
	if _, err := authority.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	caller := &p140OrdinalCaller{intent: stored, authorityOrdinal: 1, dropMutationReply: true}
	driver, err := NewRemoteDriver(authority, caller, "router-p140-reconcile", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(ctx, intent.IntentID); err == nil {
		t.Fatal("after-send reply loss was not reported")
	}
	uncertain, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || uncertain.DeliveryState != store.LocalIntentUncertain {
		t.Fatalf("after-send local intent=%s err=%v, want uncertain", uncertain.DeliveryState, err)
	}
	if err := validateRemoteReply(stored, caller.frames[0], caller.mutationReply); err != nil {
		t.Fatalf("the simulated lost mutation reply with Linux ordinal 1 should validate against Mac intent ordinal 2: %v", err)
	}

	accepted, _, err := driver.ReconcileIntent(ctx, intent.IntentID)
	if err != nil {
		t.Fatalf("reconcile after database restore: %v", err)
	}
	if accepted.DeliveryState != store.LocalIntentAccepted || len(caller.frames) != 2 || caller.frames[1].Operation != sshbridge.OperationGetCommand {
		t.Fatalf("reconciliation state=%s frames=%+v; expected one read and no resubmit", accepted.DeliveryState, caller.frames)
	}
	projection, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
	if err != nil || projection.CommandID != intent.CommandID || projection.SessionID != intent.SessionID || projection.Ordinal != 1 {
		t.Fatalf("reconciled projection=%+v err=%v; IDs and Linux ordinal must be preserved", projection, err)
	}
}

func TestP140RemoteAuthorityOrdinalValidationFailsClosed(t *testing.T) {
	intent := p068SubmitIntent(t, "intent-p140-invalid-ordinal", "session-p140-invalid-ordinal", "command-p140-invalid-ordinal", domain.TargetKindRemote, "printf once")
	*intent.IntentOrdinal = 2
	stored := store.LocalIntentRecord{LocalIntentCreate: intent}
	frame := sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID)}
	for _, test := range []struct {
		name    string
		ordinal any
		present bool
	}{
		{name: "missing"},
		{name: "zero", ordinal: 0, present: true},
		{name: "negative", ordinal: -1, present: true},
		{name: "fraction", ordinal: 1.5, present: true},
		{name: "string", ordinal: "1", present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"command_id": string(intent.CommandID), "session_id": string(intent.SessionID)}
			if test.present {
				payload["ordinal"] = test.ordinal
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			reply := sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: encoded}
			if err := validateRemoteReply(stored, frame, reply); !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("dispatch reply validation error=%v, want ErrRemoteResponse", err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatal(err)
			}
			if err := validateReconciledResource(stored, object); !errors.Is(err, ErrRemoteNotReconciled) {
				t.Fatalf("reconciliation validation error=%v, want ErrRemoteNotReconciled", err)
			}
			object["command_state"] = json.RawMessage(`"queued"`)
			if _, present, err := remoteCommandProjectionFromReply(stored, object, time.Now().UTC()); present || !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("projection present=%v error=%v; malformed authority ordinal must fail closed", present, err)
			}
		})
	}

	badHash := p140CommandReply(string(intent.IntentID), stored, 1, "queued")
	var object map[string]json.RawMessage
	if err := json.Unmarshal(badHash.Payload, &object); err != nil {
		t.Fatal(err)
	}
	object["script_sha256"] = json.RawMessage(`"00"`)
	encoded, _ := json.Marshal(object)
	if err := validateReconciledResource(stored, object); !errors.Is(err, ErrRemoteNotReconciled) {
		t.Fatalf("reconciliation with wrong script hash error=%v, want ErrRemoteNotReconciled (payload %s)", err, encoded)
	}
}

type p140OrdinalCaller struct {
	frames            []sshbridge.RequestFrame
	intent            store.LocalIntentRecord
	authorityOrdinal  int64
	dropMutationReply bool
	mutationReply     sshbridge.ReplyFrame
}

func (c *p140OrdinalCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	switch frame.Operation {
	case sshbridge.OperationSubmitOrResumeCommand:
		reply := p140CommandReplyForFrame(frame, c.intent, c.authorityOrdinal, "queued")
		c.mutationReply = reply
		if c.dropMutationReply {
			return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("P140 simulated reply loss after Linux commit")}
		}
		return reply, nil
	case sshbridge.OperationGetCommand:
		return p140CommandReply(frame.RequestID, c.intent, c.authorityOrdinal, "queued"), nil
	default:
		return sshbridge.ReplyFrame{}, errors.New("unexpected P140 bridge operation")
	}
}

func p140CommandReplyForFrame(frame sshbridge.RequestFrame, intent store.LocalIntentRecord, ordinal int64, state string) sshbridge.ReplyFrame {
	return p140CommandReply(frame.RequestID, intent, ordinal, state)
}

func p140CommandReply(requestID string, intent store.LocalIntentRecord, ordinal int64, state string) sshbridge.ReplyFrame {
	payload, _ := p140CommandReplyJSON(intent, ordinal, state)
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: "result", Payload: payload}
}

func p140CommandReplyJSON(intent store.LocalIntentRecord, ordinal int64, state string) ([]byte, error) {
	digest := sha256.Sum256(intent.ScriptBytes)
	return json.Marshal(map[string]any{
		"command_id": string(intent.CommandID), "session_id": string(intent.SessionID), "ordinal": ordinal,
		"script_sha256": hex.EncodeToString(digest[:]), "command_state": state,
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"controller":       map[string]string{"controller_type": string(intent.Controller.Type()), "controller_id": string(intent.Controller.ID())},
		"environment":      intent.Environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands_per_host": 4}},
		"observed_at":  time.Now().UTC(),
	})
}

var _ RemoteCaller = (*p140OrdinalCaller)(nil)
