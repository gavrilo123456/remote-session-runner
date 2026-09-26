package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

var (
	ErrRemoteDriverConfiguration = errors.New("remote dispatcher configuration is invalid")
	ErrNoRemoteDispatchWork      = errors.New("no eligible remote dispatch work")
	ErrRemoteRejected            = errors.New("remote authority rejected local intent")
	ErrRemoteUncertain           = errors.New("remote authority acceptance is uncertain")
	ErrRemoteResponse            = errors.New("remote bridge response is invalid")
	ErrRemotePayload             = errors.New("remote bridge payload cannot be built")
)

// RemoteCaller is the identity-preserving bridge operation used by the remote
// driver. sshclient.Client implements it; tests can inject a bounded caller.
type RemoteCaller interface {
	Call(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error)
}

// RemoteDriver claims remote intents and forwards them through the complete
// SSH bridge. It does not create IDs or send caller-supplied payloads: every
// frame is reconstructed from the committed immutable local intent.
type RemoteDriver struct {
	authority     *store.AuthorityStore
	caller        RemoteCaller
	owner         string
	leaseDuration time.Duration
}

// NewRemoteDriver constructs a lease-owning remote driver.
func NewRemoteDriver(authority *store.AuthorityStore, caller RemoteCaller, owner string, leaseDuration time.Duration) (*RemoteDriver, error) {
	if authority == nil || caller == nil || owner == "" || len(owner) > 256 || strings.IndexByte(owner, 0) >= 0 || leaseDuration <= 0 {
		return nil, ErrRemoteDriverConfiguration
	}
	return &RemoteDriver{authority: authority, caller: caller, owner: owner, leaseDuration: leaseDuration}, nil
}

// DispatchNext selects the earliest eligible remote intent and delivers it
// with stable resource, request, idempotency, and command-ordinal identities.
func (d *RemoteDriver) DispatchNext(ctx context.Context) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	candidates, err := d.authority.ListEligibleLocalIntents(ctx, 1000)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	for _, candidate := range candidates {
		if candidate.Target.Kind() != domain.TargetKindRemote || !supportedRemoteOperation(candidate.Operation) {
			continue
		}
		return d.dispatchIntent(ctx, candidate.IntentID)
	}
	return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrNoRemoteDispatchWork
}

// DispatchIntent claims and delivers one remote intent by its stable ID.
func (d *RemoteDriver) DispatchIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	if intent.Target.Kind() != domain.TargetKindRemote {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w: target %s", ErrRemotePayload, intent.Target.Kind())
	}
	if !supportedRemoteOperation(intent.Operation) {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w: operation %s", ErrRemotePayload, intent.Operation)
	}
	return d.dispatchIntent(ctx, intent.IntentID)
}

func (d *RemoteDriver) dispatchIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	frame, err := frameForRemoteIntent(intent)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	claimed, err := d.authority.ClaimLocalIntent(ctx, intent.IntentID, d.owner, d.leaseDuration)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	reply, callErr := d.caller.Call(ctx, frame)
	if callErr != nil {
		next := store.LocalIntentUncertain
		reason := "remote_transport_uncertain"
		var transportErr *sshclient.TransportError
		if errors.As(callErr, &transportErr) && transportErr.Phase == sshclient.PhaseBeforeSend {
			next = store.LocalIntentNotDelivered
			reason = "remote_transport_before_send"
		}
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, next, reason)
		if transitionErr != nil {
			return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", callErr, transitionErr)
		}
		return claimed, sshbridge.ReplyFrame{}, callErr
	}
	if err := validateRemoteReply(intent, frame, reply); err != nil {
		next := store.LocalIntentUncertain
		reason := "remote_response_uncertain"
		if errors.Is(err, ErrRemoteRejected) {
			next = store.LocalIntentNotDelivered
			reason = "remote_rejected"
		}
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, next, reason)
		if transitionErr != nil {
			return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", err, transitionErr)
		}
		return claimed, reply, err
	}
	accepted, err := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentAccepted, "remote_target_accepted")
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	return accepted, reply, nil
}

func supportedRemoteOperation(operation string) bool {
	switch operation {
	case operationCreateSession, operationSubmitCommand, operationCancelCommand, operationCloseSession, "run":
		return true
	default:
		return false
	}
}

func frameForRemoteIntent(intent store.LocalIntentRecord) (sshbridge.RequestFrame, error) {
	payload, err := bridgePayloadForIntent(intent)
	if err != nil {
		return sshbridge.RequestFrame{}, err
	}
	var operation sshbridge.Operation
	switch intent.Operation {
	case operationCreateSession:
		operation = sshbridge.OperationCreateOrResumeSession
	case operationSubmitCommand:
		operation = sshbridge.OperationSubmitOrResumeCommand
	case operationCancelCommand:
		operation = sshbridge.OperationCancelCommand
	case operationCloseSession:
		operation = sshbridge.OperationCloseSession
	case "run":
		operation = sshbridge.OperationRunOrResumeJob
	default:
		return sshbridge.RequestFrame{}, fmt.Errorf("%w: unsupported operation %q", ErrRemotePayload, intent.Operation)
	}
	return sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       string(intent.IntentID),
		Operation:       operation,
		ResourceID:      intent.ResourceID,
		IdempotencyKey:  intent.IdempotencyKey,
		Payload:         payload,
	}, nil
}

func bridgePayloadForIntent(intent store.LocalIntentRecord) (json.RawMessage, error) {
	var source map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(intent.PayloadJSON))
	if err := decoder.Decode(&source); err != nil || source == nil {
		return nil, fmt.Errorf("%w: payload object", ErrRemotePayload)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: payload trailing JSON", ErrRemotePayload)
	}
	result := make(map[string]json.RawMessage)
	copyField := func(name string) error {
		if value, ok := source[name]; ok {
			result[name] = append(json.RawMessage(nil), value...)
		}
		return nil
	}
	required := func(name string) error {
		if _, ok := source[name]; !ok {
			return fmt.Errorf("%w: missing %s", ErrRemotePayload, name)
		}
		return copyField(name)
	}
	switch intent.Operation {
	case operationCreateSession:
		if err := required("environment"); err != nil {
			return nil, err
		}
		if err := required("execution_target"); err != nil {
			return nil, err
		}
		for _, name := range []string{"source", "limits", "isolation"} {
			if err := copyField(name); err != nil {
				return nil, err
			}
		}
	case operationSubmitCommand:
		if intent.IntentOrdinal == nil || *intent.IntentOrdinal <= 0 {
			return nil, fmt.Errorf("%w: submit intent ordinal", ErrRemotePayload)
		}
		result["session_id"] = json.RawMessage(strconvQuote(string(intent.SessionID)))
		ordinal, _ := json.Marshal(*intent.IntentOrdinal)
		result["intent_ordinal"] = ordinal
		script, _ := json.Marshal(string(intent.ScriptBytes))
		result["script"] = script
		if err := copyField("timeout_seconds"); err != nil {
			return nil, err
		}
	case operationCancelCommand:
		commandID, _ := json.Marshal(string(intent.CommandID))
		result["command_id"] = commandID
	case operationCloseSession:
		sessionID, _ := json.Marshal(string(intent.SessionID))
		result["session_id"] = sessionID
		if err := copyField("policy"); err != nil {
			return nil, err
		}
	case "run":
		for _, name := range []string{"environment", "execution_target"} {
			if err := required(name); err != nil {
				return nil, err
			}
		}
		for _, name := range []string{"source", "limits", "isolation", "policy"} {
			if err := copyField(name); err != nil {
				return nil, err
			}
		}
		sessionID, _ := json.Marshal(string(intent.SessionID))
		commandID, _ := json.Marshal(string(intent.CommandID))
		result["session_id"] = sessionID
		result["command_id"] = commandID
		script, _ := json.Marshal(string(intent.ScriptBytes))
		result["script"] = script
	default:
		return nil, fmt.Errorf("%w: unsupported operation %q", ErrRemotePayload, intent.Operation)
	}
	serialized, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrRemotePayload, err)
	}
	return serialized, nil
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func validateRemoteReply(intent store.LocalIntentRecord, frame sshbridge.RequestFrame, reply sshbridge.ReplyFrame) error {
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != frame.RequestID {
		return fmt.Errorf("%w: protocol or request identity mismatch", ErrRemoteResponse)
	}
	if reply.ResponseType == "error" {
		var payload sshbridge.ErrorPayload
		decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return fmt.Errorf("%w: error payload: %v", ErrRemoteResponse, err)
		}
		if payload.Code == "transport_uncertain" {
			return fmt.Errorf("%w: %s", ErrRemoteUncertain, payload.Message)
		}
		return fmt.Errorf("%w: %s", ErrRemoteRejected, payload.Message)
	}
	if reply.ResponseType != "result" {
		return fmt.Errorf("%w: response type %q", ErrRemoteResponse, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("%w: result object", ErrRemoteResponse)
	}
	field := ""
	switch intent.Operation {
	case operationCreateSession:
		field = "session_id"
	case operationSubmitCommand, operationCancelCommand:
		field = "command_id"
	case operationCloseSession:
		field = "session_id"
	case "run":
		field = "job_id"
	}
	var resourceID string
	if err := json.Unmarshal(object[field], &resourceID); err != nil || resourceID != intent.ResourceID {
		return fmt.Errorf("%w: %s identity", ErrRemoteResponse, field)
	}
	if intent.Operation == operationSubmitCommand && intent.IntentOrdinal != nil {
		var ordinal int64
		if raw, ok := object["ordinal"]; !ok || json.Unmarshal(raw, &ordinal) != nil || ordinal != *intent.IntentOrdinal {
			return fmt.Errorf("%w: command ordinal mismatch", ErrRemoteResponse)
		}
	}
	return nil
}
